// Command controlplane is the real control plane (docs/04) — built in
// vertical slices, one section at a time. This slice: Repositories only.
// Unlike cmd/runsview (a throwaway server-rendered visibility tool, see
// its own doc comment), this is meant to be preserved and extended: a
// small SPA (one HTML shell + vanilla JS hitting a JSON API, no
// framework/build step) with a collapsible left-side nav that grows one
// section at a time as each slice lands. Static assets are embedded so
// the binary stays self-contained, same spirit as every other cmd/* here.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/client"

	"factory/internal/backlog"
	"factory/internal/conductor"
	"factory/internal/eventlog"
	"factory/internal/harnesslimits"
	"factory/internal/inbox"
	"factory/internal/repoconfig"
	"factory/internal/repositories"
	"factory/internal/roleassignment"
	"factory/internal/settings"
	"factory/internal/taskintake"
	"factory/internal/temporalconn"
	"factory/internal/workers"
	"factory/internal/workflowdef"
)

//go:embed static
var staticFS embed.FS

// deps holds the dependencies shared by handlers that need more than a
// bare Postgres pool (Work's task creation, later Inbox's signals) — a
// real Temporal client and its own config, not just the projection store.
// Handlers that only ever needed the pool (Repositories, Workflows,
// Workers) keep their existing pool-only constructor signature; this
// struct is additive, not a rewrite of those.
type deps struct {
	pool          *pgxpool.Pool
	temporal      client.Client
	taskQueue     string
	harnessLimits map[string]int
	workflowsDir  string
}

