// Package repoconfig resolves where a repo's persistent clone and a Run's
// ephemeral worktree live on disk. The source of truth today is a single
// global root read from the environment (EnvProvider) — the control plane
// is expected to eventually own this as editable global + per-repo
// config (doc 04's Repositories section), but building that store is a
// much larger effort than this need justifies on its own (a new
// projection-store schema, an API, and something to edit it with, none
// of which exist yet). Every call site goes through the Provider
// interface so that swap is a single implementation change later, never
// a call-site rewrite.
package repoconfig

import (
	"os"
	"path/filepath"
)

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

// Provider resolves Paths for a repo + Run.
type Provider interface {
	// Paths returns where repo's clone and runID's worktree should live.
	// repo is the Repository identifier (docs/04); runID is the Run's id.
	Paths(repo, runID string) Paths
}

// EnvProvider is the only Provider implementation today: a single global
// root, read once from FACTORY_ROOT, with fixed repos/ and worktrees/
// subdirectories. There is no per-repo override yet — the control plane
// has nowhere to persist one — but Paths already takes repo as a
// parameter so a future DB-backed Provider can add per-repo overrides
// without any caller needing to change.
type EnvProvider struct {
	root string
}

// NewEnvProvider builds an EnvProvider from FACTORY_ROOT, defaulting to
// /var/lib/factory if unset.
func NewEnvProvider() EnvProvider {
	return EnvProvider{root: envOr("FACTORY_ROOT", "/var/lib/factory")}
}

func (p EnvProvider) Paths(repo, runID string) Paths {
	return Paths{
		CloneDir:    filepath.Join(p.root, "repos", repo+".git"),
		WorktreeDir: filepath.Join(p.root, "worktrees", repo, runID),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
