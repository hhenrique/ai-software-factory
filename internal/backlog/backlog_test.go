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
