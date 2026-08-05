// Package stub holds throwaway stand-ins for the real git/test-runner/
// harness Activities — this slice proves the DAG-to-Temporal mapping and
// the budget-counter loop actually work end to end against a real
// Temporal server, without wiring in real harness adapters or real git/PR
// actions (explicitly out of scope, see .sketchpad/plan.md).
package stub

import (
	"context"
	"strconv"

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
func HarnessInvoke(ctx context.Context, in conductor.ActivityInput) (conductor.ActivityOutput, error) {
	return conductor.ActivityOutput{
		Produced: map[string]any{"diff": "--- stub diff for step " + in.StepID + " ---"},
	}, nil
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
