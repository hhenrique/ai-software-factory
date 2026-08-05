package verify

import (
	"context"
	"strings"
	"testing"

	"factory/internal/conductor"
)

func TestRunTestsLintBuildPass(t *testing.T) {
	a := &Activities{}
	out, err := a.RunTestsLintBuild(context.Background(), conductor.ActivityInput{
		Context: map[string]any{"worktree_path": t.TempDir()},
		Repo:    conductor.Repo{TestCommand: "echo ok && exit 0"},
	})
	if err != nil {
		t.Fatalf("RunTestsLintBuild: %v", err)
	}
	if out.Outcome != "pass" {
		t.Errorf("Outcome = %q, want pass", out.Outcome)
	}
	if out.Produced != nil {
		t.Errorf("expected no Produced fields on pass, got %+v", out.Produced)
	}
}

func TestRunTestsLintBuildFail(t *testing.T) {
	a := &Activities{}
	out, err := a.RunTestsLintBuild(context.Background(), conductor.ActivityInput{
		Context: map[string]any{"worktree_path": t.TempDir()},
		Repo:    conductor.Repo{TestCommand: "echo something broke && exit 1"},
	})
	if err != nil {
		t.Fatalf("RunTestsLintBuild: %v", err)
	}
	if out.Outcome != "fail" {
		t.Errorf("Outcome = %q, want fail", out.Outcome)
	}
	diff, _ := out.Produced["failing_tests_diff"].(string)
	if !strings.Contains(diff, "something broke") {
		t.Errorf("failing_tests_diff = %q, want it to contain command output", diff)
	}
}

func TestRunTestsLintBuildRunsInWorktreeDir(t *testing.T) {
	dir := t.TempDir()
	a := &Activities{}
	// A failing command's output is captured (Produced), so use a
	// deliberate failure to inspect the command's actual cwd.
	out, err := a.RunTestsLintBuild(context.Background(), conductor.ActivityInput{
		Context: map[string]any{"worktree_path": dir},
		Repo:    conductor.Repo{TestCommand: "pwd && exit 1"},
	})
	if err != nil {
		t.Fatalf("RunTestsLintBuild: %v", err)
	}
	diff, _ := out.Produced["failing_tests_diff"].(string)
	if !strings.Contains(diff, dir) {
		t.Errorf("failing_tests_diff = %q, want it to show cwd %q (command should run in the worktree)", diff, dir)
	}
}

func TestRunTestsLintBuildExposesAttemptNumberAsEnvVar(t *testing.T) {
	a := &Activities{}
	out, err := a.RunTestsLintBuild(context.Background(), conductor.ActivityInput{
		Context:       map[string]any{"worktree_path": t.TempDir()},
		Repo:          conductor.Repo{TestCommand: `[ "$FACTORY_ATTEMPT_NUMBER" -le "${FACTORY_FAIL_VERIFY_UNTIL_ATTEMPT:-0}" ] && exit 1 || exit 0`},
		AttemptNumber: 1,
		RunParams:     map[string]any{"fail_verify_until_attempt": 2},
	})
	if err != nil {
		t.Fatalf("RunTestsLintBuild: %v", err)
	}
	if out.Outcome != "fail" {
		t.Fatalf("attempt 1 of 2: Outcome = %q, want fail", out.Outcome)
	}

	out, err = a.RunTestsLintBuild(context.Background(), conductor.ActivityInput{
		Context:       map[string]any{"worktree_path": t.TempDir()},
		Repo:          conductor.Repo{TestCommand: `[ "$FACTORY_ATTEMPT_NUMBER" -le "${FACTORY_FAIL_VERIFY_UNTIL_ATTEMPT:-0}" ] && exit 1 || exit 0`},
		AttemptNumber: 3,
		RunParams:     map[string]any{"fail_verify_until_attempt": 2},
	})
	if err != nil {
		t.Fatalf("RunTestsLintBuild: %v", err)
	}
	if out.Outcome != "pass" {
		t.Fatalf("attempt 3 of 2: Outcome = %q, want pass", out.Outcome)
	}
}

func TestRunTestsLintBuildMissingWorktreePath(t *testing.T) {
	a := &Activities{}
	_, err := a.RunTestsLintBuild(context.Background(), conductor.ActivityInput{
		Repo: conductor.Repo{TestCommand: "exit 0"},
	})
	if err == nil {
		t.Fatalf("expected error for missing worktree_path")
	}
}

func TestRunTestsLintBuildMissingTestCommand(t *testing.T) {
	a := &Activities{}
	_, err := a.RunTestsLintBuild(context.Background(), conductor.ActivityInput{
		Context: map[string]any{"worktree_path": t.TempDir()},
	})
	if err == nil {
		t.Fatalf("expected error for missing Repo.TestCommand")
	}
}
