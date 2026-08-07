// Package repoconfig resolves where a repo's persistent clone and a Run's
// ephemeral worktree live on disk. DBProvider is the only implementation:
// a global default (internal/settings key "factory_root") with an
// optional per-repo override (repositories.worktree_root) — the two
// dimensions docs/07's glossary calls "where," made control-plane-
// editable instead of a FACTORY_ROOT env var nobody could change without
// a redeploy. Every call site goes through the Provider interface so a
// future implementation (if one's ever needed) stays a single swap, never
// a call-site rewrite.
package repoconfig

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"factory/internal/settings"
)

// FactoryRootKey is the internal/settings key DBProvider reads for the
// global default worktree root.
const FactoryRootKey = "factory_root"

// Paths is where a given repo's persistent clone and one Run's isolated
// worktree live on disk.
type Paths struct {
	// CloneDir is the repo's canonical bare clone, fetched before each
	// worktree add. Shared across every Run against this repo.
	CloneDir string

	// WorktreeDir is this Run's isolated `git worktree add` checkout.
	// Keyed by Run id, not Task id: retries are new Runs, never mutated
	// (docs/01), so each attempt gets its own directory.
	WorktreeDir string
}

// Provider resolves Paths for a repo + Run. Returns an error rather than
// silently falling back to a guessed path — a wrong-but-present default
// is exactly what caused real Run failures (a read-only default root,
// undiscovered until a Run failed deep inside a mkdir); an unconfigured
// root should fail loud and early instead.
type Provider interface {
	// Paths returns where repo's clone and runID's worktree should live.
	// repo is the Repository identifier (docs/04); runID is the Run's id.
	Paths(ctx context.Context, repo, runID string) (Paths, error)
}

// DBProvider resolves a root in two steps: repositories.worktree_root for
// this specific repo if it's set (non-empty), else the global
// FactoryRootKey setting. Errors loudly, naming both places to configure
// it, if neither is set — see the package doc comment.
type DBProvider struct {
	Pool *pgxpool.Pool
}

func (p DBProvider) Paths(ctx context.Context, repo, runID string) (Paths, error) {
	root, err := p.resolveRoot(ctx, repo)
	if err != nil {
		return Paths{}, err
	}
	return Paths{
		CloneDir:    filepath.Join(root, "repos", repo+".git"),
		WorktreeDir: filepath.Join(root, "worktrees", repo, runID),
	}, nil
}

func (p DBProvider) resolveRoot(ctx context.Context, repo string) (string, error) {
	var override string
	err := p.Pool.QueryRow(ctx, `SELECT worktree_root FROM repositories WHERE name = $1`, repo).Scan(&override)
	if err != nil && err != pgx.ErrNoRows {
		return "", fmt.Errorf("repoconfig: look up worktree_root for %q: %w", repo, err)
	}
	if override != "" {
		return override, nil
	}

	global, ok, err := settings.Get(ctx, p.Pool, FactoryRootKey)
	if err != nil {
		return "", fmt.Errorf("repoconfig: look up global %q setting: %w", FactoryRootKey, err)
	}
	if ok && global != "" {
		return global, nil
	}

	return "", fmt.Errorf(
		"repoconfig: no worktree root configured for repo %q — set a per-repo override in the Repositories view, "+
			"or a global default in the Settings view (%q)", repo, FactoryRootKey)
}

// ValidateRoot checks that root is actually usable — creatable and
// genuinely writable by this process — not just a non-empty string.
// Meant to be called when a human sets the value (the Settings view's
// factory_root, or a repository's worktree_root override), not on every
// Run: catching a bad path here moves the earlier failure mode (a
// wrong-but-accepted root, undiscovered until a Run failed deep inside a
// mkdir) from Run time to configuration time, which is the whole reason
// this validation exists.
//
// os.MkdirAll alone is not enough: it succeeds as a no-op on a directory
// that already exists, even one this process cannot write into (e.g. a
// system directory owned by another user) — exactly the class of path a
// human might paste in by mistake. The write probe below is what
// actually catches that case; MkdirAll only rules out "does not exist and
// cannot be created."
//
// Assumes this process and the worker process that will actually use the
// path share the same filesystem/user (doc05: self-hosted on one VM) —
// this validates what the control plane can see, not what the worker
// process specifically can.
func ValidateRoot(root string) error {
	if root == "" {
		return fmt.Errorf("repoconfig: root must not be empty")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("repoconfig: %q is not usable: %w", root, err)
	}

	probe := filepath.Join(root, ".factory-write-test")
	f, err := os.Create(probe)
	if err != nil {
		return fmt.Errorf("repoconfig: %q exists but is not writable: %w", root, err)
	}
	f.Close()
	os.Remove(probe)
	return nil
}
