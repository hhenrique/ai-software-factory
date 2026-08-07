package repoconfig

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"factory/internal/eventlog"
	"factory/internal/repositories"
	"factory/internal/settings"
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

func uniqueRepoName(t *testing.T) string {
	t.Helper()
	return "test-repo-" + time.Now().Format(time.RFC3339Nano)
}

// withGlobalRoot sets the FactoryRootKey setting for the duration of a
// test, restoring whatever was there before — including truly restoring
// "unset" via a direct SQL delete (test-internal, not internal/settings'
// public API, which deliberately has no Delete) when there was no prior
// value. Getting this wrong leaves a permanent, wrong factory_root row in
// a shared dev database after the test run, not just a failed assertion —
// found by checking the actual table after running these tests once.
func withGlobalRoot(t *testing.T, pool *pgxpool.Pool, value string) {
	t.Helper()
	ctx := context.Background()
	prevValue, hadPrev, err := settings.Get(ctx, pool, FactoryRootKey)
	if err != nil {
		t.Fatalf("settings.Get: %v", err)
	}
	if _, err := settings.Set(ctx, pool, FactoryRootKey, value); err != nil {
		t.Fatalf("settings.Set: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		if hadPrev {
			settings.Set(ctx, pool, FactoryRootKey, prevValue)
			return
		}
		pool.Exec(ctx, `DELETE FROM settings WHERE key = $1`, FactoryRootKey)
	})
}

// clearGlobalRoot removes the FactoryRootKey setting for the duration of
// a test (internal/settings has no real Delete, so this is a direct SQL
// statement — test-internal, not a public API a caller should rely on),
// restoring whatever was there before.
func clearGlobalRoot(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	prevValue, hadPrev, err := settings.Get(ctx, pool, FactoryRootKey)
	if err != nil {
		t.Fatalf("settings.Get: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM settings WHERE key = $1`, FactoryRootKey); err != nil {
		t.Fatalf("clear factory_root: %v", err)
	}
	t.Cleanup(func() {
		if hadPrev {
			settings.Set(context.Background(), pool, FactoryRootKey, prevValue)
		}
	})
}

func TestDBProviderUsesGlobalSettingWhenNoRepoOverride(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	withGlobalRoot(t, pool, "/data/factory")

	repoName := uniqueRepoName(t)
	if _, err := repositories.Insert(ctx, pool, repoName, "https://github.com/a/b.git", "", "", ""); err != nil {
		t.Fatalf("repositories.Insert: %v", err)
	}

	p := DBProvider{Pool: pool}
	got, err := p.Paths(ctx, repoName, "run-123")
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	want := Paths{
		CloneDir:    "/data/factory/repos/" + repoName + ".git",
		WorktreeDir: "/data/factory/worktrees/" + repoName + "/run-123",
	}
	if got != want {
		t.Errorf("Paths() = %+v, want %+v", got, want)
	}
}

func TestDBProviderRepoOverrideWinsOverGlobal(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	withGlobalRoot(t, pool, "/data/global")

	repoName := uniqueRepoName(t)
	if _, err := repositories.Insert(ctx, pool, repoName, "https://github.com/a/b.git", "", "", "/data/repo-specific"); err != nil {
		t.Fatalf("repositories.Insert: %v", err)
	}

	p := DBProvider{Pool: pool}
	got, err := p.Paths(ctx, repoName, "run-123")
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	want := Paths{
		CloneDir:    "/data/repo-specific/repos/" + repoName + ".git",
		WorktreeDir: "/data/repo-specific/worktrees/" + repoName + "/run-123",
	}
	if got != want {
		t.Errorf("Paths() = %+v, want %+v (repo override should win over the global setting)", got, want)
	}
}

func TestDBProviderFailsLoudWhenNeitherConfigured(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	clearGlobalRoot(t, pool)

	repoName := uniqueRepoName(t)
	if _, err := repositories.Insert(ctx, pool, repoName, "https://github.com/a/b.git", "", "", ""); err != nil {
		t.Fatalf("repositories.Insert: %v", err)
	}

	p := DBProvider{Pool: pool}
	_, err := p.Paths(ctx, repoName, "run-123")
	if err == nil {
		t.Fatalf("Paths: expected an error when neither a repo override nor the global setting is configured")
	}
	if !strings.Contains(err.Error(), repoName) || !strings.Contains(err.Error(), FactoryRootKey) {
		t.Errorf("Paths error = %q, want it to name both the repo (%q) and the setting key (%q)", err.Error(), repoName, FactoryRootKey)
	}
}

func TestDBProviderUnregisteredRepoStillUsesGlobal(t *testing.T) {
	// A repo with no repositories row at all (not every caller through
	// history has necessarily registered one first) must still resolve
	// via the global setting, not error out on the missing row itself —
	// DBProvider.resolveRoot treats pgx.ErrNoRows as "no override," not a
	// failure.
	pool := requirePool(t)
	ctx := context.Background()
	withGlobalRoot(t, pool, "/data/factory")

	p := DBProvider{Pool: pool}
	got, err := p.Paths(ctx, "never-registered-"+time.Now().Format(time.RFC3339Nano), "run-1")
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	if got.CloneDir == "" {
		t.Errorf("Paths() = %+v, want a resolved path via the global setting", got)
	}
}

func TestDBProviderDifferentRunsGetDifferentWorktreeDirs(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	withGlobalRoot(t, pool, "/data/factory")

	p := DBProvider{Pool: pool}
	repoName := uniqueRepoName(t)
	run1, err := p.Paths(ctx, repoName, "run-1")
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	run2, err := p.Paths(ctx, repoName, "run-2")
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}

	if run1.WorktreeDir == run2.WorktreeDir {
		t.Errorf("expected distinct WorktreeDir per run, got %q for both", run1.WorktreeDir)
	}
	if run1.CloneDir != run2.CloneDir {
		t.Errorf("expected the same shared CloneDir across runs of the same repo, got %q vs %q", run1.CloneDir, run2.CloneDir)
	}
}
