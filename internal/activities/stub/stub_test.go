package stub

import (
	"context"
	"testing"

	"factory/internal/conductor"
)

func TestRunTestsLintBuildThreshold(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		attempt     int
		failUntil   int
		wantOutcome string
	}{
		{1, 0, "pass"},
		{1, 1, "fail"},
		{2, 1, "pass"},
		{3, 99, "fail"},
	}
	for _, tc := range tests {
		out, err := RunTestsLintBuild(ctx, conductor.ActivityInput{
			AttemptNumber: tc.attempt,
			RunParams:     map[string]any{"fail_verify_until_attempt": tc.failUntil},
		})
		if err != nil {
			t.Fatalf("RunTestsLintBuild: %v", err)
		}
		if out.Outcome != tc.wantOutcome {
			t.Errorf("attempt=%d failUntil=%d: Outcome=%q, want %q", tc.attempt, tc.failUntil, out.Outcome, tc.wantOutcome)
		}
		_, hasDiff := out.Produced["failing_tests_diff"]
		if tc.wantOutcome == "fail" && !hasDiff {
			t.Errorf("attempt=%d failUntil=%d: expected failing_tests_diff in Produced", tc.attempt, tc.failUntil)
		}
		if tc.wantOutcome == "pass" && hasDiff {
			t.Errorf("attempt=%d failUntil=%d: unexpected failing_tests_diff on a pass", tc.attempt, tc.failUntil)
		}
	}
}

func TestRunTestsLintBuildMissingRunParamDefaultsToNeverFailing(t *testing.T) {
	out, err := RunTestsLintBuild(context.Background(), conductor.ActivityInput{AttemptNumber: 1})
	if err != nil {
		t.Fatalf("RunTestsLintBuild: %v", err)
	}
	if out.Outcome != "pass" {
		t.Errorf("Outcome = %q, want pass when fail_verify_until_attempt is absent", out.Outcome)
	}
}

func TestHarnessInvokeReturnsDiff(t *testing.T) {
	out, err := HarnessInvoke(context.Background(), conductor.ActivityInput{StepID: "execute"})
	if err != nil {
		t.Fatalf("HarnessInvoke: %v", err)
	}
	if out.Malformed {
		t.Errorf("Malformed = true, want false")
	}
	if _, ok := out.Produced["diff"]; !ok {
		t.Errorf("expected diff in Produced")
	}
}

func TestWorktreeCreateAndPRCreateAndLinkAreNoops(t *testing.T) {
	fns := map[string]func(context.Context, conductor.ActivityInput) (conductor.ActivityOutput, error){
		"worktree.create":    WorktreeCreate,
		"pr.create_and_link": PRCreateAndLink,
	}
	for name, fn := range fns {
		out, err := fn(context.Background(), conductor.ActivityInput{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if out.Outcome != "" || out.Malformed || len(out.Produced) != 0 {
			t.Errorf("%s: expected a bare no-op ActivityOutput, got %+v", name, out)
		}
	}
}

func TestRegistrationsCoversEveryActivityNameTheConductorDispatches(t *testing.T) {
	want := []string{"worktree.create", "run.tests_lint_build", "pr.create_and_link", conductor.HarnessInvokeActivityName}
	for _, name := range want {
		if _, ok := Registrations[name]; !ok {
			t.Errorf("Registrations missing entry for %q", name)
		}
	}
	if len(Registrations) != len(want) {
		t.Errorf("Registrations has %d entries, want %d", len(Registrations), len(want))
	}
}
