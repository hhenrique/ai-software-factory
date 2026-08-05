// Package stub holds throwaway stand-ins for the real git/test-runner/
// harness Activities — this slice proves the DAG-to-Temporal mapping and
// the budget-counter loop actually work end to end against a real
// Temporal server, without wiring in real harness adapters or real git/PR
// actions (explicitly out of scope, see .sketchpad/plan.md).
package stub

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"factory/internal/conductor"
)

// WorktreeCreate is a no-op stand-in for the real worktree.create tool
// Activity (git clone/checkout, dependency install, index warm-up).
func WorktreeCreate(ctx context.Context, in conductor.ActivityInput) (conductor.ActivityOutput, error) {
	return conductor.ActivityOutput{}, nil
}

// PRCreateAndLink is a no-op stand-in for the real pr.create_and_link tool
// Activity.
func PRCreateAndLink(ctx context.Context, in conductor.ActivityInput) (conductor.ActivityOutput, error) {
	return conductor.ActivityOutput{}, nil
}

// HarnessInvoke is a throwaway stand-in for a real harness adapter call —
// every agent step this slice dispatches here regardless of role, and it
// always reports a canned diff, never malformed output.
//
// If worktree_path is available in context (the step declared it, e.g.
// execute/revise_verify in the reference Workflow Definitions), it also
// actually writes and commits a small change there — a stub that claims
// to produce a "diff" but never applies it to any file is lying about its
// own contract, and downstream real Activities depend on that being true:
// pr.create_and_link has nothing to push/PR if the branch never diverges
// from its base. When worktree_path isn't available, it falls back to
// just returning the canned diff text, unchanged from before.
func HarnessInvoke(ctx context.Context, in conductor.ActivityInput) (conductor.ActivityOutput, error) {
	diff := "--- stub diff for step " + in.StepID + " ---"

	if worktreePath, _ := in.Context["worktree_path"].(string); worktreePath != "" {
		if err := applyStubChange(ctx, worktreePath, in.StepID, in.AttemptNumber); err != nil {
			return conductor.ActivityOutput{}, fmt.Errorf("stub: harness.invoke: %w", err)
		}
	}

	return conductor.ActivityOutput{
		Produced: map[string]any{"diff": diff},
	}, nil
}

// applyStubChange appends a line recording this invocation to a scratch
// file and commits it — deterministic, harmless, and enough for
// downstream steps (verify, merge) to have something real to work with.
func applyStubChange(ctx context.Context, worktreePath, stepID string, attempt int) error {
	path := filepath.Join(worktreePath, "FACTORY_STUB_CHANGE.md")
	existing, _ := os.ReadFile(path)
	line := fmt.Sprintf("- stub harness change from step %q, attempt %d\n", stepID, attempt)
	if err := os.WriteFile(path, append(existing, []byte(line)...), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := runGit(ctx, worktreePath, "add", "FACTORY_STUB_CHANGE.md"); err != nil {
		return err
	}

	// Retrying the same attempt (e.g. an at-least-once Activity
	// redelivery) appends an identical line, leaving nothing staged —
	// that's a no-op, not an error.
	diffCmd := exec.CommandContext(ctx, "git", "diff", "--cached", "--quiet")
	diffCmd.Dir = worktreePath
	if err := diffCmd.Run(); err == nil {
		return nil
	}

	return runGit(ctx, worktreePath, "-c", "user.email=factory-stub@example.com", "-c", "user.name=factory-stub",
		"commit", "-q", "-m", fmt.Sprintf("stub: change from %s (attempt %d)", stepID, attempt))
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

// RunTestsLintBuild is a deterministic stand-in for the real
// run.tests_lint_build tool Activity: a pure function of
// in.AttemptNumber and the Run-level fail_verify_until_attempt param
// (never wall-clock/random), so smoke-test scenarios are exactly
// assertable. Fails (Outcome: "fail") while AttemptNumber is at or below
// the threshold, then passes.
func RunTestsLintBuild(ctx context.Context, in conductor.ActivityInput) (conductor.ActivityOutput, error) {
	failUntil := failVerifyUntilAttempt(in)
	if in.AttemptNumber <= failUntil {
		return conductor.ActivityOutput{
			Outcome:  "fail",
			Produced: map[string]any{"failing_tests_diff": "stub failing test at attempt " + strconv.Itoa(in.AttemptNumber)},
		}, nil
	}
	return conductor.ActivityOutput{Outcome: "pass"}, nil
}

// failVerifyUntilAttempt reads the threshold out of RunParams, tolerating
// both a native int (set directly in-process, e.g. by conductor's own
// tests via the workflow test suite) and a float64 (what any real
// Temporal round-trip produces: ActivityInput crosses the wire as JSON,
// and Go's default map[string]any JSON decoding turns every number into
// float64 — a plain `v.(int)` type assertion would silently fail and
// default to 0 against a real server despite passing every in-process test).
func failVerifyUntilAttempt(in conductor.ActivityInput) int {
	switch v := in.RunParams["fail_verify_until_attempt"].(type) {
	case int:
		return v
	case float64:
		return int(v)
	default:
		return 0
	}
}

// Registrations maps each Activity name the conductor package's
// step-to-Activity mapping expects (see internal/conductor/registry.go)
// to its stub implementation — the single list cmd/worker registers from.
var Registrations = map[string]any{
	"worktree.create":                   WorktreeCreate,
	"run.tests_lint_build":              RunTestsLintBuild,
	"pr.create_and_link":                PRCreateAndLink,
	conductor.HarnessInvokeActivityName: HarnessInvoke,
}
