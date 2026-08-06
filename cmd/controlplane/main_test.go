package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
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
	"factory/internal/temporalconn"
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
// (no real harness call, no cost) until a worker polls the task queue.
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
	info := loadWorkflowInfo("../../workflows/issue-to-pr-claude-only.yaml")
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
		t.Errorf("Roles = %v, want 3 (planner, coder, reviewer)", info.Roles)
	}
	for _, role := range info.Roles {
		if role.Harness != "claude-code" {
			t.Errorf("role %q harness = %q, want claude-code", role.Name, role.Harness)
		}
	}
}

func TestLoadWorkflowInfoMissingFile(t *testing.T) {
	info := loadWorkflowInfo("../../workflows/does-not-exist.yaml")
	if info.Valid {
		t.Fatalf("Valid = true for a missing file")
	}
	if len(info.Errors) == 0 {
		t.Errorf("expected a non-empty Errors")
	}
}

func TestListWorkflowInfoIncludesEveryFileEvenIfOneParticularFails(t *testing.T) {
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

	infos, err := listWorkflowInfo(dir)
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

func TestAggregateWorkersGroupsByHarnessAndModelNotRoleName(t *testing.T) {
	infos := []WorkflowInfo{
		{
			Path: "wf-a.yaml", Workflow: "wf-a",
			Roles: []RoleInfo{
				{Name: "coder", Harness: "claude-code", Model: "sonnet", Params: map[string]string{"effort": "high"}},
				{Name: "reviewer", Harness: "claude-code", Model: "sonnet"},
			},
		},
		{
			Path: "wf-b.yaml", Workflow: "wf-b",
			Roles: []RoleInfo{
				// Different role name ("planner"), same (harness, model) as
				// wf-a's "coder"/"reviewer" — must land in the same group.
				{Name: "planner", Harness: "claude-code", Model: "sonnet"},
				// Same role name "coder" as wf-a, but a different model —
				// must NOT be grouped with wf-a's "coder".
				{Name: "coder", Harness: "codex", Model: "chatgpt-sol"},
			},
		},
	}

	workers := aggregateWorkers(infos)
	if len(workers) != 2 {
		t.Fatalf("len(workers) = %d, want 2 ((claude-code,sonnet) and (codex,chatgpt-sol))", len(workers))
	}

	byKey := map[string]WorkerInfo{}
	for _, w := range workers {
		byKey[w.Harness+"/"+w.Model] = w
	}

	claudeSonnet, ok := byKey["claude-code/sonnet"]
	if !ok {
		t.Fatalf("missing claude-code/sonnet group: %+v", workers)
	}
	if len(claudeSonnet.Usages) != 3 {
		t.Errorf("claude-code/sonnet usages = %v, want 3 (coder+reviewer from wf-a, planner from wf-b)", claudeSonnet.Usages)
	}
	var foundEffort bool
	for _, u := range claudeSonnet.Usages {
		if u.Role == "coder" && u.Workflow == "wf-a" && u.Effort == "high" {
			foundEffort = true
		}
	}
	if !foundEffort {
		t.Errorf("expected wf-a's coder usage to carry effort=high, got %+v", claudeSonnet.Usages)
	}

	codex, ok := byKey["codex/chatgpt-sol"]
	if !ok {
		t.Fatalf("missing codex/chatgpt-sol group: %+v", workers)
	}
	if len(codex.Usages) != 1 || codex.Usages[0].Workflow != "wf-b" || codex.Usages[0].Role != "coder" {
		t.Errorf("codex/chatgpt-sol usages = %+v, want exactly wf-b's coder", codex.Usages)
	}
}

func TestAggregateWorkersEmptyInput(t *testing.T) {
	if workers := aggregateWorkers(nil); len(workers) != 0 {
		t.Errorf("aggregateWorkers(nil) = %v, want empty", workers)
	}
}

func TestListWorkersHandlerReflectsRealWorkflowFiles(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/workers", nil)
	rec := httptest.NewRecorder()
	listWorkersHandler("../../workflows")(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var workers []WorkerInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &workers); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	found := false
	for _, w := range workers {
		if w.Harness == "claude-code" && w.Model == "sonnet" {
			found = true
			if len(w.Usages) != 3 {
				t.Errorf("claude-code/sonnet usages = %v, want 3 (planner/coder/reviewer from issue-to-pr-claude-only)", w.Usages)
			}
		}
	}
	if !found {
		t.Fatalf("expected a claude-code/sonnet worker group, got %+v", workers)
	}
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
	d := &deps{
		pool:          pool,
		temporal:      temporal,
		taskQueue:     "factory-conductor",
		harnessLimits: nil,
		workflowsDir:  "../../workflows",
	}

	repoName := "github.com/hhenrique/cp-task-test-" + time.Now().Format("20060102T150405.000000000")
	if _, err := repositories.Insert(context.Background(), pool, repoName,
		"https://github.com/hhenrique/toy-repo.git", "true", "../../workflows/issue-to-pr-claude-only.yaml"); err != nil {
		t.Fatalf("repositories.Insert: %v", err)
	}

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
	if created.TaskID == "" || created.RunID == "" || created.Workflow != "issue-to-pr-claude-only" {
		t.Fatalf("create: unexpected response %+v", created)
	}

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
		"https://github.com/hhenrique/toy-repo.git", "true", ""); err != nil {
		t.Fatalf("repositories.Insert: %v", err)
	}
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
