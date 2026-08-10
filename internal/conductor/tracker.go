package conductor

import (
	"fmt"
	"strings"

	"go.temporal.io/sdk/workflow"
)

// postTrackerComments best-effort mirrors a transition
// (docs/08-tracking-integration.md) onto whatever external tracker(s) are
// resolved for this Run: the Run's PR, once pr_url is known (produced by
// pr.create_and_link and merged into runContext, so once known it stays
// resolvable for every later transition too, not just the one that
// produced it), and the Task's source, whenever SourceRef names one.
// Posting failures never fail the Run — see TrackerPostCommentActivityName's
// Activity implementation for how they're still recorded as a queryable
// fact rather than silently dropped.
func postTrackerComments(ctx workflow.Context, sourceRef SourceRef, runContext map[string]any, ev TransitionEvent) {
	body := formatTransitionComment(ev)

	if prURL, _ := runContext["pr_url"].(string); prURL != "" {
		postTrackerComment(ctx, ev, TrackerCommentInput{
			RunID: ev.RunID, TargetKind: "github_pr", TargetRef: prURL, Body: body,
		})
	}
	if sourceRef.Kind != "" {
		postTrackerComment(ctx, ev, TrackerCommentInput{
			RunID: ev.RunID, TargetKind: sourceRef.Kind, TargetRef: sourceRef.Ref, Body: body,
		})
	}
}

// postTrackerComment dispatches one PostComment Activity call, logging
// (not propagating) a failure — same best-effort posture as
// recordTransition's own event-recording call.
func postTrackerComment(ctx workflow.Context, ev TransitionEvent, in TrackerCommentInput) {
	if err := workflow.ExecuteActivity(ctx, TrackerPostCommentActivityName, in).Get(ctx, nil); err != nil {
		workflow.GetLogger(ctx).Warn("conductor: failed to post tracker comment",
			"error", err, "run_id", ev.RunID, "target_kind", in.TargetKind, "from_step", ev.FromStep, "to_step", ev.ToStep)
	}
}

// formatTransitionComment renders a transition into a human-readable
// comment body (doc08 v1: "formats what agent steps already produce
// today" — verdict, scope_contract, findings, a diff summary — not the
// richer per-role narrative content that's still deferred to a v2).
// ev.Produced is nil for transitions with no Activity call of their own
// (Run start, budget-exhausted routing, REVIEW_PENDING resume/cancel) —
// those still get a bare transition line.
func formatTransitionComment(ev TransitionEvent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s → %s**", stepLabel(ev.FromStep), stepLabel(ev.ToStep))

	if ev.FailureReason != "" {
		fmt.Fprintf(&b, "\n\nFailed: %s", ev.FailureReason)
	}
	if content := FormatEventContent(ev.Produced); content != "" {
		fmt.Fprintf(&b, "\n\n%s", content)
	}
	return b.String()
}

// FormatEventContent renders a transition's Produced fields (verdict,
// scope_contract, findings, a diff summary) into a compact human-readable
// block — the same v1 content formatTransitionComment posts externally
// (docs/08), exported so a control-plane surface (internal/inbox,
// internal/backlog) can show the same substance for "why is this Run
// stuck" without needing Temporal's raw history to find it — doc04's
// "full trace/replay per Run" is non-negotiable even for a minimal build.
// "" if produced is empty or carries none of these recognized fields.
func FormatEventContent(produced map[string]any) string {
	var b strings.Builder
	writeSection := func(s string) {
		if s == "" {
			return
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(s)
	}

	if v, _ := produced["verdict"].(string); v != "" {
		writeSection("Verdict: " + v)
	}
	if sc, ok := produced["scope_contract"].(map[string]any); ok {
		writeSection(formatScopeContract(sc))
	}
	if findings, ok := produced["findings"].([]any); ok && len(findings) > 0 {
		writeSection(formatFindings(findings))
	}
	if diff, _ := produced["diff"].(string); diff != "" {
		files, added, removed := summarizeDiff(diff)
		writeSection(fmt.Sprintf("Diff: %d file(s) changed, +%d/-%d lines", files, added, removed))
	}
	return b.String()
}

func stepLabel(step string) string {
	if step == "" {
		return "(start)"
	}
	return step
}

// formatScopeContract summarizes a Planner's scope_contract (doc03:
// acceptance_criteria/in_scope_paths/non_goals, each a string list) —
// listed, not just counted, since these are typically short and are
// exactly the content a human watching the PR/issue most wants to see.
func formatScopeContract(sc map[string]any) string {
	var b strings.Builder
	b.WriteString("Scope:")
	for _, key := range []string{"acceptance_criteria", "in_scope_paths", "non_goals"} {
		items, ok := sc[key].([]any)
		if !ok || len(items) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n- %s: %s", key, joinStrings(items))
	}
	return b.String()
}

// formatFindings summarizes a Reviewer's findings (doc03:
// description/location/scope_classification/severity per finding).
func formatFindings(findings []any) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Findings (%d):", len(findings))
	for _, f := range findings {
		fm, ok := f.(map[string]any)
		if !ok {
			continue
		}
		severity, _ := fm["severity"].(string)
		scope, _ := fm["scope_classification"].(string)
		description, _ := fm["description"].(string)
		location, _ := fm["location"].(string)
		fmt.Fprintf(&b, "\n- [%s/%s] %s (%s)", severity, scope, description, location)
	}
	return b.String()
}

func joinStrings(items []any) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "; ")
}

// summarizeDiff counts files/added/removed lines from a `git diff --cached`
// unified diff (internal/activities/harness's commitWorktreeChanges is
// what produces the "diff" context field this reads) — deliberately not
// the full diff text itself (doc08: "not the full diff dumped into a
// comment, which is noise for a human skimming an issue thread").
func summarizeDiff(diff string) (files, added, removed int) {
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			files++
		case strings.HasPrefix(line, "+++ "), strings.HasPrefix(line, "--- "):
			// file header, not a content line — must be checked before the
			// generic +/- prefix cases below, which would otherwise
			// miscount these as added/removed lines.
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return files, added, removed
}
