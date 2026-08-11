package pr

import (
	"strings"
	"testing"

	"factory/internal/conductor"
)

func TestBuildPRContentFullContext(t *testing.T) {
	in := conductor.ActivityInput{
		RunID: "run-1",
		Repo:  conductor.Repo{TestCommand: "make test"},
		Context: map[string]any{
			"task_description": "Fix the flaky retry loop\n\nSee issue #42 for repro steps.",
			"branch":           "factory/run-1",
			"assessment":       "The retry loop double-counts attempts under contention; will guard with a mutex.",
			"scope_contract": map[string]any{
				"acceptance_criteria": []any{"retries no longer double-count"},
				"non_goals":           []any{"not touching the unrelated backoff timing"},
			},
			"findings": []any{
				map[string]any{"description": "consider a comment here", "severity": "advisory", "scope_classification": "out_of_scope"},
			},
		},
	}
	diffStat := " x.go | 2 +-\n 1 file changed, 1 insertion(+), 1 deletion(-)\n"

	title, body := buildPRContent(in, diffStat)

	if title != "Fix the flaky retry loop" {
		t.Errorf("title = %q, want first line of task_description", title)
	}
	for _, want := range []string{
		"## Overview",
		"Fix the flaky retry loop",
		"## Intention",
		"guard with a mutex",
		"## Changes",
		"x.go | 2 +-",
		"What changed:",
		"consider a comment here",
		"## Risk assessment",
		"not touching the unrelated backoff timing",
		"## How to test",
		"make test",
		"factory/run-1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\nfull body:\n%s", want, body)
		}
	}
	// acceptance_criteria isn't repeated under Risk — only non_goals is.
	if strings.Contains(body, "retries no longer double-count") {
		t.Errorf("body should not repeat acceptance_criteria under Risk assessment:\n%s", body)
	}
	// findings describe what changed, not what's risky — they belong
	// under Changes only, not repeated under Risk assessment.
	changesIdx := strings.Index(body, "## Changes")
	riskIdx := strings.Index(body, "## Risk assessment")
	findingIdx := strings.Index(body, "consider a comment here")
	if !(changesIdx < findingIdx && findingIdx < riskIdx) {
		t.Errorf("expected the finding text between Changes and Risk assessment headers, got body:\n%s", body)
	}
}

func TestBuildPRContentEmptyContextFallsBackGracefully(t *testing.T) {
	in := conductor.ActivityInput{RunID: "run-2"}

	title, body := buildPRContent(in, "")

	if title != "factory: automated change (run run-2)" {
		t.Errorf("title = %q, want generic fallback", title)
	}
	for _, want := range []string{
		"No task description was recorded",
		"No plan assessment was recorded",
		"Diff summary unavailable.",
		"No risk signals were surfaced",
		"No test command is configured",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing fallback %q\nfull body:\n%s", want, body)
		}
	}
}

func TestPRTitleTruncatesLongFirstLine(t *testing.T) {
	long := strings.Repeat("a", 100)
	title := prTitle(long, "run-3")
	// "…" is a 3-byte UTF-8 rune, so byte length is 72 + len("…"), not 73.
	if !strings.HasSuffix(title, "…") {
		t.Errorf("title = %q, want ellipsis suffix", title)
	}
	if got := strings.TrimSuffix(title, "…"); len(got) != 72 {
		t.Errorf("title (minus ellipsis) length = %d, want 72", len(got))
	}
}

func TestBaseBranchPrefersContextOverRepoDefault(t *testing.T) {
	in := conductor.ActivityInput{
		Repo:    conductor.Repo{DefaultBranch: "repo-default"},
		Context: map[string]any{"default_branch": "resolved-default"},
	}
	if got := baseBranch(in); got != "resolved-default" {
		t.Errorf("baseBranch = %q, want context value to win", got)
	}

	in2 := conductor.ActivityInput{Repo: conductor.Repo{DefaultBranch: "repo-default"}}
	if got := baseBranch(in2); got != "repo-default" {
		t.Errorf("baseBranch = %q, want fallback to Repo.DefaultBranch", got)
	}
}
