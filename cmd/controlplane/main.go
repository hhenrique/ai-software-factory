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
	"factory/internal/repositories"
	"factory/internal/taskintake"
	"factory/internal/temporalconn"
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
	mux.HandleFunc("GET /api/workflows", listWorkflowsHandler(d.workflowsDir))
	mux.HandleFunc("GET /api/workflow-graph", workflowGraphHandler(d.workflowsDir))
	mux.HandleFunc("GET /api/workers", listWorkersHandler(d.workflowsDir))
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
// fresh from disk on each request. Also backs the Repositories form's
// "Default workflow" combobox (the .Path field), which is why this
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

// RoleInfo is one entry of a Workflow Definition's roles: block — the raw
// material docs/04's Workers (Roles) section aggregates by (harness,
// model) into WorkerInfo below, rather than its own registry (roles live
// inline in each Workflow Definition, not as standalone data).
type RoleInfo struct {
	Name    string            `json:"name"`
	Harness string            `json:"harness"`
	Model   string            `json:"model"`
	Params  map[string]string `json:"params,omitempty"`
}

// listWorkflowsHandler scans dir for *.yaml/*.yml files and parses +
// validates each one — docs/04's Workflows section: "Validation status
// surfaced before a Workflow can be made active." A file that fails to
// parse or validate is still listed, Valid: false with Errors set, rather
// than silently dropped — the whole point is surfacing the problem, not
// hiding it.
func listWorkflowsHandler(dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		infos, err := listWorkflowInfo(dir)
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
// listWorkflowFiles. A per-file parse/validate failure becomes an invalid
// WorkflowInfo entry, not a request-level error — one broken YAML file
// shouldn't hide every other one from the list.
func listWorkflowInfo(dir string) ([]WorkflowInfo, error) {
	files, err := listWorkflowFiles(dir)
	if err != nil {
		return nil, err
	}
	infos := make([]WorkflowInfo, len(files))
	for i, path := range files {
		infos[i] = loadWorkflowInfo(path)
	}
	return infos, nil
}

func loadWorkflowInfo(path string) WorkflowInfo {
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
	for name, role := range def.Roles {
		info.Roles = append(info.Roles, RoleInfo{Name: name, Harness: role.Harness, Model: role.Model, Params: role.Params})
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

// WorkflowGraph is the full step graph behind a Workflow Definition —
// what the workflow_v1/v2/v3 visualization prototypes render. Separate
// from WorkflowInfo (which is a summary for the Workflows list): a graph
// view needs every step and every edge, not just counts.
type WorkflowGraph struct {
	Workflow string      `json:"workflow"`
	Path     string      `json:"path"`
	Nodes    []GraphNode `json:"nodes"`
	Edges    []GraphEdge `json:"edges"`
}

// GraphNode is one step, or one of the terminal states (COMPLETED/FAILED/
// CANCELLED/REVIEW_PENDING) an edge points at — terminals are real nodes
// here (Kind: "terminal") since a graph with dangling edges pointing at
// nothing isn't a graph a human can read.
type GraphNode struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"` // "tool" | "agent" | "terminal"
	Role   string `json:"role,omitempty"`
	Action string `json:"action,omitempty"`
	Budget string `json:"budget,omitempty"`
}

// GraphEdge is one routing possibility out of a step: an unconditional
// `next:`, one entry of an `on:` map (Label is the outcome, e.g. "pass"/
// "fail"/"budget_exhausted" — the last already appears as an ordinary
// `on:` key in every reference Workflow Definition, no special-casing
// needed), or `on_malformed_output` (Label: "malformed_output").
type GraphEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label,omitempty"`
}

// buildWorkflowGraph is a pure function of an already-parsed Definition —
// deliberately not requiring workflowdef.Validate to have passed: seeing
// the actual graph structure, including a broken one (e.g. an unbounded
// cycle), is exactly when a human most wants to look at it.
func buildWorkflowGraph(def *workflowdef.Definition, path string) WorkflowGraph {
	g := WorkflowGraph{Workflow: def.Workflow, Path: path}

	terminals := map[string]bool{}
	addTerminalNode := func(id string) {
		if workflowdef.IsTerminalState(id) && !terminals[id] {
			terminals[id] = true
			g.Nodes = append(g.Nodes, GraphNode{ID: id, Kind: "terminal"})
		}
	}

	for _, step := range def.Steps {
		g.Nodes = append(g.Nodes, GraphNode{
			ID: step.ID, Kind: string(step.Type), Role: step.Role, Action: step.Action, Budget: step.Budget,
		})
	}

	for _, step := range def.Steps {
		if step.Next != "" {
			addTerminalNode(step.Next)
			g.Edges = append(g.Edges, GraphEdge{From: step.ID, To: step.Next})
		}
		for outcome, target := range step.On {
			dest := target.Destination()
			addTerminalNode(dest)
			g.Edges = append(g.Edges, GraphEdge{From: step.ID, To: dest, Label: outcome})
		}
		if step.OnMalformedOutput != "" {
			addTerminalNode(step.OnMalformedOutput)
			g.Edges = append(g.Edges, GraphEdge{From: step.ID, To: step.OnMalformedOutput, Label: "malformed_output"})
		}
	}

	// step.On is a map — Go's iteration order is random. Sort for stable,
	// diffable JSON output rather than a different edge order per request.
	sort.Slice(g.Edges, func(i, j int) bool {
		if g.Edges[i].From != g.Edges[j].From {
			return g.Edges[i].From < g.Edges[j].From
		}
		return g.Edges[i].Label < g.Edges[j].Label
	})

	return g
}

// workflowGraphHandler serves one Workflow Definition's full graph, keyed
// by ?path=. path must be one of the files listWorkflowFiles finds under
// dir — checked by membership rather than trusted directly, since unlike
// the directory scan itself (never client-influenced), this handler's
// input is a query parameter and a bare os.ReadFile(path) on it would be
// an arbitrary local file read.
func workflowGraphHandler(dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Query().Get("path")
		if path == "" {
			http.Error(w, "path is required", http.StatusBadRequest)
			return
		}
		files, err := listWorkflowFiles(dir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		known := false
		for _, f := range files {
			if f == path {
				known = true
				break
			}
		}
		if !known {
			http.Error(w, fmt.Sprintf("unknown workflow path %q", path), http.StatusNotFound)
			return
		}

		data, err := os.ReadFile(path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		def, err := workflowdef.Parse(data)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, buildWorkflowGraph(def, path))
	}
}

// WorkerInfo is docs/04's Workers (Roles) section: "List of configured
// roles (name, harness, model/endpoint). Which Workflows reference each
// role (so changing a role's backing model shows its blast radius)." A
// "role" isn't a standalone entity anywhere in this system — it's inline
// per-Workflow-Definition config (RoleInfo) — so this aggregates by the
// actual thing a config change touches, the (harness, model) pair, not by
// role name: two workflows' "coder" roles pointing at the same
// harness/model are the same blast radius even if named differently, and
// the same role name in two workflows pointing at different models is
// not. Concurrency limits (also listed in that doc section) aren't here:
// nothing in this system tracks or enforces them yet ("if needed" in the
// doc's own words) — there's no data to surface, so this doesn't
// fabricate a column for it.
type WorkerInfo struct {
	Harness string      `json:"harness"`
	Model   string      `json:"model"`
	Usages  []RoleUsage `json:"usages"`
}

// RoleUsage is one (Workflow, role name) pair backed by a WorkerInfo's
// (harness, model) — the "blast radius" entries docs/04 asks for.
type RoleUsage struct {
	WorkflowPath string `json:"workflow_path"`
	Workflow     string `json:"workflow"`
	Role         string `json:"role"`
	Effort       string `json:"effort,omitempty"`
}

// listWorkersHandler derives the Workers (Roles) view from the same scan
// listWorkflowsHandler does — no separate registry, since roles live
// inline in each Workflow Definition (see WorkerInfo's doc comment).
func listWorkersHandler(dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		infos, err := listWorkflowInfo(dir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, aggregateWorkers(infos))
	}
}

