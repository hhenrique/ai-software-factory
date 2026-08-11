package harness

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"factory/internal/activities/cmderr"
)

// commitWorktreeChanges stages and commits whatever the harness changed
// in the worktree, returning the diff — or "" if it made no changes.
// Doc 03's implicit-patch convention (a schema-less agent step's output
// resolves to a diff regardless of the harness's internal edit
// representation) is implemented here rather than by parsing anything out
// of the CLI's text output: these harnesses edit files directly, so the
// conductor computes the diff itself the same deterministic way for every
// harness, exactly mirroring internal/activities/stub's placeholder
// version of this same step now that a real harness produces the changes
// instead of a canned line.
func commitWorktreeChanges(ctx context.Context, worktreePath, stepID string, attempt int) (string, error) {
	if err := runGit(ctx, worktreePath, "add", "-A"); err != nil {
		return "", err
	}

	diffCmd := exec.CommandContext(ctx, "git", "diff", "--cached")
	diffCmd.Dir = worktreePath
	diffOut, err := diffCmd.Output()
	if err != nil {
		return "", cmderr.Wrap("git diff --cached", err, cmderr.Stderr(err))
	}
	if len(diffOut) == 0 {
		return "", nil
	}

	if err := runGit(ctx, worktreePath, "-c", "user.email=factory-harness@example.com", "-c", "user.name=factory-harness",
		"commit", "-q", "-m", fmt.Sprintf("harness: change from %s (attempt %d)", stepID, attempt)); err != nil {
		return "", err
	}
	return string(diffOut), nil
}

// enforceReadOnlyWorktree is the deterministic backstop behind a
// Planner-role call's harness-level read-only flag (doc03: necessary,
// not sufficient — a harness's own claim of read-only isn't verified at
// the source). Reports whether the worktree was dirty despite the
// invocation being told not to touch it; if so, resets it (never lets a
// stray edit leak into what a later Coder-role call against the same
// worktree sees) rather than either silently discarding the violation or
// failing the whole Run over it.
func enforceReadOnlyWorktree(ctx context.Context, worktreePath string) (violated bool, err error) {
	statusCmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	statusCmd.Dir = worktreePath
	out, err := statusCmd.Output()
	if err != nil {
		return false, cmderr.Wrap("git status --porcelain", err, cmderr.Stderr(err))
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return false, nil
	}

	if err := runGit(ctx, worktreePath, "checkout", "--", "."); err != nil {
		return false, err
	}
	if err := runGit(ctx, worktreePath, "clean", "-fd"); err != nil {
		return false, err
	}
	return true, nil
}

func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return cmderr.Wrap("git "+strings.Join(args, " "), err, string(out))
	}
	return nil
}
