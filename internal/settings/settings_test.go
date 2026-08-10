package settings

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"factory/internal/eventlog"
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

// uniqueKey also registers a best-effort cleanup that deletes any row
// this key ends up backing (a no-op if Set was never called for it) —
// every test in this file must go through this, not a bare timestamp
// string, so the settings table never accumulates "test-key-..." rows
// from routine `go test` runs.
func uniqueKey(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	key := "test-key-" + time.Now().Format(time.RFC3339Nano)
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM settings WHERE key = $1`, key)
	})
	return key
}

func TestGetUnsetKeyReturnsNotOK(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()

	_, ok, err := Get(ctx, pool, uniqueKey(t, pool))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Errorf("Get on an unset key: ok = true, want false")
	}
}

func TestSetThenGet(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	key := uniqueKey(t, pool)

	if _, err := Set(ctx, pool, key, "/data/factory"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	value, ok, err := Get(ctx, pool, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatalf("Get after Set: ok = false, want true")
	}
	if value != "/data/factory" {
		t.Errorf("value = %q, want /data/factory", value)
	}
}

func TestSetIsUpsert(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	key := uniqueKey(t, pool)

	if _, err := Set(ctx, pool, key, "first"); err != nil {
		t.Fatalf("first Set: %v", err)
	}
	if _, err := Set(ctx, pool, key, "second"); err != nil {
		t.Fatalf("second Set: %v", err)
	}

	value, ok, err := Get(ctx, pool, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok || value != "second" {
		t.Errorf("Get after re-Set = (%q, %v), want (second, true)", value, ok)
	}
}

func TestListIncludesSetKey(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	key := uniqueKey(t, pool)

	if _, err := Set(ctx, pool, key, "value"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	all, err := List(ctx, pool)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, s := range all {
		if s.Key == key && s.Value == "value" {
			found = true
		}
	}
	if !found {
		t.Errorf("List did not include %q=value", key)
	}
}