func main() {
	ctx := context.Background()
	pool, err := eventlog.NewPool(ctx)
	if err != nil {
		log.Fatalf("controlplane: %v", err)
	}
	defer pool.Close()

	hostPort := envOr("TEMPORAL_HOST_PORT", "localhost:7233")
	namespace := envOr("TEMPORAL_NAMESPACE", "default")
	dialCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	temporalClient, err := temporalconn.DialWithRetry(dialCtx, hostPort, namespace, 45, 2*time.Second)
	cancel()
	if err != nil {
		log.Fatalf("controlplane: unable to dial Temporal at %s: %v", hostPort, err)
	}
	defer temporalClient.Close()

	harnessLimits, err := harnesslimits.ParseEnv()
	if err != nil {
		log.Fatalf("controlplane: %v", err)
	}

	d := &deps{
		pool:          pool,
		temporal:      temporalClient,
		taskQueue:     envOr("TASK_QUEUE", "factory-conductor"),
		harnessLimits: harnessLimits,
		workflowsDir:  envOr("CONTROLPLANE_WORKFLOWS_DIR", "workflows"),
	}

	addr := envOr("CONTROLPLANE_ADDR", ":8082")

	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("controlplane: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.FS(static)))
	mux.HandleFunc("GET /api/repositories", listRepositoriesHandler(pool))
	mux.HandleFunc("POST /api/repositories", createRepositoryHandler(pool))
	mux.HandleFunc("POST /api/repositories/enable", setEnabledHandler(pool, true))
	mux.HandleFunc("POST /api/repositories/disable", setEnabledHandler(pool, false))
	mux.HandleFunc("POST /api/repositories/update", updateRepositoryHandler(pool))
	mux.HandleFunc("POST /api/repositories/delete", deleteRepositoryHandler(pool))
	mux.HandleFunc("GET /api/workflows", listWorkflowsHandler(d.workflowsDir, pool))
	mux.HandleFunc("GET /api/workflow-graph", workflowGraphHandler(d.workflowsDir))
	mux.HandleFunc("GET /api/workers", listWorkersHandler(pool))
	mux.HandleFunc("POST /api/workers", createWorkerHandler(pool))
	mux.HandleFunc("POST /api/workers/update", updateWorkerHandler(pool))
	mux.HandleFunc("POST /api/workers/delete", deleteWorkerHandler(pool))
	mux.HandleFunc("GET /api/role-assignments", listRoleAssignmentsHandler(pool))
	mux.HandleFunc("POST /api/role-assignments", setRoleAssignmentHandler(d.workflowsDir, pool))
	mux.HandleFunc("POST /api/role-assignments/delete", deleteRoleAssignmentHandler(pool))
	mux.HandleFunc("GET /api/settings", listSettingsHandler(pool))
	mux.HandleFunc("POST /api/settings", setSettingHandler(pool))
	mux.HandleFunc("GET /api/tasks", listTasksHandler(d))
	mux.HandleFunc("POST /api/tasks", createTaskHandler(d))
	mux.HandleFunc("GET /api/inbox", listInboxHandler(d))
	mux.HandleFunc("POST /api/inbox/resume", resumeInboxHandler(d))
	mux.HandleFunc("POST /api/inbox/cancel", cancelInboxHandler(d))

	log.Printf("controlplane: listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("controlplane: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// WorkflowInfo is docs/04's Workflows section, in full now: a Workflow
// Definition is already checked-in YAML (workflows/), so unlike
// Repositories this needs no persistence — every field here is derived
// fresh from disk on each request, except each declared role's current
// Worker assignment (RoleInfo's Worker/Harness/Model/Params), which comes
// from the database (internal/roleassignment) — the one piece of a
// WorkflowInfo that isn't YAML content. Also backs the Repositories
// form's "Default workflow" combobox (the .Path field), which is why this
// existed before the rest of the section did.
type WorkflowInfo struct {
	Path     string `json:"path"`
	Workflow string `json:"workflow"`
	Version  int    `json:"version"`
	// StepIDs backs the Inbox view's resume-step combobox — the set of
	// values conductor.HumanDecision.ResumeStepID actually means something
	// for, given this Workflow.
	StepIDs    []string   `json:"step_ids,omitempty"`
	StepCount  int        `json:"step_count"`
	Roles      []RoleInfo `json:"roles"`
	HasTrigger bool       `json:"has_trigger"`
	Valid      bool       `json:"valid"`
	Errors     []string   `json:"errors,omitempty"`
}

// RoleInfo is one role this Workflow Definition's roles: list declares,
// enriched with whichever Worker (internal/workers) is currently assigned
// to play it for this Workflow (internal/roleassignment). WorkerID == 0
// means "declared but unassigned" — a real, visible state (not an error
// here; taskintake.Submit is what actually refuses to start a Run over
// it), so a human can see and fix it from the Workflows view.
type RoleInfo struct {
	Name     string            `json:"name"`
	WorkerID int64             `json:"worker_id,omitempty"`
	Worker   string            `json:"worker,omitempty"`
	Harness  string            `json:"harness,omitempty"`
	Model    string            `json:"model,omitempty"`
	Params   map[string]string `json:"params,omitempty"`
}

// listWorkflowsHandler scans dir for *.yaml/*.yml files and parses +
// validates each one — docs/04's Workflows section: "Validation status
// surfaced before a Workflow can be made active." A file that fails to
// parse or validate is still listed, Valid: false with Errors set, rather
// than silently dropped — the whole point is surfacing the problem, not
// hiding it.
func listWorkflowsHandler(dir string, pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		infos, err := listWorkflowInfo(r.Context(), dir, pool)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, infos)
	}
}

// listWorkflowFiles returns every *.yaml/*.yml file directly under dir,
// sorted. Not recursive — workflows/ has no subdirectories today, and
// there's no reason yet to walk one that might appear.
func listWorkflowFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("list workflow files in %s: %w", dir, err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := filepath.Ext(e.Name())
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		files = append(files, filepath.Join(dir, e.Name()))
	}
	sort.Strings(files)
	return files, nil
}

// listWorkflowInfo parses+validates every workflow file found by
// listWorkflowFiles, then enriches each declared role with its current
// Worker assignment — one role_assignments+workers query for the whole
// batch (currentRoleAssignments), not one per file. A per-file parse/
// validate failure becomes an invalid WorkflowInfo entry, not a
// request-level error — one broken YAML file shouldn't hide every other
// one from the list.
func listWorkflowInfo(ctx context.Context, dir string, pool *pgxpool.Pool) ([]WorkflowInfo, error) {
	files, err := listWorkflowFiles(dir)
	if err != nil {
		return nil, err
	}
	lookup, err := currentRoleAssignments(ctx, pool)
	if err != nil {
		return nil, err
	}
	infos := make([]WorkflowInfo, len(files))
	for i, path := range files {
		infos[i] = loadWorkflowInfo(path, lookup)
	}
	return infos, nil
}

