package conductor

import (
	"strings"
	"testing"
)

func TestFormatTransitionCommentBareTransition(t *testing.T) {
	got := formatTransitionComment(TransitionEvent{FromStep: "", ToStep: "provision"})
	want := "**(start) → provision**"
	if got != want {
		t.Errorf("formatTransitionComment = %q, want %q", got, want)
	}
}

func TestFormatTransitionCommentIncludesFailureReason(t *testing.T) {
	got := formatTransitionComment(TransitionEvent{FromStep: "provision", ToStep: "FAILED", FailureReason: "mkdir: permission denied"})
	if !strings.Contains(got, "Failed: mkdir: permission denied") {
		t.Errorf("formatTransitionComment = %q, want it to contain the failure reason", got)
	}
}

func TestFormatTransitionCommentIncludesVerdict(t *testing.T) {
	got := formatTransitionComment(TransitionEvent{FromStep: "plan", ToStep: "execute", Produced: map[string]any{"verdict": "proceed"}})
	if !strings.Contains(got, "Verdict: proceed") {
		t.Errorf("formatTransitionComment = %q, want it to contain the verdict", got)
	}
}

func TestFormatTransitionCommentIncludesScopeContract(t *testing.T) {
	produced := map[string]any{
		"scope_contract": map[string]any{
			"acceptance_criteria": []any{"tests pass", "no regressions"},
			"in_scope_paths":      []any{"internal/foo"},
			"non_goals":           []any{},
		},
	}
	got := formatTransitionComment(TransitionEvent{FromStep: "plan", ToStep: "execute", Produced: produced})
	if !strings.Contains(got, "acceptance_criteria: tests pass; no regressions") {
		t.Errorf("formatTransitionComment = %q, want it to list acceptance_criteria", got)
	}
	if !strings.Contains(got, "in_scope_paths: internal/foo") {
		t.Errorf("formatTransitionComment = %q, want it to list in_scope_paths", got)
	}
	if strings.Contains(got, "non_goals") {
		t.Errorf("formatTransitionComment = %q, want an empty non_goals list omitted", got)
	}
}

func TestFormatTransitionCommentIncludesFindings(t *testing.T) {
	produced := map[string]any{
		"findings": []any{
			map[string]any{"severity": "blocking", "scope_classification": "in_scope", "description": "missing nil check", "location": "foo.go:12"},
		},
	}
	got := formatTransitionComment(TransitionEvent{FromStep: "review", ToStep: "coder_response", Produced: produced})
	if !strings.Contains(got, "Findings (1):") || !strings.Contains(got, "[blocking/in_scope] missing nil check (foo.go:12)") {
		t.Errorf("formatTransitionComment = %q, want it to include the finding", got)
	}
}

func TestFormatTransitionCommentIncludesDiffSummaryNotFullDiff(t *testing.T) {
	diff := "diff --git a/foo.go b/foo.go\n--- a/foo.go\n+++ b/foo.go\n@@ -1,2 +1,3 @@\n line1\n+line2\n-line3\n"
	produced := map[string]any{"diff": diff}
	got := formatTransitionComment(TransitionEvent{FromStep: "execute", ToStep: "verify", Produced: produced})
	if !strings.Contains(got, "Diff: 1 file(s) changed, +1/-1 lines") {
		t.Errorf("formatTransitionComment = %q, want a file/line-count summary", got)
	}
	if strings.Contains(got, "@@ -1,2 +1,3 @@") {
		t.Errorf("formatTransitionComment = %q, must not include the raw diff hunk (doc08: noise for a human skimming an issue thread)", got)
	}
}

func TestSummarizeDiffCountsFilesAndLines(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/a.go b/a.go",
		"--- a/a.go",
		"+++ b/a.go",
		"@@ -1,1 +1,2 @@",
		"-old",
		"+new1",
		"+new2",
		"diff --git a/b.go b/b.go",
		"--- a/b.go",
		"+++ b/b.go",
		"@@ -1,1 +0,0 @@",
		"-removed",
		"",
	}, "\n")

	files, added, removed := summarizeDiff(diff)
	if files != 2 {
		t.Errorf("files = %d, want 2", files)
	}
	if added != 2 {
		t.Errorf("added = %d, want 2", added)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}
}

func TestPostTrackerCommentsSkipsWhenNoTargetsResolved(t *testing.T) {
	// A run with no pr_url in context and no SourceRef must not attempt
	// any Activity call at all — postTrackerComments is called with a nil
	// workflow.Context here specifically to prove this: any attempt to
	// dispatch would nil-panic, so a clean return proves the early skip.
	postTrackerComments(nil, SourceRef{}, map[string]any{}, TransitionEvent{})
}
