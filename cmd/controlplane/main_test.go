package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"factory/internal/activities/stub"
	"factory/internal/backlog"
	"factory/internal/conductor"
	"factory/internal/eventlog"
	"factory/internal/inbox"
	"factory/internal/repositories"
	"factory/internal/roleassignment"
	"factory/internal/temporalconn"
	"factory/internal/workers"
	"factory/internal/workflowdef"
)

func requirePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pool, err := eventlog.NewPool(ctx)
	if err != nil {
		t.Skip("projection store not configured:", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skip("projection store not reachable (is `docker compose up` running?):", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// requireTemporal dials a real local Temporal, skipping if unreachable —
// tests that use this never need cmd/worker running: ExecuteWorkflow just
// durably registers the Run with Temporal, and nothing actually executes
// (no real harness call, no cost) until a worker polls the task queue. So
// a test using a private queue (e.g. startInboxTestWorker's tests below)
// truly costs nothing without cmd/worker running; TestCreateAndListTaskHandlers
// deliberately breaks that pattern — see its own comment.
func requireTemporal(t *testing.T) client.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := temporalconn.DialWithRetry(ctx, "localhost:7233", "default", 3, 500*time.Millisecond)
	if err != nil {
		t.Skip("Temporal not reachable (is `docker compose up` running?):", err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestParseGitHubIdentity(t *testing.T) {
	name, cloneURL, err := parseGitHubIdentity("github.com/hhenrique/toy-repo")
	if err != nil {
		t.Fatalf("parseGitHubIdentity: %v", err)
	}
	if name != "github.com/hhenrique/toy-repo" {
		t.Errorf("name = %q", name)
	}
	if cloneURL != "https://github.com/hhenrique/toy-repo.git" {
		t.Errorf("cloneURL = %q", cloneURL)
	}

	for _, bad := range []string{"", "gitlab.com/a/b", "github.com/onlyowner", "github.com/"} {
		if _, _, err := parseGitHubIdentity(bad); err == nil {
			t.Errorf("parseGitHubIdentity(%q): expected an error", bad)
		}
	}
}

func TestParseGitHubIdentityTolerantOfPastedURLs(t *testing.T) {
	cases := []string{
		"https://github.com/hhenrique/toy-repo",
		"https://github.com/hhenrique/toy-repo.git",
		"http://github.com/hhenrique/toy-repo",
		"github.com/hhenrique/toy-repo/",
		"  github.com/hhenrique/toy-repo  ",
	}
	for _, in := range cases {
		name, cloneURL, err := parseGitHubIdentity(in)
		if err != nil {
			t.Errorf("parseGitHubIdentity(%q): %v", in, err)
			continue
		}
		if name != "github.com/hhenrique/toy-repo" {
			t.Errorf("parseGitHubIdentity(%q): name = %q, want github.com/hhenrique/toy-repo", in, name)
		}
		if cloneURL != "https://github.com/hhenrique/toy-repo.git" {
			t.Errorf("parseGitHubIdentity(%q): cloneURL = %q", in, cloneURL)
		}
	}
}

func TestListWorkflowFiles(t *testing.T) {
	files, err := listWorkflowFiles("../../workflows")
	if err != nil {
		t.Fatalf("listWorkflowFiles: %v", err)
	}
	found := false
	for _, f := range files {
		if f == "../../workflows/issue-to-pr-claude-only.yaml" {
			found = true
		}
	}
	if !found {
		t.Errorf("listWorkflowFiles = %v, want it to include issue-to-pr-claude-only.yaml", files)
	}
}

func TestListWorkflowFilesMissingDirErrors(t *testing.T) {
	if _, err := listWorkflowFiles("../../does-not-exist"); err == nil {
		t.Fatalf("expected an error for a missing directory")
	}
}

func TestLoadWorkflowInfoValidFile(t *testing.T) {
	// nil roleAssignments (no DB lookup) — this test is pure YAML parsing,
	// same as before harness/model moved to the database. Role enrichment
	// against a real assignment is covered by
	// TestListWorkflowInfoEnrichesRolesWithCurrentAssignment below.
	info := loadWorkflowInfo("../../workflows/issue-to-pr-claude-only.yaml", nil)
	if !info.Valid {
		t.Fatalf("Valid = false, Errors = %v", info.Errors)
	}
	if info.Workflow != "issue-to-pr-claude-only" {
		t.Errorf("Workflow = %q", info.Workflow)
	}
	if info.StepCount == 0 {
		t.Errorf("StepCount = 0, want > 0")
	}
	if len(info.Roles) != 3 {
		t.Fatalf("Roles = %v, want 3 (planner, coder, reviewer)", info.Roles)
	}
	for _, role := range info.Roles {
		if role.WorkerID != 0 || role.Harness != "" {
			t.Errorf("role %q = %+v, want unassigned (no roleAssignments lookup given)", role.Name, role)
		}
	}
}

func TestBuildWorkflowGraphIncludesTerminalNodesAndSortedEdges(t *testing.T) {
	def := workflowdef.Definition{
		Workflow: "graph-test", Version: 1,
		Steps: []workflowdef.Step{
			{
				ID: "plan", Type: workflowdef.StepTypeAgent, Role: "planner",
				OutputSchema: map[string]any{"verdict": []any{"proceed", "reject"}},
				On: map[string]workflowdef.Target{
					"proceed": {StepOrState: "execute"},
					"reject":  {StepOrState: "FAILED"},
				},
				OnMalformedOutput: "REVIEW_PENDING",
			},
			{ID: "execute", Type: workflowdef.StepTypeTool, Action: "run.tests_lint_build", Next: "COMPLETED"},
		},
	}

	g := buildWorkflowGraph(&def, "workflows/graph-test.yaml")
	if g.Workflow != "graph-test" || g.Path != "workflows/graph-test.yaml" {
		t.Errorf("Workflow/Path = %q/%q", g.Workflow, g.Path)
	}

	var nodeIDs []string
	for _, n := range g.Nodes {
		nodeIDs = append(nodeIDs, n.ID)
	}
	for _, want := range []string{"plan", "execute", "FAILED", "REVIEW_PENDING", "COMPLETED"} {
		found := false
		for _, id := range nodeIDs {
			if id == want {
				found = true
			}
		}
		if !found {
			t.Errorf("Nodes missing %q: %v", want, nodeIDs)
		}
	}

	// Exactly one terminal node per distinct terminal state reached, even
	// though FAILED/REVIEW_PENDING/COMPLETED are each referenced once here
	// — this asserts de-duplication, which matters more once two steps
	// route to the same terminal.
	terminalCount := 0
	for _, n := range g.Nodes {
		if n.Kind == "terminal" {
			terminalCount++
		}
	}
	if terminalCount != 3 {
		t.Errorf("terminal node count = %d, want 3", terminalCount)
	}

	if len(g.Edges) != 4 {
		t.Fatalf("len(Edges) = %d, want 4 (proceed, reject, malformed_output, execute->COMPLETED): %+v", len(g.Edges), g.Edges)
	}
	// Sorted by (From, Label): execute's unlabeled edge sorts before any
	// of plan's labeled edges for the same From, and "malformed_output" <
	// "proceed" < "reject" alphabetically.
	if g.Edges[0].From != "execute" || g.Edges[0].To != "COMPLETED" || g.Edges[0].Label != "" {
		t.Errorf("Edges[0] = %+v", g.Edges[0])
	}
	if g.Edges[1].From != "plan" || g.Edges[1].Label != "malformed_output" || g.Edges[1].To != "REVIEW_PENDING" {
		t.Errorf("Edges[1] = %+v", g.Edges[1])
	}
	if g.Edges[2].From != "plan" || g.Edges[2].Label != "proceed" || g.Edges[2].To != "execute" {
		t.Errorf("Edges[2] = %+v", g.Edges[2])
	}
	if g.Edges[3].From != "plan" || g.Edges[3].Label != "reject" || g.Edges[3].To != "FAILED" {
		t.Errorf("Edges[3] = %+v", g.Edges[3])
	}
}

func TestWorkflowGraphHandlerRealFile(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/workflow-graph?path=../../workflows/issue-to-pr-claude-only.yaml", nil)
	rec := httptest.NewRecorder()
	workflowGraphHandler("../../workflows")(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var g WorkflowGraph
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if g.Workflow != "issue-to-pr-claude-only" {
		t.Errorf("Workflow = %q", g.Workflow)
	}
	if len(g.Nodes) == 0 || len(g.Edges) == 0 {
		t.Errorf("expected non-empty Nodes/Edges, got %d/%d", len(g.Nodes), len(g.Edges))
	}
}

func TestWorkflowGraphHandlerRejectsPathOutsideWorkflowsDir(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/workflow-graph?path=../../go.mod", nil)
	rec := httptest.NewRecorder()
	workflowGraphHandler("../../workflows")(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (path traversal must be rejected)", rec.Code)
	}
}

func TestWorkflowGraphHandlerMissingPathParam(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/workflow-graph", nil)
	rec := httptest.NewRecorder()
	workflowGraphHandler("../../workflows")(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestLoadWorkflowInfoMissingFile(t *testing.T) {
	info := loadWorkflowInfo("../../workflows/does-not-exist.yaml", nil)
	if info.Valid {
		t.Fatalf("Valid = true for a missing file")
	}
	if len(info.Errors) == 0 {
		t.Errorf("expected a non-empty Errors")
	}
}

func TestListWorkflowInfoIncludesEveryFileEvenIfOneParticularFails(t *testing.T) {
	pool := requirePool(t)
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/good.yaml", []byte(`
workflow: good
version: 1
steps:
  - id: only
    type: tool
    action: noop
    next: COMPLETED
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/bad.yaml", []byte("not: [valid yaml"), 0o644); err != nil {
		t.Fatal(err)
	}

	infos, err := listWorkflowInfo(context.Background(), dir, pool)
	if err != nil {
		t.Fatalf("listWorkflowInfo: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("len(infos) = %d, want 2", len(infos))
	}

	byPath := map[string]WorkflowInfo{}
	for _, info := range infos {
		byPath[info.Path] = info
	}
	if !byPath[dir+"/good.yaml"].Valid {
		t.Errorf("good.yaml: Valid = false, Errors = %v", byPath[dir+"/good.yaml"].Errors)
	}
	if byPath[dir+"/bad.yaml"].Valid || len(byPath[dir+"/bad.yaml"].Errors) == 0 {
		t.Errorf("bad.yaml: expected Valid = false with a non-empty Errors, got %+v", byPath[dir+"/bad.yaml"])
	}
}

func TestBuildWorkerInfosGroupsUsagesByWorkerIDNotContent(t *testing.T) {
	// Two workers configured identically (same harness/model) stay two
	// distinct entries — unlike the old YAML-derived aggregation, a
	// Worker's identity is its row (id), not its content, since it's a
	// real persisted, independently-editable entity now.
	all := []workers.Worker{
		{ID: 1, Name: "Sonnet A", Harness: "claude-code", Model: "sonnet"},
		{ID: 2, Name: "Sonnet B", Harness: "claude-code", Model: "sonnet"},
	}
	assignments := []roleassignment.Assignment{
		{Workflow: "wf-a", Role: "coder", WorkerID: 1},
		{Workflow: "wf-a", Role: "reviewer", WorkerID: 1},
		{Workflow: "wf-b", Role: "planner", WorkerID: 2},
	}

	infos := buildWorkerInfos(all, assignments)
	if len(infos) != 2 {
		t.Fatalf("len(infos) = %d, want 2", len(infos))
	}

	byID := map[int64]WorkerInfo{}
	for _, w := range infos {
		byID[w.ID] = w
	}
	if len(byID[1].Usages) != 2 {
		t.Errorf("worker 1 usages = %+v, want 2 (coder+reviewer from wf-a)", byID[1].Usages)
	}
	if len(byID[2].Usages) != 1 || byID[2].Usages[0].Workflow != "wf-b" || byID[2].Usages[0].Role != "planner" {
		t.Errorf("worker 2 usages = %+v, want exactly wf-b's planner", byID[2].Usages)
	}
}

func TestBuildWorkerInfosEmptyInput(t *testing.T) {
	if infos := buildWorkerInfos(nil, nil); len(infos) != 0 {
		t.Errorf("buildWorkerInfos(nil, nil) = %v, want empty", infos)
	}
}

func TestListWorkersHandlerReflectsRealAssignments(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	suffix := time.Now().Format(time.RFC3339Nano)

	w, err := workers.Create(ctx, pool, "test-worker-"+suffix, "claude-code", "sonnet", map[string]string{"effort": "high"})
	if err != nil {
		t.Fatalf("workers.Create: %v", err)
	}
	workflow := "test-workflow-" + suffix
	if _, err := roleassignment.Set(ctx, pool, workflow, "coder", w.ID); err != nil {
		t.Fatalf("roleassignment.Set: %v", err)
	}
	t.Cleanup(func() {
		roleassignment.Delete(context.Background(), pool, workflow, "coder")
		workers.Delete(context.Background(), pool, w.ID)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/workers", nil)
	rec := httptest.NewRecorder()
	listWorkersHandler(pool)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var infos []WorkerInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &infos); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, info := range infos {
		if info.ID != w.ID {
			continue
		}
		for _, u := range info.Usages {
			if u.Workflow == workflow && u.Role == "coder" {
				return // found
			}
		}
		t.Fatalf("worker %d usages = %+v, missing %s/coder", w.ID, info.Usages, workflow)
	}
	t.Fatalf("created worker %d not found in response: %+v", w.ID, infos)
}

func TestCreateListEnableDisableRepositoryHandlers(t *testing.T) {
	pool := requirePool(t)
	name := "test-controlplane-" + time.Now().Format("20060102T150405.000000000")
	identity := "github.com/hhenrique/" + name

	createBody := `{"identity":"` + identity + `","test_command":"node --check script.js","default_workflow":"workflows/issue-to-pr-claude-only.yaml"}`
	req := httptest.NewRequest(http.MethodPost, "/api/repositories", strings.NewReader(createBody))
	rec := httptest.NewRecorder()
	createRepositoryHandler(pool)(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var created repositories.Repository
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("create: decode response: %v", err)
	}
	if created.Name != identity || !created.Enabled {
		t.Fatalf("create: unexpected response %+v", created)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/repositories", nil)
	rec = httptest.NewRecorder()
	listRepositoriesHandler(pool)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status = %d", rec.Code)
	}
	var listed []repositories.Repository
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("list: decode response: %v", err)
	}
	found := false
	for _, r := range listed {
		if r.Name == identity {
			found = true
		}
	}
	if !found {
		t.Fatalf("list: did not include %q", identity)
	}

	disableBody := `{"name":"` + identity + `"}`
	req = httptest.NewRequest(http.MethodPost, "/api/repositories/disable", strings.NewReader(disableBody))
	rec = httptest.NewRecorder()
	setEnabledHandler(pool, false)(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("disable: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	got, err := repositories.Get(context.Background(), pool, identity)
	if err != nil {
		t.Fatalf("Get after disable: %v", err)
	}
	if got.Enabled {
		t.Errorf("Enabled = true after disable")
	}

	req = httptest.NewRequest(http.MethodPost, "/api/repositories/enable", strings.NewReader(disableBody))
	rec = httptest.NewRecorder()
	setEnabledHandler(pool, true)(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("enable: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	updateBody := `{"name":"` + identity + `","test_command":"go test ./...","default_workflow":"workflows/other.yaml"}`
	req = httptest.NewRequest(http.MethodPost, "/api/repositories/update", strings.NewReader(updateBody))
	rec = httptest.NewRecorder()
	updateRepositoryHandler(pool)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var updated repositories.Repository
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("update: decode response: %v", err)
	}
	if updated.TestCommand != "go test ./..." || updated.DefaultWorkflow != "workflows/other.yaml" {
		t.Fatalf("update: unexpected response %+v", updated)
	}
	if updated.CloneURL != created.CloneURL {
		t.Fatalf("update: clone_url changed: got %q, want %q", updated.CloneURL, created.CloneURL)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/repositories/delete", strings.NewReader(disableBody))
	rec = httptest.NewRecorder()
	deleteRepositoryHandler(pool)(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if _, err := repositories.Get(context.Background(), pool, identity); !errors.Is(err, repositories.ErrNotFound) {
		t.Fatalf("Get after delete: err = %v, want ErrNotFound", err)
	}
}

func TestUpdateRepositoryHandlerUnknownNameReturns404(t *testing.T) {
	pool := requirePool(t)
	body := `{"name":"does-not-exist-` + time.Now().Format(time.RFC3339Nano) + `","test_command":"x","default_workflow":"y"}`
	req := httptest.NewRequest(http.MethodPost, "/api/repositories/update", strings.NewReader(body))
	rec := httptest.NewRecorder()
	updateRepositoryHandler(pool)(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestDeleteRepositoryHandlerUnknownNameReturns404(t *testing.T) {
	pool := requirePool(t)
	body := `{"name":"does-not-exist-` + time.Now().Format(time.RFC3339Nano) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/repositories/delete", strings.NewReader(body))
	rec := httptest.NewRecorder()
	deleteRepositoryHandler(pool)(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestSetEnabledHandlerUnknownNameReturns404(t *testing.T) {
	pool := requirePool(t)
	body := `{"name":"does-not-exist-` + time.Now().Format(time.RFC3339Nano) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/repositories/enable", strings.NewReader(body))
	rec := httptest.NewRecorder()
	setEnabledHandler(pool, true)(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestCreateRepositoryHandlerMalformedBody(t *testing.T) {
	pool := requirePool(t)
	req := httptest.NewRequest(http.MethodPost, "/api/repositories", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	createRepositoryHandler(pool)(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestCreateAndListTaskHandlers(t *testing.T) {
	pool := requirePool(t)
	temporal := requireTemporal(t)

	// Deliberately the same "factory-conductor" queue cmd/worker polls by
	// default, not a private one: toy-repo is a real testbench (real
	// harness calls, a real PR, real cost are expected and fine here) —
	// if a real worker is running, this Task is meant to actually execute
	// against it, same as any other real submission.
	d := &deps{
		pool:          pool,
		temporal:      temporal,
		taskQueue:     "factory-conductor",
		harnessLimits: nil,
		workflowsDir:  "../../workflows",
	}

	// A private copy of the real default workflow file under a
	// test-only workflow name, not "issue-to-pr-claude-only" itself:
	// role_assignments is keyed by workflow name, and this test's cleanup
	// unconditionally deletes what it Set — reusing the real production
	// workflow name would delete its real role assignments too (not
	// restore whatever was configured before the test ran).
	workflowName := "cp-task-test-workflow-" + time.Now().Format("20060102T150405.000000000")
	realYAML, err := os.ReadFile("../../workflows/issue-to-pr-claude-only.yaml")
	if err != nil {
		t.Fatalf("read real workflow file: %v", err)
	}
	testYAML := strings.Replace(string(realYAML), "workflow: issue-to-pr-claude-only", "workflow: "+workflowName, 1)
	workflowFile := filepath.Join(t.TempDir(), "workflow.yaml")
	if err := os.WriteFile(workflowFile, []byte(testYAML), 0o644); err != nil {
		t.Fatalf("write test workflow file: %v", err)
	}

	repoName := "github.com/hhenrique/cp-task-test-" + time.Now().Format("20060102T150405.000000000")
	if _, err := repositories.Insert(context.Background(), pool, repoName,
		"https://github.com/hhenrique/toy-repo.git", "true", workflowFile, ""); err != nil {
		t.Fatalf("repositories.Insert: %v", err)
	}
	t.Cleanup(func() {
		if err := repositories.Delete(context.Background(), pool, repoName); err != nil {
			t.Logf("cleanup repositories.Delete(%q): %v", repoName, err)
		}
	})

	// taskintake.Submit now resolves every declared role to a Worker via
	// internal/roleassignment before starting the Run — seed one worker
	// played across all three roles the test workflow declares, same as
	// any other real submission would need configured first.
	worker, err := workers.Create(context.Background(), pool, "cp-task-test-worker-"+time.Now().Format(time.RFC3339Nano), "claude-code", "sonnet", nil)
	if err != nil {
		t.Fatalf("workers.Create: %v", err)
	}
	for _, role := range []string{"planner", "coder", "reviewer"} {
		if _, err := roleassignment.Set(context.Background(), pool, workflowName, role, worker.ID); err != nil {
			t.Fatalf("roleassignment.Set(%s): %v", role, err)
		}
	}
	t.Cleanup(func() {
		ctx := context.Background()
		for _, role := range []string{"planner", "coder", "reviewer"} {
			roleassignment.Delete(ctx, pool, workflowName, role)
		}
		workers.Delete(ctx, pool, worker.ID)
	})

	body := `{"repo_name":"` + repoName + `","description":"a task created from the control plane"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(body))
	rec := httptest.NewRecorder()
	createTaskHandler(d)(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var created createTaskResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("create: decode response: %v", err)
	}
	if created.TaskID == "" || created.RunID == "" || created.Workflow != workflowName {
		t.Fatalf("create: unexpected response %+v", created)
	}
	// internal/backlog has no exported Delete, so this is test-only
	// cleanup via direct SQL, not a package API.
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM backlog_tasks WHERE task_id = $1`, created.TaskID); err != nil {
			t.Logf("cleanup backlog_tasks delete(%q): %v", created.TaskID, err)
		}
	})

	req = httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	rec = httptest.NewRecorder()
	listTasksHandler(d)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status = %d", rec.Code)
	}
	var listed []backlog.Task
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("list: decode response: %v", err)
	}
	found := false
	for _, task := range listed {
		if task.TaskID == created.TaskID {
			found = true
			if task.RunID != created.RunID {
				t.Errorf("listed task RunID = %q, want %q", task.RunID, created.RunID)
			}
			if task.Source != "human" {
				t.Errorf("listed task Source = %q, want human", task.Source)
			}
		}
	}
	if !found {
		t.Fatalf("list: did not include created task %q", created.TaskID)
	}
}

func TestCreateTaskHandlerUnknownRepoReturns404(t *testing.T) {
	pool := requirePool(t)
	d := &deps{pool: pool, workflowsDir: "../../workflows"}

	body := `{"repo_name":"does-not-exist-` + time.Now().Format(time.RFC3339Nano) + `","description":"x"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(body))
	rec := httptest.NewRecorder()
	createTaskHandler(d)(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s, want 404", rec.Code, rec.Body.String())
	}
}

func TestCreateTaskHandlerDisabledRepoReturnsConflict(t *testing.T) {
	pool := requirePool(t)
	d := &deps{pool: pool, workflowsDir: "../../workflows"}

	repoName := "github.com/hhenrique/cp-task-disabled-" + time.Now().Format("20060102T150405.000000000")
	if _, err := repositories.Insert(context.Background(), pool, repoName,
		"https://github.com/hhenrique/toy-repo.git", "true", "", ""); err != nil {
		t.Fatalf("repositories.Insert: %v", err)
	}
	t.Cleanup(func() {
		if err := repositories.Delete(context.Background(), pool, repoName); err != nil {
			t.Logf("cleanup repositories.Delete(%q): %v", repoName, err)
		}
	})
	if err := repositories.SetEnabled(context.Background(), pool, repoName, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}

	body := `{"repo_name":"` + repoName + `","description":"x"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(body))
	rec := httptest.NewRecorder()
	createTaskHandler(d)(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s, want 409", rec.Code, rec.Body.String())
	}
}

func TestCreateTaskHandlerRequiresRepoName(t *testing.T) {
	pool := requirePool(t)
	d := &deps{pool: pool, workflowsDir: "../../workflows"}

	req := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(`{"description":"x"}`))
	rec := httptest.NewRecorder()
	createTaskHandler(d)(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// startInboxTestWorker mirrors internal/inbox's own test helper (can't
// import unexported test helpers across packages) — a real worker on its
// own task queue, stub Activities only, so the Inbox HTTP handlers can be
// proven against a real signal round trip without cmd/worker or any real
// harness cost.
func startInboxTestWorker(t *testing.T, c client.Client, pool *pgxpool.Pool, taskQueue string) {
	t.Helper()
	w := worker.New(c, taskQueue, worker.Options{})
	w.RegisterWorkflow(conductor.RunWorkflow)

	eventActivities := &eventlog.Activities{Pool: pool}
	for name, fn := range eventActivities.Registrations() {
		w.RegisterActivityWithOptions(fn, activity.RegisterOptions{Name: name, DisableAlreadyRegisteredCheck: true})
	}
	for name, fn := range stub.Registrations {
		w.RegisterActivityWithOptions(fn, activity.RegisterOptions{Name: name, DisableAlreadyRegisteredCheck: true})
	}
	if err := w.Start(); err != nil {
		t.Fatalf("start test worker: %v", err)
	}
	t.Cleanup(w.Stop)
}

func waitForInboxEntry(t *testing.T, d *deps, runID string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		pending, err := inbox.List(context.Background(), d.pool)
		if err != nil {
			t.Fatalf("inbox.List: %v", err)
		}
		for _, p := range pending {
			if p.RunID == runID {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("run %q never reached REVIEW_PENDING within the deadline", runID)
}

func TestInboxHandlersListAndCancelRoundTrip(t *testing.T) {
	pool := requirePool(t)
	temporal := requireTemporal(t)
	taskQueue := "cp-inbox-test-" + time.Now().Format("20060102T150405.000000000")
	startInboxTestWorker(t, temporal, pool, taskQueue)
	d := &deps{pool: pool, temporal: temporal}

	runID := "cp-inbox-cancel-" + time.Now().Format("20060102T150405.000000000")
	def := workflowdef.Definition{
		Workflow: "cp-inbox-test", Version: 1,
		Steps: []workflowdef.Step{
			{ID: "verify", Type: workflowdef.StepTypeTool, Action: "run.tests_lint_build",
				On: map[string]workflowdef.Target{"pass": {StepOrState: "COMPLETED"}, "fail": {StepOrState: "REVIEW_PENDING"}}},
		},
	}
	run, err := temporal.ExecuteWorkflow(context.Background(), client.StartWorkflowOptions{ID: runID, TaskQueue: taskQueue},
		conductor.RunWorkflow, conductor.RunInput{Definition: def, FailVerifyUntilAttempt: 1})
	if err != nil {
		t.Fatalf("ExecuteWorkflow: %v", err)
	}
	waitForInboxEntry(t, d, runID)

	req := httptest.NewRequest(http.MethodGet, "/api/inbox", nil)
	rec := httptest.NewRecorder()
	listInboxHandler(d)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status = %d", rec.Code)
	}
	var pending []inbox.PendingRun
	if err := json.Unmarshal(rec.Body.Bytes(), &pending); err != nil {
		t.Fatalf("list: decode: %v", err)
	}
	found := false
	for _, p := range pending {
		if p.RunID == runID {
			found = true
		}
	}
	if !found {
		t.Fatalf("list did not include %q: %+v", runID, pending)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/inbox/cancel", strings.NewReader(`{"run_id":"`+runID+`"}`))
	rec = httptest.NewRecorder()
	cancelInboxHandler(d)(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("cancel: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var result conductor.RunResult
	if err := run.Get(ctx, &result); err != nil {
		t.Fatalf("workflow execution error: %v", err)
	}
	if result.FinalState != "CANCELLED" {
		t.Errorf("FinalState = %q, want CANCELLED", result.FinalState)
	}
}

func TestResumeInboxHandlerRequiresRunID(t *testing.T) {
	temporal := requireTemporal(t)
	d := &deps{temporal: temporal}

	req := httptest.NewRequest(http.MethodPost, "/api/inbox/resume", strings.NewReader(`{"resume_step_id":"verify"}`))
	rec := httptest.NewRecorder()
	resumeInboxHandler(d)(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestResumeInboxHandlerMissingResumeStepIDReturns400(t *testing.T) {
	temporal := requireTemporal(t)
	d := &deps{temporal: temporal}

	req := httptest.NewRequest(http.MethodPost, "/api/inbox/resume", strings.NewReader(`{"run_id":"does-not-exist"}`))
	rec := httptest.NewRecorder()
	resumeInboxHandler(d)(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestCancelInboxHandlerRequiresRunID(t *testing.T) {
	pool := requirePool(t)
	d := &deps{pool: pool}

	req := httptest.NewRequest(http.MethodPost, "/api/inbox/cancel", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	cancelInboxHandler(d)(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
