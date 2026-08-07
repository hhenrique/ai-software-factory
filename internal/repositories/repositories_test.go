package repositories

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"factory/internal/eventlog"
)

// requirePool connects to the projection store, skipping if it's not
// reachable — same spirit as internal/backlog's requirePool: this
// package's real behavior needs a real Postgres (deploy/docker-compose.yaml).
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
	return "test-repo-" + time.Now().Format(time.RFC3339Nano)
}

func TestInsertThenGet(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	name := uniqueName(t)

	inserted, err := Insert(ctx, pool, name, "https://github.com/hhenrique/toy-repo.git", "node --check script.js", "workflows/issue-to-pr-claude-only.yaml", "")
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if !inserted.Enabled {
		t.Errorf("Insert: Enabled = false, want true (default)")
	}

	got, err := Get(ctx, pool, name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != name || got.CloneURL != "https://github.com/hhenrique/toy-repo.git" ||
		got.TestCommand != "node --check script.js" || got.DefaultWorkflow != "workflows/issue-to-pr-claude-only.yaml" {
		t.Errorf("Get = %+v, unexpected fields", got)
	}
}

func TestGetUnknownNameReturnsErrNotFound(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()

	_, err := Get(ctx, pool, "does-not-exist-"+time.Now().Format(time.RFC3339Nano))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get: err = %v, want ErrNotFound", err)
	}
}

func TestInsertDuplicateNameErrors(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	name := uniqueName(t)

	if _, err := Insert(ctx, pool, name, "https://github.com/a/b.git", "", "", ""); err != nil {
		t.Fatalf("first Insert: %v", err)
	}
	if _, err := Insert(ctx, pool, name, "https://github.com/a/b.git", "", "", ""); err == nil {
		t.Fatalf("expected an error registering a duplicate name")
	}
}

func TestSetEnabledTogglesAndErrorsOnUnknownName(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	name := uniqueName(t)

	if _, err := Insert(ctx, pool, name, "https://github.com/a/b.git", "", "", ""); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := SetEnabled(ctx, pool, name, false); err != nil {
		t.Fatalf("SetEnabled(false): %v", err)
	}
	got, err := Get(ctx, pool, name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Enabled {
		t.Errorf("Enabled = true after SetEnabled(false)")
	}

	if err := SetEnabled(ctx, pool, "does-not-exist-"+time.Now().Format(time.RFC3339Nano), true); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetEnabled on unknown name: err = %v, want ErrNotFound", err)
	}
}

func TestListIncludesInsertedRepository(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	name := uniqueName(t)

	if _, err := Insert(ctx, pool, name, "https://github.com/a/b.git", "", "", ""); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	all, err := List(ctx, pool)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, r := range all {
		if r.Name == name {
			found = true
		}
	}
	if !found {
		t.Errorf("List did not include inserted repository %q", name)
	}
}

func TestUpdateChangesTestCommandAndWorkflowNotNameOrCloneURL(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	name := uniqueName(t)

	if _, err := Insert(ctx, pool, name, "https://github.com/a/b.git", "old command", "old.yaml", "/old/root"); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	updated, err := Update(ctx, pool, name, "new command", "new.yaml", "/new/root")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.TestCommand != "new command" || updated.DefaultWorkflow != "new.yaml" || updated.WorktreeRoot != "/new/root" {
		t.Errorf("Update = %+v, want new command/new.yaml//new/root", updated)
	}
	if updated.Name != name || updated.CloneURL != "https://github.com/a/b.git" {
		t.Errorf("Update changed name/clone_url: %+v", updated)
	}
}

func TestInsertAndUpdateRoundTripWorktreeRoot(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	name := uniqueName(t)

	inserted, err := Insert(ctx, pool, name, "https://github.com/a/b.git", "", "", "/data/factory")
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if inserted.WorktreeRoot != "/data/factory" {
		t.Errorf("Insert: WorktreeRoot = %q, want /data/factory", inserted.WorktreeRoot)
	}

	got, err := Get(ctx, pool, name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.WorktreeRoot != "/data/factory" {
		t.Errorf("Get: WorktreeRoot = %q, want /data/factory", got.WorktreeRoot)
	}

	updated, err := Update(ctx, pool, name, "", "", "")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.WorktreeRoot != "" {
		t.Errorf("Update: WorktreeRoot = %q, want empty (cleared)", updated.WorktreeRoot)
	}
}

func TestUpdateUnknownNameReturnsErrNotFound(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()

	_, err := Update(ctx, pool, "does-not-exist-"+time.Now().Format(time.RFC3339Nano), "cmd", "wf.yaml", "")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update: err = %v, want ErrNotFound", err)
	}
}

func TestDeleteThenGetReturnsErrNotFound(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	name := uniqueName(t)

	if _, err := Insert(ctx, pool, name, "https://github.com/a/b.git", "", "", ""); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := Delete(ctx, pool, name); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := Get(ctx, pool, name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete: err = %v, want ErrNotFound", err)
	}
}

func TestDeleteUnknownNameReturnsErrNotFound(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()

	err := Delete(ctx, pool, "does-not-exist-"+time.Now().Format(time.RFC3339Nano))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete: err = %v, want ErrNotFound", err)
	}
}
