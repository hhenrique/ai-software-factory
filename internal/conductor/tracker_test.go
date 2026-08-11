package conductor

import (
	"strings"
	"testing"
)

func TestAuthorLineConductorForToolOwnedTransition(t *testing.T) {
	got := authorLine(TransitionEvent{FromStep: "verify", ToStep: "review", Outcome: "pass"})
	if got != "conductor" {
		t.Errorf("authorLine = %q, want %q for a tool-owned transition (Role unset)", got, "conductor")
	}
}

func TestAuthorLineWorkerIdentityForAgentTransition(t *testing.T) {
	got := authorLine(TransitionEvent{Role: "coder", Harness: "codex", Model: "gpt-5.6-luna", Effort: "medium"})
	want := "coder:codex/gpt-5.6-luna/medium"
	if got != want {
		t.Errorf("authorLine = %q, want %q", got, want)
	}
}

func TestAuthorLineOmitsTrailingSlashWhenEffortUnset(t *testing.T) {
	got := authorLine(TransitionEvent{Role: "planner", Harness: "claude-code", Model: "sonnet-5"})
	want := "planner:claude-code/sonnet-5"
	if got != want {
		t.Errorf("authorLine = %q, want %q (no trailing slash for unset effort)", got, want)
	}
}

func TestFormatTransitionCommentBareTransition(t *testing.T) {
	got := formatTransitionComment(TransitionEvent{FromStep: "", ToStep: "provision"})
	want := "conductor\n**(start) → provision**"
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

// TestFormatEventContentAssessmentLeadsVerdict is doc03's "structured
// tracking content per role" (Planner assessment) landing in the same
// comment doc08's v1 content already posts — assessment must read before
// the verdict it informed, not buried after routing/scope detail.
func TestFormatEventContentAssessmentLeadsVerdict(t *testing.T) {
	got := FormatEventContent(map[string]any{
		"assessment": "This needs the existing render loop, not a new one.",
		"verdict":    "proceed",
	})
	wantAssessment := strings.Index(got, "Assessment: This needs the existing render loop, not a new one.")
	wantVerdict := strings.Index(got, "Verdict: proceed")
	if wantAssessment == -1 || wantVerdict == -1 {
		t.Fatalf("FormatEventContent = %q, want both Assessment and Verdict present", got)
	}
	if wantAssessment > wantVerdict {
		t.Errorf("FormatEventContent = %q, want Assessment before Verdict", got)
	}
}

// TestFormatEventContentRationaleFollowsVerdict is doc03's Coder
// reasoning ("plus reasoning text when verdict: dispute... retained and
// shown to the Reviewer in the next round") — rendered as the
// justification for the verdict, so it reads after it.
func TestFormatEventContentRationaleFollowsVerdict(t *testing.T) {
	got := FormatEventContent(map[string]any{
		"verdict":   "dispute",
		"rationale": "The flagged line is dead code removed in the same diff, not a leftover.",
	})
	wantVerdict := strings.Index(got, "Verdict: dispute")
	wantRationale := strings.Index(got, "Rationale: The flagged line is dead code removed in the same diff, not a leftover.")
	if wantVerdict == -1 || wantRationale == -1 {
		t.Fatalf("FormatEventContent = %q, want both Verdict and Rationale present", got)
	}
	if wantVerdict > wantRationale {
		t.Errorf("FormatEventContent = %q, want Verdict before Rationale", got)
	}
}

// TestFormatEventContentChangeSummaryLeadsDiffCount covers
// execute/revise_verify/revise_review's new change_summary field (docs/03:
// the Coder narrating what it changed, from the same call that produced
// the diff) — rendered before the bare diff count, same "what/why before
// the number" ordering assessment/verdict already follow.
func TestFormatEventContentChangeSummaryLeadsDiffCount(t *testing.T) {
	got := FormatEventContent(map[string]any{
		"change_summary": "Added a time-based animation loop for the fixed Voronoi sites.",
		"diff":           "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n+added\n",
	})
	wantSummary := strings.Index(got, "Changes: Added a time-based animation loop")
	wantDiff := strings.Index(got, "Diff: 1 file(s) changed")
	if wantSummary == -1 || wantDiff == -1 {
		t.Fatalf("FormatEventContent = %q, want both change_summary and the diff count present", got)
	}
	if wantSummary > wantDiff {
		t.Errorf("FormatEventContent = %q, want change_summary before the diff count", got)
	}
}

// TestFormatEventContentOmitsAssessmentAndRationaleWhenAbsent guards
// backward compatibility: a Workflow Definition whose output_schema
// doesn't declare these fields (or a harness call that omits them) must
// render exactly as before — no "Assessment: " / "Rationale: " label
// with empty content.
func TestFormatEventContentOmitsAssessmentAndRationaleWhenAbsent(t *testing.T) {
	got := FormatEventContent(map[string]any{"verdict": "pass"})
	if strings.Contains(got, "Assessment") || strings.Contains(got, "Rationale") {
		t.Errorf("FormatEventContent = %q, want no Assessment/Rationale section when absent from produced", got)
	}
}

// TestFormatFindingsFallsBackToMessageFieldWhenDescriptionAbsent is a
// regression test for a real bug: the review step's output_schema used
// to declare findings as a bare "array" with no example item shape, so a
// real Reviewer call returned {message, severity} instead of doc03's
// {description, location, scope_classification, severity} — rendering as
// a content-free "[medium/]  ()" line. The schema now shows an example
// item (see workflows/issue-to-pr.yaml), but this renderer
// must keep degrading gracefully regardless — a harness's own field
// naming isn't something this package can fully control.
func TestFormatFindingsFallsBackToMessageFieldWhenDescriptionAbsent(t *testing.T) {
	produced := map[string]any{
		"findings": []any{
			map[string]any{
				"message":  "stepAnimation only reflects once per axis.",
				"severity": "medium",
			},
		},
	}
	got := FormatEventContent(produced)
	if !strings.Contains(got, "[medium] stepAnimation only reflects once per axis.") {
		t.Errorf("FormatEventContent = %q, want the message field used when description is absent", got)
	}
}

// TestFormatFindingsDropsFindingWithNoUsableText guards against
// rendering an empty "- " line for a finding that has neither field.
func TestFormatFindingsDropsFindingWithNoUsableText(t *testing.T) {
	produced := map[string]any{
		"findings": []any{
			map[string]any{"severity": "medium"},
		},
	}
	got := FormatEventContent(produced)
	if strings.Contains(got, "\n- ") {
		t.Errorf("FormatEventContent = %q, want the textless finding dropped, not rendered blank", got)
	}
}

// TestFormatFindingsAcceptsSingleElementArraySeverityAndScope is a
// regression test: a real Reviewer call (gpt-5-mini, via the copilot
// CLI) returned severity/scope_classification as single-element arrays
// (["advisory"], ["in_scope"]) instead of bare strings for the same
// enum-array-schema reason as the verdict bug — formatFindings already
// degraded safely (silently dropped the tag), but that lost real,
// available information rather than showing it.
func TestFormatFindingsAcceptsSingleElementArraySeverityAndScope(t *testing.T) {
	produced := map[string]any{
		"findings": []any{
			map[string]any{
				"description":          "missing nil check",
				"location":             "foo.go:12",
				"severity":             []any{"advisory"},
				"scope_classification": []any{"in_scope"},
			},
		},
	}
	got := FormatEventContent(produced)
	if !strings.Contains(got, "[advisory/in_scope] missing nil check (foo.go:12)") {
		t.Errorf("FormatEventContent = %q, want severity/scope_classification shown despite the array wrapping", got)
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

// TestShouldPostComment covers the curated-mirror rule this session
// added on top of doc08's v1 ("leave only the interactions with the
// agents... and any human pending action" — plus a Run's own final
// result, since a clean COMPLETED run would otherwise never mention the
// PR anywhere in the issue thread).
func TestShouldPostComment(t *testing.T) {
	cases := []struct {
		name string
		ev   TransitionEvent
		want bool
	}{
		{"agent step result", TransitionEvent{ToStep: "verify", AgentStep: true}, true},
		{"review_pending", TransitionEvent{ToStep: "REVIEW_PENDING"}, true},
		{"completed", TransitionEvent{ToStep: "COMPLETED"}, true},
		{"failed", TransitionEvent{ToStep: "FAILED"}, true},
		{"cancelled", TransitionEvent{ToStep: "CANCELLED"}, true},
		{"tool step routing", TransitionEvent{ToStep: "verify", AgentStep: false}, false},
		{"run start", TransitionEvent{FromStep: "", ToStep: "provision"}, false},
		{"resume out of REVIEW_PENDING", TransitionEvent{FromStep: "REVIEW_PENDING", ToStep: "plan"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldPostComment(c.ev); got != c.want {
				t.Errorf("shouldPostComment(%+v) = %v, want %v", c.ev, got, c.want)
			}
		})
	}
}