// currentRoleAssignments fetches every role_assignments row joined with
// its Worker, keyed workflow -> role -> Worker, for loadWorkflowInfo to
// enrich each RoleInfo with.
func currentRoleAssignments(ctx context.Context, pool *pgxpool.Pool) (map[string]map[string]workers.Worker, error) {
	assignments, err := roleassignment.List(ctx, pool)
	if err != nil {
		return nil, err
	}
	allWorkers, err := workers.List(ctx, pool)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]workers.Worker, len(allWorkers))
	for _, w := range allWorkers {
		byID[w.ID] = w
	}

	lookup := map[string]map[string]workers.Worker{}
	for _, a := range assignments {
		w, ok := byID[a.WorkerID]
		if !ok {
			continue // shouldn't happen (worker_id is a foreign key) — skip defensively
		}
		if lookup[a.Workflow] == nil {
			lookup[a.Workflow] = map[string]workers.Worker{}
		}
		lookup[a.Workflow][a.Role] = w
	}
	return lookup, nil
}

func loadWorkflowInfo(path string, roleAssignments map[string]map[string]workers.Worker) WorkflowInfo {
	info := WorkflowInfo{Path: path}

	data, err := os.ReadFile(path)
	if err != nil {
		info.Errors = []string{err.Error()}
		return info
	}
	def, err := workflowdef.Parse(data)
	if err != nil {
		info.Errors = []string{"parse: " + err.Error()}
		return info
	}

	info.Workflow = def.Workflow
	info.Version = def.Version
	info.StepCount = len(def.Steps)
	info.HasTrigger = def.Trigger != nil
	for _, step := range def.Steps {
		info.StepIDs = append(info.StepIDs, step.ID)
	}
	for _, name := range def.Roles {
		ri := RoleInfo{Name: name}
		if w, ok := roleAssignments[def.Workflow][name]; ok {
			ri.WorkerID = w.ID
			ri.Worker = w.Name
			ri.Harness = w.Harness
			ri.Model = w.Model
			ri.Params = w.Params
		}
		info.Roles = append(info.Roles, ri)
	}
	sort.Slice(info.Roles, func(i, j int) bool { return info.Roles[i].Name < info.Roles[j].Name })

	if errs := workflowdef.Validate(def); len(errs) != 0 {
		for _, e := range errs {
			info.Errors = append(info.Errors, e.Error())
		}
		return info
	}
	info.Valid = true
	return info
}

// WorkerInfo is a persisted Worker (internal/workers) plus its current
// usages — every (workflow, role) pair internal/roleassignment currently
// points at it, docs/04's "blast radius of changing a role's backing
// model." Unlike the old scan-and-derive-from-YAML version, this is a
// direct worker_id foreign-key join now, not content-equality grouping by
// (harness, model): two Workers configured identically are still two
// distinct rows here, since a Worker has real identity (a name, an id)
// independent of what it happens to be configured with.
type WorkerInfo struct {
	ID      int64             `json:"id"`
	Name    string            `json:"name"`
	Harness string            `json:"harness"`
	Model   string            `json:"model"`
	Params  map[string]string `json:"params,omitempty"`
	Usages  []RoleUsage       `json:"usages"`
}

// RoleUsage is one (Workflow, role name) pair currently assigned to a
// WorkerInfo — the "blast radius" entries docs/04 asks for.
type RoleUsage struct {
	Workflow string `json:"workflow"`
	Role     string `json:"role"`
}

// listWorkersHandler lists every persisted Worker enriched with its
// current usages — replaces the old scan-and-aggregate-by-(harness,model)
// derivation now that Workers are real rows (internal/workers), not
// inline YAML config.
func listWorkersHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		all, err := workers.List(r.Context(), pool)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		assignments, err := roleassignment.List(r.Context(), pool)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, buildWorkerInfos(all, assignments))
	}
}

func buildWorkerInfos(all []workers.Worker, assignments []roleassignment.Assignment) []WorkerInfo {
	usagesByWorkerID := map[int64][]RoleUsage{}
	for _, a := range assignments {
		usagesByWorkerID[a.WorkerID] = append(usagesByWorkerID[a.WorkerID], RoleUsage{Workflow: a.Workflow, Role: a.Role})
	}
	infos := make([]WorkerInfo, len(all))
	for i, w := range all {
		// Usages: []RoleUsage{}, not usagesByWorkerID[w.ID] directly — a
		// map miss is a nil slice, which encoding/json renders as `null`
		// rather than `[]`, and the Workers view calls .length/.map on
		// this field unconditionally.
		usages := usagesByWorkerID[w.ID]
		if usages == nil {
			usages = []RoleUsage{}
		}
		infos[i] = WorkerInfo{ID: w.ID, Name: w.Name, Harness: w.Harness, Model: w.Model, Params: w.Params, Usages: usages}
	}
	return infos
}

