// Package gitops holds real git-backed Activities — the first one being
// worktree.create, replacing internal/activities/stub's no-op for repos
// that need actual clones and worktrees.
package gitops

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"factory/internal/conductor"
	"factory/internal/filelock"
	"factory/internal/repoconfig"
)

// Activities holds the dependencies real git-backed Activities need.
type Activities struct {
	Paths repoconfig.Provider
}

// Registrations maps the Activity names the conductor package's
// step-to-Activity mapping expects (see internal/conductor/registry.go)
// to this struct's methods — mirrors internal/activities/stub's
// Registrations so cmd/worker can register either set the same way.
func (a *Activities) Registrations() map[string]any {
	return map[string]any{
		"worktree.create": a.WorktreeCreate,
	}
}

// WorktreeCreate provisions a Run's isolated git worktree: it initializes
// (on first use) or fetches the repo's shared bare clone, then adds a
// worktree branched off the repo's default branch.
//
// The bare clone's own refs/heads/* is reserved exclusively for the
// branches this function creates (factory/<run-id>, one per Run); origin's
// branches live under refs/remotes/origin/* instead, via an explicitly
// scoped fetch refspec. This is deliberate, not incidental: a first
// attempt mirrored origin's branches directly into refs/heads/* (via
// `git clone --mirror`), and `fetch --prune` — needed so a second Run
// actually sees new upstream commits — silently deleted every earlier
// Run's still-in-flight factory/<run-id> branch on its next invocation,
// because prune treats anything in the mirrored namespace that's absent
// upstream as stale. Keeping the two namespaces disjoint (the standard
// non-bare-clone convention, just applied to a bare clone) means `fetch
// --prune` can never touch our own branches, however many Runs overlap.
//
// The bare clone is shared across every Run against the same repo, so
// access to it is serialized with an flock (internal/filelock) —
// concurrent Runs against the same repo would otherwise race on git's own
// ref/index locks inside that shared clone (CLAUDE.md: "if more than one
// process or thread can reach shared state, add a lock").
//
// The worktree directory itself is disposable, Run-scoped data (keyed by
// Run id, never Task id — retries are new Runs, never mutated ones), so
// on a retry of the same Run id (e.g. an at-least-once Activity
// redelivery) it's simplest and safest to clear and recreate it rather
// than trying to detect and resume a partial prior attempt.
func (a *Activities) WorktreeCreate(ctx context.Context, in conductor.ActivityInput) (conductor.ActivityOutput, error) {
	if in.Repo.CloneURL == "" {
		return conductor.ActivityOutput{}, fmt.Errorf("gitops: worktree.create: Repo.CloneURL is empty")
	}
	if in.RunID == "" {
		return conductor.ActivityOutput{}, fmt.Errorf("gitops: worktree.create: RunID is empty")
	}

	paths := a.Paths.Paths(in.Repo.Name, in.RunID)

	if err := os.MkdirAll(filepath.Dir(paths.CloneDir), 0o755); err != nil {
		return conductor.ActivityOutput{}, fmt.Errorf("gitops: worktree.create: mkdir clone parent: %w", err)
	}

	unlock, err := filelock.Lock(paths.CloneDir + ".lock")
	if err != nil {
		return conductor.ActivityOutput{}, fmt.Errorf("gitops: worktree.create: %w", err)
	}
	defer unlock()

	if _, statErr := os.Stat(paths.CloneDir); os.IsNotExist(statErr) {
		if err := run(ctx, "git", "init", "--bare", paths.CloneDir); err != nil {
			return conductor.ActivityOutput{}, fmt.Errorf("gitops: worktree.create: init: %w", err)
		}
		if err := run(ctx, "git", "--git-dir="+paths.CloneDir, "remote", "add", "origin", in.Repo.CloneURL); err != nil {
			return conductor.ActivityOutput{}, fmt.Errorf("gitops: worktree.create: remote add: %w", err)
		}
		// Scoped on purpose — see the func doc above.
		if err := run(ctx, "git", "--git-dir="+paths.CloneDir, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"); err != nil {
			return conductor.ActivityOutput{}, fmt.Errorf("gitops: worktree.create: configure fetch refspec: %w", err)
		}
	} else if statErr != nil {
		return conductor.ActivityOutput{}, fmt.Errorf("gitops: worktree.create: stat clone dir: %w", statErr)
	}

	if err := run(ctx, "git", "--git-dir="+paths.CloneDir, "fetch", "--prune", "origin"); err != nil {
		return conductor.ActivityOutput{}, fmt.Errorf("gitops: worktree.create: fetch: %w", err)
	}

	defaultBranch := in.Repo.DefaultBranch
	if defaultBranch == "" {
		// Asks the remote directly what its HEAD points to — the same
		// thing `git clone` itself does internally to pick a branch —
		// rather than relying on any local ref bookkeeping.
		out, err := output(ctx, "git", "ls-remote", "--symref", in.Repo.CloneURL, "HEAD")
		if err != nil {
			return conductor.ActivityOutput{}, fmt.Errorf("gitops: worktree.create: resolve default branch: %w", err)
		}
		defaultBranch, err = parseSymrefHEAD(out)
		if err != nil {
			return conductor.ActivityOutput{}, fmt.Errorf("gitops: worktree.create: %w", err)
		}
	}

	branch := BranchName(in.RunID)

	if err := os.RemoveAll(paths.WorktreeDir); err != nil {
		return conductor.ActivityOutput{}, fmt.Errorf("gitops: worktree.create: clear stale worktree dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.WorktreeDir), 0o755); err != nil {
		return conductor.ActivityOutput{}, fmt.Errorf("gitops: worktree.create: mkdir worktree parent: %w", err)
	}
	if err := run(ctx, "git", "--git-dir="+paths.CloneDir, "worktree", "prune"); err != nil {
		return conductor.ActivityOutput{}, fmt.Errorf("gitops: worktree.create: worktree prune: %w", err)
	}
	if err := run(ctx, "git", "--git-dir="+paths.CloneDir, "worktree", "add", "-B", branch, paths.WorktreeDir, "origin/"+defaultBranch); err != nil {
		return conductor.ActivityOutput{}, fmt.Errorf("gitops: worktree.create: worktree add: %w", err)
	}

	return conductor.ActivityOutput{
		Produced: map[string]any{
			"worktree_path": paths.WorktreeDir,
			"branch":        branch,
			"clone_dir":     paths.CloneDir,
		},
	}, nil
}

// BranchName is the branch-naming convention for every Run's worktree:
// factory/<run-id>. Anchored on Run id, not Task id, because nothing in
// this codebase persists a Task entity yet (doc 04's Work section is
// unbuilt) — once it does, this can incorporate the Task id too for
// discoverability without changing the shape of what calls it.
func BranchName(runID string) string {
	return "factory/" + runID
}

// GitHubSlug extracts "owner/repo" from an https://github.com/... clone
// URL, for passing to `gh ... --repo`. Only the https form is handled —
// every caller of this (cmd/smoketest's PR cleanup, cmd/submittask's issue
// fetch) already requires an https clone URL for pr.create_and_link's own
// gh usage, so there's no case where a caller has this URL in some other
// form.
func GitHubSlug(cloneURL string) (string, error) {
	const prefix = "https://github.com/"
	if !strings.HasPrefix(cloneURL, prefix) {
		return "", fmt.Errorf("clone URL must start with %q to derive a GitHub owner/repo slug, got %q", prefix, cloneURL)
	}
	slug := strings.TrimSuffix(strings.TrimPrefix(cloneURL, prefix), ".git")
	if slug == "" || !strings.Contains(slug, "/") {
		return "", fmt.Errorf("clone URL doesn't look like %s<owner>/<repo>[.git], got %q", prefix, cloneURL)
	}
	return slug, nil
}

// parseSymrefHEAD extracts the branch name from `git ls-remote --symref
// <url> HEAD` output, e.g.:
//
//	ref: refs/heads/main	HEAD
//	<sha>	HEAD
func parseSymrefHEAD(out string) (string, error) {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "ref:" {
			continue
		}
		if branch, ok := strings.CutPrefix(fields[1], "refs/heads/"); ok {
			return branch, nil
		}
	}
	return "", fmt.Errorf("could not parse default branch from ls-remote --symref output: %q", out)
}

func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func output(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return string(out), nil
}
