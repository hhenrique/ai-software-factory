package harness

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"factory/internal/conductor"
	"factory/internal/workers"
)

// TestAdaptersMatchKnownHarnesses guards against the two lists drifting:
// internal/workers.KnownHarnesses is what a Worker's harness field is
// validated against at Create/Update time (fail fast, before any Run
// wastes real work reaching harness.invoke's own runtime lookup — found
// live: a Worker saved with harness "copilot" only failed once a real
// Run's Reviewer step got there, after provision/plan/execute had
// already run for real). workers can't import this package to derive the
// list directly (that would invert the dependency: this package is an
// edge implementation of doc03's harness adapter contract, workers is
// core domain), so this test is what keeps them in sync instead — add an
// adapter here, add it to workers.KnownHarnesses too, or this fails.
func TestAdaptersMatchKnownHarnesses(t *testing.T) {
	var got []string
	for name := range adapters {
		got = append(got, name)
	}
	want := append([]string(nil), workers.KnownHarnesses...)
	sort.Strings(got)
	sort.Strings(want)

	if len(got) != len(want) {
		t.Fatalf("adapters = %v, workers.KnownHarnesses = %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("adapters = %v, workers.KnownHarnesses = %v", got, want)
		}
	}
}

// writeFakeCLI installs a fake `name` executable on PATH (prepended,
// restored after the test) that: records its argv to $FAKE_CLI_ARGV_FILE
// (one arg per line) if that env var is set, and behaves according to
// $FAKE_CLI_MODE:
//   - "diff": writes CHANGED.md into its cwd, prints a claude-shaped
//     --output-format json result on stdout.
//   - "schema": prints a claude-shaped result whose "result" field
//     contains a fenced ```json block.
//   - "schema-array-verdict": same, but "verdict" is a single-element
//     array (["approved"]) instead of a bare string — found live from a
//     real model (gpt-5-mini via the copilot CLI); scalarString exists to
//     tolerate exactly this.
//   - "malformed": prints a claude-shaped result whose "result" is plain
//     text, not JSON.
//   - "error": prints a claude-shaped result with is_error=true.
//
// Reused across the claude/codex/copilot Invoke tests since they all go
// through the same normalized invocationResult contract on the way out —
// only claude's is verified against real --output-format json shape
// (internal/activities/harness/claude.go's live-checked struct); this
// fake stands in as "any adapter, behaving as documented" for exercising
// Activities.Invoke's own logic (dispatch, diff commit, JSON extraction),
// not for verifying each adapter's real argv translation.
func writeFakeCLI(t *testing.T, name string) {
	t.Helper()
	dir := t.TempDir()
	// Built via concatenation, not one raw string literal: the "schema"
	// case's fenced ```json block would otherwise terminate a Go backtick
	// string early.
	fence := "```"
	script := `#!/bin/sh
if [ -n "$FAKE_CLI_ARGV_FILE" ]; then
  for a in "$@"; do printf '%s\n' "$a" >> "$FAKE_CLI_ARGV_FILE"; done
fi
case "$FAKE_CLI_MODE" in
  diff)
    echo "changed" > CHANGED.md
    echo '{"is_error":false,"result":"Done.","usage":{"input_tokens":5,"output_tokens":7,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}'
    ;;
  schema)
    echo '{"is_error":false,"result":"Here you go:\n\n` + fence + `json\n{\"verdict\":\"proceed\",\"scope_contract\":{\"acceptance_criteria\":[\"x\"]}}\n` + fence + `","usage":{"input_tokens":5,"output_tokens":7,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}'
    ;;
  schema-no-verdict)
    echo '{"is_error":false,"result":"Here you go:\n\n` + fence + `json\n{\"scope_contract\":{\"acceptance_criteria\":[\"x\"]}}\n` + fence + `","usage":{"input_tokens":5,"output_tokens":7,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}'
    ;;
  schema-array-verdict)
    echo '{"is_error":false,"result":"` + fence + `json\n{\"verdict\":[\"approved\"]}\n` + fence + `","usage":{"input_tokens":5,"output_tokens":7,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}'
    ;;
  malformed)
    echo '{"is_error":false,"result":"sure, doing that now","usage":{"input_tokens":1,"output_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}'
    ;;
  error)
    echo '{"is_error":true,"result":"boom","usage":{"input_tokens":1,"output_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}'
    exit 0
    ;;
esac
`
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestInvokeSchemaLessStepCommitsHarnessDiff(t *testing.T) {
	requireGit(t)
	writeFakeCLI(t, "claude")
	t.Setenv("FAKE_CLI_MODE", "diff")

	dir := newFixtureWorktree(t)
	a := &Activities{}
	out, err := a.Invoke(context.Background(), conductor.ActivityInput{
		StepID:        "execute",
		Harness:       "claude-code",
		AttemptNumber: 1,
		Context:       map[string]any{"worktree_path": dir, "task_description": "add a file"},
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if out.Malformed {
		t.Errorf("Malformed = true, want false for a schema-less step")
	}
	diff, _ := out.Produced["diff"].(string)
	if !strings.Contains(diff, "changed") {
		t.Errorf("Produced[diff] = %q, want it to contain the harness's file edit", diff)
	}
	if out.TokensUsed != 12 { // 5 + 7 + 0 + 0
		t.Errorf("TokensUsed = %d, want 12", out.TokensUsed)
	}
}

func TestInvokeSchemaStepParsesVerdict(t *testing.T) {
	requireGit(t)
	writeFakeCLI(t, "claude")
	t.Setenv("FAKE_CLI_MODE", "schema")

	dir := newFixtureWorktree(t)
	a := &Activities{}
	out, err := a.Invoke(context.Background(), conductor.ActivityInput{
		StepID:       "plan",
		Harness:      "claude-code",
		Context:      map[string]any{"worktree_path": dir},
		OutputSchema: map[string]any{"verdict": []any{"proceed", "reject", "escalate"}, "scope_contract": "object"},
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if out.Malformed {
		t.Fatalf("Malformed = true, want false")
	}
	if out.Outcome != "proceed" {
		t.Errorf("Outcome = %q, want proceed", out.Outcome)
	}
	if _, ok := out.Produced["scope_contract"]; !ok {
		t.Errorf("Produced missing scope_contract: %+v", out.Produced)
	}
}

// TestInvokeAcceptsSingleElementArrayVerdict is a regression test: a real
// Reviewer call (gpt-5-mini, via the copilot CLI) returned
// {"verdict": ["approved"]} for an enum field instead of the bare string
// output_schema asked for, routing to malformed_output and burning a
// whole real review round for nothing. scalarString's tolerance for
// exactly this shape must apply through the full Invoke path, not just
// in isolation.
func TestInvokeAcceptsSingleElementArrayVerdict(t *testing.T) {
	requireGit(t)
	writeFakeCLI(t, "claude")
	t.Setenv("FAKE_CLI_MODE", "schema-array-verdict")

	dir := newFixtureWorktree(t)
	a := &Activities{}
	out, err := a.Invoke(context.Background(), conductor.ActivityInput{
		StepID:       "review",
		Harness:      "claude-code",
		Context:      map[string]any{"worktree_path": dir},
		OutputSchema: map[string]any{"verdict": []any{"approved", "changes_required"}},
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if out.Malformed {
		t.Fatalf("Malformed = true, want false — a single-element array verdict must be tolerated")
	}
	if out.Outcome != "approved" {
		t.Errorf("Outcome = %q, want approved", out.Outcome)
	}
	if v, _ := out.Produced["verdict"].(string); v != "approved" {
		t.Errorf("Produced[verdict] = %v, want the plain string \"approved\" (normalized, not the raw array)", out.Produced["verdict"])
	}
}

func TestInvokeMalformedOutputWhenSchemaExpectedButNotJSON(t *testing.T) {
	requireGit(t)
	writeFakeCLI(t, "claude")
	t.Setenv("FAKE_CLI_MODE", "malformed")

	dir := newFixtureWorktree(t)
	a := &Activities{}
	out, err := a.Invoke(context.Background(), conductor.ActivityInput{
		StepID:       "plan",
		Harness:      "claude-code",
		Context:      map[string]any{"worktree_path": dir},
		OutputSchema: map[string]any{"verdict": []any{"proceed", "reject"}},
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !out.Malformed {
		t.Errorf("Malformed = false, want true when the harness didn't return parseable JSON")
	}
}

// TestInvokeMalformedWhenVerdictMissingFromOtherwiseValidJSON is a
// regression test: found live against a real Reviewer response — the
// harness's JSON parsed fine, but had no "verdict" key. That must route
// through on_malformed_output like any other unparseable response, not
// silently produce Outcome "" (which route() then can't find an on:
// mapping for, hard-erroring the whole Run instead of the intended
// conservative malformed-output routing).
func TestInvokeMalformedWhenVerdictMissingFromOtherwiseValidJSON(t *testing.T) {
	requireGit(t)
	writeFakeCLI(t, "claude")
	t.Setenv("FAKE_CLI_MODE", "schema-no-verdict")

	dir := newFixtureWorktree(t)
	a := &Activities{}
	out, err := a.Invoke(context.Background(), conductor.ActivityInput{
		StepID:       "plan",
		Harness:      "claude-code",
		Context:      map[string]any{"worktree_path": dir},
		OutputSchema: map[string]any{"verdict": []any{"proceed", "reject"}, "scope_contract": "object"},
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !out.Malformed {
		t.Errorf("Malformed = false, want true when the parsed JSON has no verdict field")
	}
	if out.Outcome != "" {
		t.Errorf("Outcome = %q, want empty on malformed output", out.Outcome)
	}
}

func TestInvokeHarnessErrorResultIsAnActivityError(t *testing.T) {
	requireGit(t)
	writeFakeCLI(t, "claude")
	t.Setenv("FAKE_CLI_MODE", "error")

	dir := newFixtureWorktree(t)
	a := &Activities{}
	_, err := a.Invoke(context.Background(), conductor.ActivityInput{
		StepID:  "execute",
		Harness: "claude-code",
		Context: map[string]any{"worktree_path": dir},
	})
	if err == nil {
		t.Fatalf("expected an error when the harness reports is_error=true")
	}
}

func TestInvokeUnknownHarness(t *testing.T) {
	a := &Activities{}
	_, err := a.Invoke(context.Background(), conductor.ActivityInput{
		Harness: "not-a-real-harness",
		Context: map[string]any{"worktree_path": t.TempDir()},
	})
	if err == nil {
		t.Fatalf("expected an error for an unknown harness")
	}
}

// TestInvokeWithoutWorktreePathSkipsDiffButStillWorks covers the
// Planner/Reviewer shape: no worktree_path in context (they judge a task
// description or an already-produced diff string, never edit files) —
// this must not error, and must not attempt a worktree diff commit at all
// (there's no worktree to commit in).
func TestInvokeWithoutWorktreePathSkipsDiffButStillWorks(t *testing.T) {
	writeFakeCLI(t, "claude")
	t.Setenv("FAKE_CLI_MODE", "schema")

	a := &Activities{}
	out, err := a.Invoke(context.Background(), conductor.ActivityInput{
		StepID:       "plan",
		Harness:      "claude-code",
		Context:      map[string]any{"task_description": "plan this"},
		OutputSchema: map[string]any{"verdict": []any{"proceed", "reject", "escalate"}, "scope_contract": "object"},
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if out.Malformed {
		t.Fatalf("Malformed = true, want false")
	}
	if out.Outcome != "proceed" {
		t.Errorf("Outcome = %q, want proceed", out.Outcome)
	}
	if _, ok := out.Produced["diff"]; ok {
		t.Errorf("Produced should not contain diff when there's no worktree_path, got %+v", out.Produced)
	}
}

func TestInvokePassesModelAndEffortAsFlags(t *testing.T) {
	requireGit(t)
	writeFakeCLI(t, "claude")
	t.Setenv("FAKE_CLI_MODE", "diff")
	argvFile := filepath.Join(t.TempDir(), "argv.txt")
	t.Setenv("FAKE_CLI_ARGV_FILE", argvFile)

	dir := newFixtureWorktree(t)
	a := &Activities{}
	if _, err := a.Invoke(context.Background(), conductor.ActivityInput{
		Harness: "claude-code",
		Model:   "sonnet",
		Params:  map[string]string{"effort": "high"},
		Context: map[string]any{"worktree_path": dir},
	}); err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	argv, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv file: %v", err)
	}
	got := string(argv)
	if !strings.Contains(got, "--model\nsonnet") {
		t.Errorf("argv %q missing --model sonnet", got)
	}
	if !strings.Contains(got, "--effort\nhigh") {
		t.Errorf("argv %q missing --effort high", got)
	}
}