// createWorkerRequest is /api/workers' POST body.
type createWorkerRequest struct {
	Name    string            `json:"name"`
	Harness string            `json:"harness"`
	Model   string            `json:"model"`
	Params  map[string]string `json:"params"`
}

func createWorkerHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createWorkerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "malformed JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}
		worker, err := workers.Create(r.Context(), pool, req.Name, req.Harness, req.Model, req.Params)
		if errors.Is(err, workers.ErrUnknownHarness) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusCreated, worker)
	}
}

// idRequest is the body for every /api/workers/{action} endpoint keyed
// by id only.
type idRequest struct {
	ID int64 `json:"id"`
}

// updateWorkerRequest is /api/workers/update's body.
type updateWorkerRequest struct {
	ID      int64             `json:"id"`
	Name    string            `json:"name"`
	Harness string            `json:"harness"`
	Model   string            `json:"model"`
	Params  map[string]string `json:"params"`
}

func updateWorkerHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req updateWorkerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "malformed JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}
		worker, err := workers.Update(r.Context(), pool, req.ID, req.Name, req.Harness, req.Model, req.Params)
		if errors.Is(err, workers.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if errors.Is(err, workers.ErrUnknownHarness) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, worker)
	}
}

func deleteWorkerHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req idRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "malformed JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}
		err := workers.Delete(r.Context(), pool, req.ID)
		if errors.Is(err, workers.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if errors.Is(err, workers.ErrInUse) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// listRoleAssignmentsHandler returns every current (workflow, role) ->
// worker_id assignment — small, whole-table data the Workflows view
// fetches once and reads client-side, same convention as /api/workflows
// and /api/workers.
func listRoleAssignmentsHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		assignments, err := roleassignment.List(r.Context(), pool)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, assignments)
	}
}

// setRoleAssignmentRequest is /api/role-assignments' POST body.
type setRoleAssignmentRequest struct {
	Workflow string `json:"workflow"`
	Role     string `json:"role"`
	WorkerID int64  `json:"worker_id"`
}

// setRoleAssignmentHandler validates workflow against the actual workflow
// files on disk before delegating to roleassignment.Set, which has no
// notion of a workflows directory to check that against itself (see its
// doc comment).
func setRoleAssignmentHandler(dir string, pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req setRoleAssignmentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "malformed JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}
		known, err := knownWorkflowNames(dir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !known[req.Workflow] {
			http.Error(w, fmt.Sprintf("unknown workflow %q", req.Workflow), http.StatusBadRequest)
			return
		}
		assignment, err := roleassignment.Set(r.Context(), pool, req.Workflow, req.Role, req.WorkerID)
		if errors.Is(err, roleassignment.ErrUnknownRole) || errors.Is(err, roleassignment.ErrUnknownWorker) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, assignment)
	}
}

// deleteRoleAssignmentRequest is /api/role-assignments/delete's body —
// unassigning a role (picking "(unassigned)" in the Workflows view) is a
// real, valid state, distinct from Set with a worker_id of 0 (which would
// just fail role_assignments' foreign key).
type deleteRoleAssignmentRequest struct {
	Workflow string `json:"workflow"`
	Role     string `json:"role"`
}

func deleteRoleAssignmentHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req deleteRoleAssignmentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "malformed JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}
		err := roleassignment.Delete(r.Context(), pool, req.Workflow, req.Role)
		if errors.Is(err, roleassignment.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// knownWorkflowNames scans dir the same way listWorkflowFiles does,
// returning the set of Workflow Definition names found — parse-only (not
// full Validate), since even a currently-invalid file's name is still a
// legitimate role_assignments target: fixing the YAML and fixing its role
// assignment aren't ordered relative to each other.
func knownWorkflowNames(dir string) (map[string]bool, error) {
	files, err := listWorkflowFiles(dir)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		def, err := workflowdef.Parse(data)
		if err != nil {
			continue
		}
		names[def.Workflow] = true
	}
	return names, nil
}

// listTasksHandler backs docs/04's Work section — every backlog Task,
// both sources (human-submitted via createTaskHandler/cmd/submittask, and
// auto-generated:review-finding via internal/backlog.CreateTask).
func listTasksHandler(d *deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tasks, err := backlog.List(r.Context(), d.pool)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, tasks)
	}
}

