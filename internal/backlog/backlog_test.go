package backlog

import (
	"context"
	"testing"
	"time"

	"factory/internal/conductor"
	"factory/internal/eventlog"
)

// requirePool connects to the projection store, skipping if it's not
// reachable — same spirit as eventlog's requirePool: this package's real
// behavior needs a real Postgres (deploy/docker-compose.yaml).
func requirePool(t *testing.T) *Activities {
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
	return &Activities{Pool: pool}
}

func TestCreateTaskInsertsRowAndReturnsID(t *testing.T) {
	a := requirePool(t)
	ctx := context.Background()

	runID := "test-run-" + time.Now().Format(time.RFC3339Nano)
	out, err := a.CreateTask(ctx, conductor.ActivityInput{
		RunID: runID,
		Context: map[string]any{
			"source":           "review-finding",
			"task_description": "extract this into a method",
		},
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	taskID, _ := out.Produced["spawned_task_id"].(string)
	if taskID == "" {
		t.Fatalf("expected spawned_task_id in Produced, got %+v", out.Produced)
	}

	var gotRunID, gotSource, gotStatus string
	err = a.Pool.QueryRow(ctx,
		`SELECT run_id, source, status FROM backlog_tasks WHERE task_id = $1`, taskID,
	).Scan(&gotRunID, &gotSource, &gotStatus)
	if err != nil {
		t.Fatalf("query back: %v", err)
	}
	if gotRunID != runID {
		t.Errorf("run_id = %q, want %q", gotRunID, runID)
	}
	if gotSource != "review-finding" {
		t.Errorf("source = %q, want review-finding", gotSource)
	}
	if gotStatus != "QUEUED" {
		t.Errorf("status = %q, want QUEUED", gotStatus)
	}
}

func TestCreateTaskDefaultsSourceWhenMissing(t *testing.T) {
	a := requirePool(t)
	ctx := context.Background()

	out, err := a.CreateTask(ctx, conductor.ActivityInput{
		RunID:   "test-run-no-source-" + time.Now().Format(time.RFC3339Nano),
		Context: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	taskID, _ := out.Produced["spawned_task_id"].(string)

	var gotSource string
	if err := a.Pool.QueryRow(ctx, `SELECT source FROM backlog_tasks WHERE task_id = $1`, taskID).Scan(&gotSource); err != nil {
		t.Fatalf("query back: %v", err)
	}
	if gotSource != "unknown" {
		t.Errorf("source = %q, want unknown", gotSource)
	}
}

func TestInsertHumanTaskThenAttachRun(t *testing.T) {
	a := requirePool(t)
	ctx := context.Background()

	taskID, err := InsertHumanTask(ctx, a.Pool, "hhenrique/toy-repo", "issue-to-pr-claude-only", "fix the thing")
	if err != nil {
		t.Fatalf("InsertHumanTask: %v", err)
	}
	if taskID == "" {
		t.Fatalf("expected a non-empty task id")
	}

	var gotRunID *string
	var gotRepo, gotWorkflow, gotSource, gotStatus string
	err = a.Pool.QueryRow(ctx,
		`SELECT run_id, target_repo, workflow, source, status FROM backlog_tasks WHERE task_id = $1`, taskID,
	).Scan(&gotRunID, &gotRepo, &gotWorkflow, &gotSource, &gotStatus)
	if err != nil {
		t.Fatalf("query back: %v", err)
	}
	if gotRunID != nil {
		t.Errorf("run_id = %v, want NULL before AttachRun", *gotRunID)
	}
	if gotRepo != "hhenrique/toy-repo" {
		t.Errorf("target_repo = %q, want hhenrique/toy-repo", gotRepo)
	}
	if gotWorkflow != "issue-to-pr-claude-only" {
		t.Errorf("workflow = %q, want issue-to-pr-claude-only", gotWorkflow)
	}
	if gotSource != "human" {
		t.Errorf("source = %q, want human", gotSource)
	}
	if gotStatus != "QUEUED" {
		t.Errorf("status = %q, want QUEUED", gotStatus)
	}

	runID := "test-run-" + time.Now().Format(time.RFC3339Nano)
	if err := AttachRun(ctx, a.Pool, taskID, runID); err != nil {
		t.Fatalf("AttachRun: %v", err)
	}

	err = a.Pool.QueryRow(ctx,
		`SELECT run_id, status FROM backlog_tasks WHERE task_id = $1`, taskID,
	).Scan(&gotRunID, &gotStatus)
	if err != nil {
		t.Fatalf("query back after AttachRun: %v", err)
	}
	if gotRunID == nil || *gotRunID != runID {
		t.Errorf("run_id after AttachRun = %v, want %q", gotRunID, runID)
	}
	if gotStatus != "RUNNING" {
		t.Errorf("status after AttachRun = %q, want RUNNING", gotStatus)
	}
}

func TestAttachRunUnknownTaskIDErrors(t *testing.T) {
	a := requirePool(t)
	ctx := context.Background()

	err := AttachRun(ctx, a.Pool, "does-not-exist-"+time.Now().Format(time.RFC3339Nano), "some-run")
	if err == nil {
		t.Fatalf("expected an error for an unknown task_id")
	}
}

func TestListIncludesInsertedHumanTaskWithEmptyRunIDBeforeAttach(t *testing.T) {
	a := requirePool(t)
	ctx := context.Background()

	taskID, err := InsertHumanTask(ctx, a.Pool, "hhenrique/toy-repo", "issue-to-pr-claude-only", "list test "+time.Now().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("InsertHumanTask: %v", err)
	}

	tasks, err := List(ctx, a.Pool)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var found *Task
	for i := range tasks {
		if tasks[i].TaskID == taskID {
			found = &tasks[i]
		}
	}
	if found == nil {
		t.Fatalf("List did not include inserted task %q", taskID)
	}
	if found.RunID != "" {
		t.Errorf("RunID = %q, want empty before AttachRun", found.RunID)
	}
	if found.TargetRepo != "hhenrique/toy-repo" || found.Workflow != "issue-to-pr-claude-only" {
		t.Errorf("unexpected fields: %+v", found)
	}
	if found.Status != "QUEUED" {
		t.Errorf("Status = %q, want QUEUED", found.Status)
	}
}
