package conductor

import (
	"fmt"
	"strings"

	"go.temporal.io/sdk/workflow"

	"factory/internal/workflowdef"
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
//
// Not every transition posts — see shouldPostComment. A human watching
// the issue wants the agents' results and to know when it's their turn,
// not routing plumbing (provision starting, a tool step passing, a
// resume/cancel bookkeeping transition).
func postTrackerComments(ctx workflow.Context, sourceRef SourceRef, runContext map[string]any, ev TransitionEvent) {
	if !shouldPostComment(ev) {
		return
	}
	body := formatTransitionComment(ev)
	prURL, _ := runContext["pr_url"].(string)

	if prURL != "" {
		postTrackerComment(ctx, ev, TrackerCommentInput{
			RunID: ev.RunID, TargetKind: "github_pr", TargetRef: prURL, Body: body,
		})
	}
	if sourceRef.Kind != "" {
		issueBody := body
		// A human reading the issue can't see the PR unless we say where
		// it is — the diff/findings they'd actually act on live there,
		// not in the issue thread. Added whenever this transition lands
		// on any terminal-ish state (REVIEW_PENDING included — see
		// shouldPostComment), not just REVIEW_PENDING specifically: a
		// Run that reaches COMPLETED or FAILED is just as much "the
		// human needs to know, and here's where to look" as one parked
		// waiting on a decision.
		if workflowdef.IsTerminalState(ev.ToStep) && prURL != "" {
			issueBody += "\n\nPR: " + prURL
		}
		postTrackerComment(ctx, ev, TrackerCommentInput{
			RunID: ev.RunID, TargetKind: sourceRef.Kind, TargetRef: sourceRef.Ref, Body: issueBody,
		})
	}
}

// shouldPostComment is docs/08's curated mirror content: "leave only the
// interactions with the agents... and any human pending action" — plus a
// Run's own final result, the same idea taken to its natural end: an
// agent step's own result (ev.AgentStep, set by RunWorkflow only for the
// transition right after a real Planner/Coder/Reviewer Activity call
// succeeded, malformed or not), or landing on any of doc01's terminal
// states (REVIEW_PENDING for any reason — an escalate verdict, malformed
// output, a budget/harness-limit exhaustion — or the Run's true end:
// COMPLETED, FAILED, CANCELLED). Never routing plumbing: a tool step
// passing, provision starting, or the resume bookkeeping transition out
// of REVIEW_PENDING back into a step.
func shouldPostComment(ev TransitionEvent) bool {
	return ev.AgentStep || workflowdef.IsTerminalState(ev.ToStep)
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

// FormatEventContent renders a transition's Produced fields (assessment,
// verdict, rationale, scope_contract, findings, a diff summary) into a
// compact human-readable block — the same content formatTransitionComment
// posts externally (docs/08), exported so a control-plane surface
// (internal/inbox, internal/backlog) can show the same substance for "why
// is this Run stuck" without needing Temporal's raw history to find it —
// doc04's "full trace/replay per Run" is non-negotiable even for a
// minimal build. "" if produced is empty or carries none of these
// recognized fields.
//
// assessment/rationale are doc03's "structured tracking content per
// role" (Planner assessment, Coder root-cause/rationale, Reviewer
// reasoning) — optional narrative fields a step's output_schema can ask
// for alongside its required routing fields (verdict, findings, ...).
// Rendering them here is unconditional on their presence in produced, not
// on which step produced it: nothing here needs to know which role ran:
// an older Workflow Definition whose output_schema doesn't declare them
// just never has the key, and this renders exactly as before.
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

	// A human's resume hint/justification leads everything else — it's
	// context a person gave *before* whatever this transition represents
	// (a plan approval's "here's why," a request-changes note, an
	// ordinary Inbox resume hint), not a byproduct of it. See
	// conductor.RunWorkflow's REVIEW_PENDING resume branch for why this
	// is persisted at all rather than only merged into live runContext.
	if hint, _ := produced["human_hint"].(string); hint != "" {
		writeSection("Human note: " + hint)
	}
	// Assessment leads the rest (context before the decision it informed
	// — a Planner's understanding of the task and its plan, read before
	// the verdict that came out of it).
	if assessment, _ := produced["assessment"].(string); assessment != "" {
		writeSection("Assessment: " + assessment)
	}
	if v, _ := produced["verdict"].(string); v != "" {
		writeSection("Verdict: " + v)
	}
	// Rationale follows the verdict it justifies — a Coder's reasoning
	// for dispute/address/out_of_scope on a review finding (doc03: "plus
	// reasoning text when verdict: dispute... retained and shown to the
	// Reviewer in the next round").
	if rationale, _ := produced["rationale"].(string); rationale != "" {
		writeSection("Rationale: " + rationale)
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
// Tolerant of a harness that doesn't follow that shape exactly — found
// live: a real Reviewer call returned {message, severity} instead of
// {description, location, scope_classification, severity} (the review
// step's output_schema declared findings as a bare "array" with no
// example item shape to follow — since fixed in the workflow YAML, but
// this still degrades gracefully rather than rendering an empty
// "[medium/]  ()" line if a harness ever deviates again). A finding with
// no usable text at all (neither field present) is dropped rather than
// rendered as a blank line.
func formatFindings(findings []any) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Findings (%d):", len(findings))
	for _, f := range findings {
		fm, ok := f.(map[string]any)
		if !ok {
			continue
		}
		text, _ := fm["description"].(string)
		if text == "" {
			text, _ = fm["message"].(string)
		}
		if text == "" {
			continue
		}

		var tags []string
		if severity, ok := scalarString(fm["severity"]); ok && severity != "" {
			tags = append(tags, severity)
		}
		if scope, ok := scalarString(fm["scope_classification"]); ok && scope != "" {
			tags = append(tags, scope)
		}
		line := text
		if len(tags) > 0 {
			line = "[" + strings.Join(tags, "/") + "] " + line
		}
		if location, _ := fm["location"].(string); location != "" {
			line += " (" + location + ")"
		}
		fmt.Fprintf(&b, "\n- %s", line)
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

// scalarString extracts a plain string from a parsed JSON value, also
// accepting a single-element array containing one — found live: a real
// model (gpt-5-mini, via the copilot CLI) wrapped enum-field values
// (verdict, and here severity/scope_classification) in a one-element
// array instead of writing the bare string doc03's schema asks for.
// internal/activities/harness has the identical helper (for the same
// reason, applied to Produced["verdict"]) — not shared across packages
// for one five-line function; see that copy's doc comment for the full
// story.
func scalarString(v any) (s string, ok bool) {
	if s, ok := v.(string); ok {
		return s, true
	}
	if arr, ok := v.([]any); ok && len(arr) == 1 {
		if s, ok := arr[0].(string); ok {
			return s, true
		}
	}
	return "", false
}