// createTaskRequest is the Work section's "create Task" form: pick an
// already-registered, enabled Repository by name rather than re-entering
// a clone URL/test command (those live on the Repository, see
// internal/repositories) — Workflow defaults to that repo's
// default_workflow when left blank, same precedence taskintake.Submit
// otherwise expects a caller to have already resolved.
type createTaskRequest struct {
	RepoName     string `json:"repo_name"`
	WorkflowFile string `json:"workflow_file"`
	Description  string `json:"description"`
	GitHubIssue  int    `json:"github_issue"`
}

// createTaskResponse mirrors cmd/submittask's own stdout — task/run id
// and which workflow actually ran, plus AttachRunWarning surfaced as a
// string rather than swallowed (see taskintake.Result's doc comment).
type createTaskResponse struct {
	TaskID           string `json:"task_id"`
	RunID            string `json:"run_id"`
	Workflow         string `json:"workflow"`
	AttachRunWarning string `json:"attach_run_warning,omitempty"`
}

// createTaskHandler is the UI's "Delegate task" action — it does exactly
// what cmd/submittask does (taskintake.Submit), because it's the same
// package, not a reimplementation. This is the one control-plane action
// in the whole app that spends real money the moment it returns
// successfully: every real harness.invoke call in the Run it just started
// costs real API credits. The UI is expected to confirm that with a human
// before calling this — see cmd/controlplane/static/app.js's Work view.
func createTaskHandler(d *deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createTaskRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "malformed JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.RepoName == "" {
			http.Error(w, "repo_name is required", http.StatusBadRequest)
			return
		}

		repo, err := repositories.Get(r.Context(), d.pool, req.RepoName)
		if errors.Is(err, repositories.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !repo.Enabled {
			http.Error(w, fmt.Sprintf("repository %q is disabled", req.RepoName), http.StatusConflict)
			return
		}

		workflowFile := req.WorkflowFile
		if workflowFile == "" {
			workflowFile = repo.DefaultWorkflow
		}

		result, err := taskintake.Submit(r.Context(), taskintake.Deps{
			Pool:           d.pool,
			TemporalClient: d.temporal,
			TaskQueue:      d.taskQueue,
			HarnessLimits:  d.harnessLimits,
		}, taskintake.Params{
			Repo: conductor.Repo{
				Name:        req.RepoName,
				CloneURL:    repo.CloneURL,
				TestCommand: repo.TestCommand,
			},
			WorkflowFile: workflowFile,
			Description:  req.Description,
			GitHubIssue:  req.GitHubIssue,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		resp := createTaskResponse{TaskID: result.TaskID, RunID: result.RunID, Workflow: result.Workflow}
		if result.AttachRunWarning != nil {
			resp.AttachRunWarning = result.AttachRunWarning.Error()
		}
		writeJSON(w, http.StatusCreated, resp)
	}
}

// listInboxHandler backs the Inbox view — every Run currently parked at
// REVIEW_PENDING, per internal/inbox.List's doc comment on what "narrow"
// means here (not a general Runs browser).
func listInboxHandler(d *deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pending, err := inbox.List(r.Context(), d.pool)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, pending)
	}
}

// resumeInboxRequest mirrors conductor.HumanDecision's resume fields —
// RunID is Temporal's workflow id (== Run id, doc 05), not a backlog
// Task id.
type resumeInboxRequest struct {
	RunID        string `json:"run_id"`
	ResumeStepID string `json:"resume_step_id"`
	Hint         string `json:"hint"`
}

func resumeInboxHandler(d *deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req resumeInboxRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "malformed JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.RunID == "" {
			http.Error(w, "run_id is required", http.StatusBadRequest)
			return
		}
		if err := inbox.SignalResume(r.Context(), d.temporal, req.RunID, req.ResumeStepID, req.Hint); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type cancelInboxRequest struct {
	RunID string `json:"run_id"`
}

func cancelInboxHandler(d *deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req cancelInboxRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "malformed JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.RunID == "" {
			http.Error(w, "run_id is required", http.StatusBadRequest)
			return
		}
		if err := inbox.SignalCancel(r.Context(), d.temporal, req.RunID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func listRepositoriesHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		repos, err := repositories.List(r.Context(), pool)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, repos)
	}
}

// createRepositoryRequest mirrors the UI's "Canonical identity" field
// (e.g. "github.com/hhenrique/toy-repo") rather than asking for a raw
// clone URL directly — GitHub is the only provider this slice manages, so
// the identity alone is enough to derive one.
type createRepositoryRequest struct {
	Identity        string `json:"identity"`
	TestCommand     string `json:"test_command"`
	DefaultWorkflow string `json:"default_workflow"`
	WorktreeRoot    string `json:"worktree_root"`
}

func createRepositoryHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createRepositoryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "malformed JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}

		name, cloneURL, err := parseGitHubIdentity(req.Identity)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Empty is fine (inherit the global default); non-empty gets the
		// same validation as the Settings view's factory_root, same reason.
		if req.WorktreeRoot != "" {
			if err := repoconfig.ValidateRoot(req.WorktreeRoot); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}

		repo, err := repositories.Insert(r.Context(), pool, name, cloneURL, req.TestCommand, req.DefaultWorkflow, req.WorktreeRoot)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusCreated, repo)
	}
}

// nameRequest is the body for every /api/repositories/{action} endpoint
// keyed by name only — not a path segment, since a canonical identity
// contains slashes ("github.com/owner/repo").
type nameRequest struct {
	Name string `json:"name"`
}

func setEnabledHandler(pool *pgxpool.Pool, enabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req nameRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "malformed JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}
		err := repositories.SetEnabled(r.Context(), pool, req.Name, enabled)
		if errors.Is(err, repositories.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// updateRepositoryRequest is /api/repositories/update's body — name plus
// exactly the fields repositories.Update allows changing (not clone_url:
// see that function's doc comment for why a rename is a delete+re-add).
type updateRepositoryRequest struct {
	Name            string `json:"name"`
	TestCommand     string `json:"test_command"`
	DefaultWorkflow string `json:"default_workflow"`
	WorktreeRoot    string `json:"worktree_root"`
}

func updateRepositoryHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req updateRepositoryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "malformed JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.WorktreeRoot != "" {
			if err := repoconfig.ValidateRoot(req.WorktreeRoot); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		repo, err := repositories.Update(r.Context(), pool, req.Name, req.TestCommand, req.DefaultWorkflow, req.WorktreeRoot)
		if errors.Is(err, repositories.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, repo)
	}
}

func deleteRepositoryHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req nameRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "malformed JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}
		err := repositories.Delete(r.Context(), pool, req.Name)
		if errors.Is(err, repositories.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// listSettingsHandler returns every configured global setting — small,
// whole-table data, same convention as GET /api/workflows and
// GET /api/workers.
func listSettingsHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		all, err := settings.List(r.Context(), pool)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, all)
	}
}

// setSettingRequest is /api/settings' POST body.
type setSettingRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func setSettingHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req setSettingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "malformed JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Key == "" {
			http.Error(w, "key is required", http.StatusBadRequest)
			return
		}
		// factory_root specifically gets validated before being persisted
		// — an accepted-but-wrong root (doesn't exist, isn't writable) is
		// exactly the earlier failure mode (silent until a Run failed deep
		// inside a mkdir) moved from an env var to this form instead of
		// actually fixed. Other keys have no path semantics to validate.
		if req.Key == repoconfig.FactoryRootKey {
			if err := repoconfig.ValidateRoot(req.Value); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		s, err := settings.Set(r.Context(), pool, req.Key, req.Value)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, s)
	}
}

// parseGitHubIdentity validates and splits a GitHub repo reference into a
// unique repository name (the canonical "github.com/owner/repo" form,
// verbatim) and an https clone URL — the same URL shape gitops.GitHubSlug
// expects back out of it, so a repository registered here is immediately
// usable by pr.create_and_link and cmd/submittask's gh issue lookups.
//
// Tolerates pasting a full clone URL, not just the bare identity the
// field's placeholder suggests — https://github.com/owner/repo,
// https://github.com/owner/repo.git, and http:// are all normalized down
// to the same canonical form, since a human copying a URL out of a
// browser or `git remote -v` shouldn't have to hand-edit it first.
func parseGitHubIdentity(identity string) (name, cloneURL string, err error) {
	identity = strings.TrimSpace(identity)
	identity = strings.TrimPrefix(identity, "https://")
	identity = strings.TrimPrefix(identity, "http://")
	identity = strings.TrimSuffix(identity, ".git")
	identity = strings.TrimSuffix(identity, "/")

	const prefix = "github.com/"
	if !strings.HasPrefix(identity, prefix) {
		return "", "", errors.New(`identity must look like "github.com/<owner>/<repo>" — GitHub is the only managed provider in this release`)
	}
	ownerRepo := strings.TrimPrefix(identity, prefix)
	parts := strings.Split(ownerRepo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", errors.New(`identity must look like "github.com/<owner>/<repo>"`)
	}
	return identity, "https://" + identity + ".git", nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("controlplane: encode response: %v", err)
	}
}
