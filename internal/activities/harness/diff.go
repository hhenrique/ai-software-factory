package harness

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
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
		return "", fmt.Errorf("git diff --cached: %w", err)
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

func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