// aggregateWorkers groups every role usage across infos by (harness,
// model), sorted for stable output. Roles from a file that failed
// overall validation are still included — a role's harness/model is
// meaningful data on its own even if some unrelated part of that file
// (e.g. a step's context: list) has a separate problem; only a file that
// failed to parse at all contributes no roles (WorkflowInfo.Roles is
// simply empty in that case).
func aggregateWorkers(infos []WorkflowInfo) []WorkerInfo {
	type key struct{ harness, model string }
	byKey := map[key]*WorkerInfo{}
	var order []key

	for _, info := range infos {
		for _, role := range info.Roles {
			k := key{role.Harness, role.Model}
			w, ok := byKey[k]
			if !ok {
				w = &WorkerInfo{Harness: role.Harness, Model: role.Model}
				byKey[k] = w
				order = append(order, k)
			}
			w.Usages = append(w.Usages, RoleUsage{
				WorkflowPath: info.Path,
				Workflow:     info.Workflow,
				Role:         role.Name,
				Effort:       role.Params["effort"],
			})
		}
	}

	sort.Slice(order, func(i, j int) bool {
		if order[i].harness != order[j].harness {
			return order[i].harness < order[j].harness
		}
		return order[i].model < order[j].model
	})

	workers := make([]WorkerInfo, len(order))
	for i, k := range order {
		workers[i] = *byKey[k]
	}
	return workers
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

		repo, err := repositories.Insert(r.Context(), pool, name, cloneURL, req.TestCommand, req.DefaultWorkflow)
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
}

func updateRepositoryHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req updateRepositoryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "malformed JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}
		repo, err := repositories.Update(r.Context(), pool, req.Name, req.TestCommand, req.DefaultWorkflow)
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
