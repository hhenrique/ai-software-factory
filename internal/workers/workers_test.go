package workers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"factory/internal/eventlog"
)

// requirePool connects to the projection store, skipping if it's not
// reachable — same pattern as internal/repositories' requirePool.
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

func uniqueName(t *testing.T) string {
	t.Helper()
	return "test-worker-" + time.Now().Format(time.RFC3339Nano)
}

func TestCreateThenGetRoundTripsParams(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	name := uniqueName(t)

	created, err := Create(ctx, pool, name, "claude-code", "sonnet", map[string]string{"effort": "high"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == 0 {
		t.Errorf("Create: ID = 0, want a real id")
	}

	got, err := Get(ctx, pool, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != name || got.Harness != "claude-code" || got.Model != "sonnet" {
		t.Errorf("Get = %+v, unexpected fields", got)
	}
	if got.Params["effort"] != "high" {
		t.Errorf("Get.Params = %+v, want effort=high", got.Params)
	}
}

func TestCreateWithNilParams(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()

	created, err := Create(ctx, pool, uniqueName(t), "codex", "chatgpt-sol", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Params == nil || len(created.Params) != 0 {
		t.Errorf("Create with nil params: Params = %+v, want empty non-nil map", created.Params)
	}
}

func TestGetUnknownIDReturnsErrNotFound(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()

	_, err := Get(ctx, pool, -1)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get: err = %v, want ErrNotFound", err)
	}
}

func TestCreateDuplicateNameErrors(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	name := uniqueName(t)

	if _, err := Create(ctx, pool, name, "claude-code", "sonnet", nil); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := Create(ctx, pool, name, "claude-code", "sonnet", nil); err == nil {
		t.Fatalf("expected an error creating a duplicate name")
	}
}

func TestListIncludesCreatedWorker(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	name := uniqueName(t)

	if _, err := Create(ctx, pool, name, "claude-code", "sonnet", nil); err != nil {
		t.Fatalf("Create: %v", err)
	}

	all, err := List(ctx, pool)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, w := range all {
		if w.Name == name {
			found = true
		}
	}
	if !found {
		t.Errorf("List did not include created worker %q", name)
	}
}

func TestUpdateChangesFields(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	name := uniqueName(t)

	created, err := Create(ctx, pool, name, "claude-code", "sonnet", map[string]string{"effort": "medium"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	newName := name + "-renamed"
	updated, err := Update(ctx, pool, created.ID, newName, "claude-code", "opus", map[string]string{"effort": "high"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != newName || updated.Model != "opus" || updated.Params["effort"] != "high" {
		t.Errorf("Update = %+v, unexpected fields", updated)
	}
}

func TestUpdateUnknownIDReturnsErrNotFound(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()

	_, err := Update(ctx, pool, -1, "x", "y", "z", nil)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update: err = %v, want ErrNotFound", err)
	}
}

func TestDeleteThenGetReturnsErrNotFound(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()

	created, err := Create(ctx, pool, uniqueName(t), "claude-code", "sonnet", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := Delete(ctx, pool, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := Get(ctx, pool, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete: err = %v, want ErrNotFound", err)
	}
}

func TestDeleteUnknownIDReturnsErrNotFound(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()

	if err := Delete(ctx, pool, -1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete: err = %v, want ErrNotFound", err)
	}
}

// TestDeleteInUseReturnsErrInUse inserts a role_assignments row directly
// (rather than depending on internal/roleassignment, which would make
// this a cross-package test for a same-package concern: workers.Delete's
// own foreign-key-violation handling) to prove a Worker still assigned
// to a role can't be silently deleted.
func TestDeleteInUseReturnsErrInUse(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()

	created, err := Create(ctx, pool, uniqueName(t), "claude-code", "sonnet", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	workflow := "test-workflow-" + time.Now().Format(time.RFC3339Nano)
	if _, err := pool.Exec(ctx, `
		INSERT INTO role_assignments (workflow, role, worker_id) VALUES ($1, 'coder', $2)
	`, workflow, created.ID); err != nil {
		t.Fatalf("insert role_assignments: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM role_assignments WHERE workflow = $1`, workflow)
	})

	if err := Delete(ctx, pool, created.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("Delete: err = %v, want ErrInUse", err)
	}
}
