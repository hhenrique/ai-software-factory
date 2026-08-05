// Package verify holds the real run.tests_lint_build Activity —
// VERIFYING is fully tool-owned (doc 01): run the repo's declared
// build/test/lint command, route on its exit code. No LLM judgment
// involved.
package verify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"factory/internal/conductor"
)

// Activities holds the dependencies the real run.tests_lint_build
// Activity needs (none, today — kept as a struct for symmetry with
// gitops.Activities and so cmd/worker registers every Activity set the
// same way).
type Activities struct{}

// Registrations maps the Activity name the conductor package's
// step-to-Activity mapping expects (see internal/conductor/registry.go)
// to this struct's method.
func (a *Activities) Registrations() map[string]any {
	return map[string]any{
		"run.tests_lint_build": a.RunTestsLintBuild,
	}
}

// RunTestsLintBuild runs the repo's declared TestCommand in the Run's
// worktree and routes on its exit code: 0 → "pass", nonzero → "fail" with
// the command's combined stdout+stderr as failing_tests_diff.
//
// worktree_path comes from in.Context, not a dedicated ActivityInput
// field — it's provision's (worktree.create's) Produced output, and
// run.tests_lint_build (type: tool) receives the full accumulated Run
// context without needing to declare `context: [worktree_path]` (see
// ActivityInput.Context's doc comment).
//
// Doc 01 also calls for a flakiness guard (re-run failed tests in
// isolation once before routing to REVISING) and structured per-test
// parsing (which test failed, compiler error text) — both need
// per-repo/per-framework knowledge this generic exit-code-based runner
// doesn't have, so both stay deferred, same as before this Activity went
// from stub to real.
func (a *Activities) RunTestsLintBuild(ctx context.Context, in conductor.ActivityInput) (conductor.ActivityOutput, error) {
	worktreePath, _ := in.Context["worktree_path"].(string)
	if worktreePath == "" {
		return conductor.ActivityOutput{}, fmt.Errorf("verify: run.tests_lint_build: worktree_path missing from context")
	}
	if in.Repo.TestCommand == "" {
		return conductor.ActivityOutput{}, fmt.Errorf("verify: run.tests_lint_build: Repo.TestCommand is empty")
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", in.Repo.TestCommand)
	cmd.Dir = worktreePath
	// FACTORY_ATTEMPT_NUMBER/FACTORY_FAIL_VERIFY_UNTIL_ATTEMPT let a test
	// command deliberately vary its result by attempt — used by
	// cmd/smoketest's fixture repo to exercise the verify↔revise_verify
	// loop for real, without the conductor itself knowing anything about
	// attempt-based simulation.
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("FACTORY_ATTEMPT_NUMBER=%d", in.AttemptNumber),
		fmt.Sprintf("FACTORY_FAIL_VERIFY_UNTIL_ATTEMPT=%v", in.RunParams["fail_verify_until_attempt"]),
	)

	out, err := cmd.CombinedOutput()
	if err == nil {
		return conductor.ActivityOutput{Outcome: "pass"}, nil
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		// sh itself couldn't run (not found, permissions, ctx canceled,
		// ...) — an infra failure, not a legitimate verify result.
		return conductor.ActivityOutput{}, fmt.Errorf("verify: run.tests_lint_build: %w", err)
	}

	return conductor.ActivityOutput{
		Outcome:  "fail",
		Produced: map[string]any{"failing_tests_diff": string(out)},
	}, nil
}
