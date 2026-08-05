// Package pr holds the real pr.create_and_link Activity — MERGING is
// fully tool-owned (doc 01): push the Run's branch and open a PR. No
// agent involvement.
package pr

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"factory/internal/conductor"
)

// Activities holds the dependencies the real pr.create_and_link Activity
// needs (none, today — kept as a struct for symmetry with the other real
// Activity sets so cmd/worker registers every one the same way). It
// shells out to the gh CLI rather than calling the GitHub API directly:
// gh already owns auth/credential handling (doc 03's "outbound API call"
// case), and re-implementing that is exactly the kind of hand-rolled
// infrastructure CLAUDE.md's tools-before-agents rule (Rule 2, applied to
// tooling in general) argues against building when a well-tested tool
// already does it.
type Activities struct{}

// Registrations maps the Activity name the conductor package's
// step-to-Activity mapping expects (see internal/conductor/registry.go)
// to this struct's method.
func (a *Activities) Registrations() map[string]any {
	return map[string]any{
		"pr.create_and_link": a.CreateAndLink,
	}
}

// CreateAndLink pushes the Run's worktree branch and opens a PR for it.
// "linking to the source Task" (doc 01's MERGING description) is not yet
// possible — there's no persisted Task entity to link to (doc 04's Work
// section is unbuilt) — so this only does the push+PR half for now.
//
// worktree_path and branch come from in.Context (provision/worktree.create's
// Produced output), the same way run.tests_lint_build gets worktree_path:
// pr.create_and_link is type: tool, so it receives the full accumulated
// Run context rather than a pruned `context:` subset (see
// ActivityInput.Context's doc comment).
func (a *Activities) CreateAndLink(ctx context.Context, in conductor.ActivityInput) (conductor.ActivityOutput, error) {
	worktreePath, _ := in.Context["worktree_path"].(string)
	if worktreePath == "" {
		return conductor.ActivityOutput{}, fmt.Errorf("pr: create_and_link: worktree_path missing from context")
	}
	branch, _ := in.Context["branch"].(string)
	if branch == "" {
		return conductor.ActivityOutput{}, fmt.Errorf("pr: create_and_link: branch missing from context")
	}

	if err := pushBranch(ctx, worktreePath, branch); err != nil {
		return conductor.ActivityOutput{}, fmt.Errorf("pr: create_and_link: %w", err)
	}

	prURL, err := createOrFindPR(ctx, worktreePath, branch, in.RunID)
	if err != nil {
		return conductor.ActivityOutput{}, fmt.Errorf("pr: create_and_link: %w", err)
	}

	return conductor.ActivityOutput{
		Produced: map[string]any{"pr_url": prURL},
	}, nil
}

// pushBranch force-pushes (with lease) rather than a plain fast-forward
// push. factory/<run-id> branches are entirely factory-owned by
// convention (see gitops.BranchName) — nothing else should ever commit to
// one — but worktree.create's `-B` semantics reset the branch to its base
// on every invocation (deliberately, for idempotency under Activity
// retry: see gitops.WorktreeCreate's doc comment), so a retried Run's
// branch routinely diverges from whatever it last pushed. A plain push
// is fast-forward-only and would reject that as a real conflict.
// --force-with-lease (not bare --force) still refuses to clobber a
// remote that changed for a reason this push doesn't know about — safe
// here since nothing but this Activity is expected to touch the branch,
// but cheap insurance if that assumption is ever wrong.
func pushBranch(ctx context.Context, worktreePath, branch string) error {
	return run(ctx, worktreePath, "git", "push", "--force-with-lease", "-u", "origin", branch)
}

// createOrFindPR opens a PR for branch, or — if one already exists (an
// at-least-once Activity redelivery for the same Run) — returns the
// existing PR's URL instead of failing. gh reports "already exists" on
// stderr in that case; there's no structured exit code to key off, so
// this matches on the message text.
func createOrFindPR(ctx context.Context, worktreePath, branch, runID string) (string, error) {
	title := fmt.Sprintf("factory: automated change (run %s)", runID)
	body := fmt.Sprintf("Opened automatically by the factory conductor for Run %s. Safe to close if this is a test.", runID)

	out, err := output(ctx, worktreePath, "gh", "pr", "create", "--title", title, "--body", body, "--head", branch)
	if err == nil {
		return strings.TrimSpace(out), nil
	}
	if !strings.Contains(err.Error(), "already exists") {
		return "", err
	}

	out, viewErr := output(ctx, worktreePath, "gh", "pr", "view", branch, "--json", "url", "-q", ".url")
	if viewErr != nil {
		return "", fmt.Errorf("pr already exists but could not resolve its URL: %w", viewErr)
	}
	return strings.TrimSpace(out), nil
}

func run(ctx context.Context, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func output(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
