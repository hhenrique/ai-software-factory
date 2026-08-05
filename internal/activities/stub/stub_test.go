package stub

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"factory/internal/conductor"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}
}

func newFixtureWorktree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	for _, args := range [][]string{
		{"add", "README.md"},
		{"commit", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return dir
}

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

func TestHarnessInvokeAppliesRealChangeWhenWorktreePathAvailable(t *testing.T) {
	requireGit(t)
	dir := newFixtureWorktree(t)

	out, err := HarnessInvoke(context.Background(), conductor.ActivityInput{
		StepID:        "execute",
		AttemptNumber: 1,
		Context:       map[string]any{"worktree_path": dir},
	})
	if err != nil {
		t.Fatalf("HarnessInvoke: %v", err)
	}
	if _, ok := out.Produced["diff"]; !ok {
		t.Errorf("expected diff in Produced")
	}

	logCmd := exec.Command("git", "log", "--oneline")
	logCmd.Dir = dir
	log, err := logCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v: %s", err, log)
	}
	if !strings.Contains(string(log), "execute") {
		t.Errorf("expected a commit mentioning step %q, got log:\n%s", "execute", log)
	}

	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = dir
	status, err := statusCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v: %s", err, status)
	}
	if strings.TrimSpace(string(status)) != "" {
		t.Errorf("expected a clean working tree after HarnessInvoke commits, got status:\n%s", status)
	}
}

func TestHarnessInvokeRetrySameAttemptIsIdempotent(t *testing.T) {
	requireGit(t)
	dir := newFixtureWorktree(t)

	in := conductor.ActivityInput{StepID: "execute", AttemptNumber: 1, Context: map[string]any{"worktree_path": dir}}
	if _, err := HarnessInvoke(context.Background(), in); err != nil {
		t.Fatalf("first HarnessInvoke: %v", err)
	}
	// Simulate an at-least-once Activity redelivery for the same attempt.
	if _, err := HarnessInvoke(context.Background(), in); err != nil {
		t.Fatalf("second HarnessInvoke (retry): %v", err)
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
