package roleassignment

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"factory/internal/eventlog"
	"factory/internal/workers"
)

// requirePool connects to the projection store, skipping if it's not
// reachable — same pattern as internal/repositories'/internal/workers'
// requirePool.
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

func uniqueWorkflow(t *testing.T) string {
	t.Helper()
	return "test-workflow-" + time.Now().Format(time.RFC3339Nano)
}

// createWorker registers a best-effort delete of the created worker once
// the test ends. Every call site in this file that later Sets an
// assignment for it must also call cleanupAssignment right after that
// Set — registered later, so t.Cleanup's LIFO order deletes the
// role_assignments row (this worker's Set-created foreign-key reference)
// before this cleanup tries to delete the worker itself; the reverse
// order would hit workers.ErrInUse and leak the worker.
func createWorker(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	w, err := workers.Create(context.Background(), pool, "test-worker-"+time.Now().Format(time.RFC3339Nano), "claude-code", "sonnet", nil)
	if err != nil {
		t.Fatalf("workers.Create: %v", err)
	}
	t.Cleanup(func() {
		workers.Delete(context.Background(), pool, w.ID)
	})
	return w.ID
}

// cleanupAssignment deletes a Set-created role_assignments row once the
// test ends — see createWorker's doc comment on why call order matters.
func cleanupAssignment(t *testing.T, pool *pgxpool.Pool, workflow, role string) {
	t.Helper()
	t.Cleanup(func() {
		Delete(context.Background(), pool, workflow, role)
	})
}

func TestSetThenListIncludesAssignment(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	workflow := uniqueWorkflow(t)
	workerID := createWorker(t, pool)

	if _, err := Set(ctx, pool, workflow, "coder", workerID); err != nil {
		t.Fatalf("Set: %v", err)
	}
	cleanupAssignment(t, pool, workflow, "coder")

	all, err := List(ctx, pool)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, a := range all {
		if a.Workflow == workflow && a.Role == "coder" && a.WorkerID == workerID {
			found = true
		}
	}
	if !found {
		t.Errorf("List did not include Set assignment for %q/coder", workflow)
	}
}

func TestSetIsUpsert(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	workflow := uniqueWorkflow(t)
	first := createWorker(t, pool)
	second := createWorker(t, pool)

	if _, err := Set(ctx, pool, workflow, "coder", first); err != nil {
		t.Fatalf("first Set: %v", err)
	}
	if _, err := Set(ctx, pool, workflow, "coder", second); err != nil {
		t.Fatalf("second Set: %v", err)
	}
	cleanupAssignment(t, pool, workflow, "coder")

	resolved, err := Resolve(ctx, pool, workflow, []string{"coder"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	gotWorker, err := workers.Get(ctx, pool, second)
	if err != nil {
		t.Fatalf("workers.Get: %v", err)
	}
	if resolved["coder"].Model != gotWorker.Model {
		t.Errorf("Resolve after re-Set = %+v, want the second worker's config", resolved["coder"])
	}
}

func TestSetUnknownRoleReturnsErrUnknownRole(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	workerID := createWorker(t, pool)

	_, err := Set(ctx, pool, uniqueWorkflow(t), "nonexistent_role", workerID)
	if !errors.Is(err, ErrUnknownRole) {
		t.Fatalf("Set: err = %v, want ErrUnknownRole", err)
	}
}

func TestSetUnknownWorkerReturnsErrUnknownWorker(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()

	_, err := Set(ctx, pool, uniqueWorkflow(t), "coder", -1)
	if !errors.Is(err, ErrUnknownWorker) {
		t.Fatalf("Set: err = %v, want ErrUnknownWorker", err)
	}
}

func TestDeleteThenNotInList(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	workflow := uniqueWorkflow(t)
	workerID := createWorker(t, pool)

	if _, err := Set(ctx, pool, workflow, "coder", workerID); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := Delete(ctx, pool, workflow, "coder"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	all, err := List(ctx, pool)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, a := range all {
		if a.Workflow == workflow && a.Role == "coder" {
			t.Errorf("List still includes deleted assignment %q/coder", workflow)
		}
	}
}

func TestDeleteUnknownReturnsErrNotFound(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()

	if err := Delete(ctx, pool, uniqueWorkflow(t), "coder"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete: err = %v, want ErrNotFound", err)
	}
}

func TestResolveReturnsHarnessModelParams(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	workflow := uniqueWorkflow(t)

	w, err := workers.Create(ctx, pool, "test-worker-"+time.Now().Format(time.RFC3339Nano), "claude-code", "sonnet", map[string]string{"effort": "high"})
	if err != nil {
		t.Fatalf("workers.Create: %v", err)
	}
	t.Cleanup(func() { workers.Delete(context.Background(), pool, w.ID) })
	if _, err := Set(ctx, pool, workflow, "coder", w.ID); err != nil {
		t.Fatalf("Set: %v", err)
	}
	cleanupAssignment(t, pool, workflow, "coder")

	resolved, err := Resolve(ctx, pool, workflow, []string{"coder"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got := resolved["coder"]
	if got.Harness != "claude-code" || got.Model != "sonnet" || got.Params["effort"] != "high" {
		t.Errorf("Resolve[coder] = %+v, unexpected", got)
	}
}

func TestResolveMissingAssignmentListsEveryMissingRole(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	workflow := uniqueWorkflow(t)
	workerID := createWorker(t, pool)

	// Only "coder" gets an assignment; "planner" and "reviewer" don't.
	if _, err := Set(ctx, pool, workflow, "coder", workerID); err != nil {
		t.Fatalf("Set: %v", err)
	}
	cleanupAssignment(t, pool, workflow, "coder")

	_, err := Resolve(ctx, pool, workflow, []string{"planner", "coder", "reviewer"})
	if err == nil {
		t.Fatalf("Resolve: expected an error for unassigned roles, got nil")
	}
	if !strings.Contains(err.Error(), "planner") || !strings.Contains(err.Error(), "reviewer") {
		t.Errorf("Resolve error = %q, want it to name both missing roles", err.Error())
	}
	if strings.Contains(err.Error(), "\"coder\"") {
		t.Errorf("Resolve error = %q, should not list coder (it has an assignment)", err.Error())
	}
}

func TestResolveEmptyRoleNames(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()

	resolved, err := Resolve(ctx, pool, uniqueWorkflow(t), nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(resolved) != 0 {
		t.Errorf("Resolve with no roleNames = %+v, want empty", resolved)
	}
}
