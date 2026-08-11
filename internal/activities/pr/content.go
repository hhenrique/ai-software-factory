package pr

import (
	"fmt"
	"strings"

	"factory/internal/conductor"
)

// buildPRContent renders a real title and body from whatever the Run has
// already produced by CREATE_PR time — no agent call, per Rule 2: every
// field read here is either already in in.Context (assembled by prior
// steps: task_description at Run start, assessment/scope_contract from
// `plan`, findings from the last `review` round) or on in.Repo (static
// config, no lookup). diffStat is the fresh base...HEAD `git diff --stat`
// CreateAndLink computed, not in.Context["diff"] (see its call site's
// comment for why) — a per-file breakdown, never the diff content itself:
// a human reviewing the PR already has that in GitHub's own Files Changed
// tab, so repeating it here is noise, not signal.
func buildPRContent(in conductor.ActivityInput, diffStat string) (title, body string) {
	taskDescription, _ := in.Context["task_description"].(string)
	assessment, _ := in.Context["assessment"].(string)
	changeSummary, _ := in.Context["change_summary"].(string)
	scopeContract, _ := in.Context["scope_contract"].(map[string]any)
	branch, _ := in.Context["branch"].(string)

	title = prTitle(taskDescription, in.RunID)

	var b strings.Builder
	fmt.Fprintf(&b, "## Overview\n\n%s\n", orFallback(taskDescription, "No task description was recorded for this Run."))
	fmt.Fprintf(&b, "\n## Intention\n\n%s\n", orFallback(assessment, "No plan assessment was recorded for this Run."))
	fmt.Fprintf(&b, "\n## Changes\n\n%s\n", changesSummary(changeSummary, diffStat))
	fmt.Fprintf(&b, "\n## Risk assessment\n\n%s\n", riskAssessment(scopeContract))
	fmt.Fprintf(&b, "\n## How to test\n\n%s\n", howToTest(in.Repo.TestCommand, branch))
	fmt.Fprintf(&b, "\n---\n_Opened automatically by the factory conductor for Run %s._", in.RunID)

	return title, b.String()
}

// prTitle uses the task description's first line — usually a GitHub issue
// title (see taskintake.fetchGitHubIssue) or a human's own short summary
// — capped so it stays a real title, not a dumped paragraph. Falls back
// to the previous generic title when there's no description to draw
// from (a free-text Task with no recorded description, or a Workflow
// Definition whose `plan`/intake step never populated task_description).
func prTitle(taskDescription, runID string) string {
	first := strings.TrimSpace(taskDescription)
	if idx := strings.IndexByte(first, '\n'); idx >= 0 {
		first = strings.TrimSpace(first[:idx])
	}
	if first == "" {
		return fmt.Sprintf("factory: automated change (run %s)", runID)
	}
	const maxLen = 72
	if len(first) > maxLen {
		first = strings.TrimSpace(first[:maxLen]) + "…"
	}
	return first
}

// changesSummary leads with change_summary — the Coder's own plain-
// language description of what it did, populated from the same call
// that produced the diff (execute/revise_verify/revise_review's
// output_schema; see internal/activities/harness/prompt.go's Coder-role
// note), not a second call re-reading the diff to describe it (Rule 1).
// The per-file breakdown (git's own --stat output) trails underneath as
// scale context, not the primary content — a human wants to know what
// changed before how many lines it took.
func changesSummary(changeSummary, diffStat string) string {
	var b strings.Builder
	b.WriteString(orFallback(changeSummary, "No change summary was recorded for this Run."))
	if diffStat != "" {
		fmt.Fprintf(&b, "\n\n```\n%s```", diffStat)
	}
	return b.String()
}

// riskAssessment is just the Planner's declared non-goals — the
// boundaries it flagged, not a restatement of the Reviewer's findings
// (those describe what changed, now under Changes, not what's risky
// about it; an approved review has no blocking findings left by
// CREATE_PR time anyway).
func riskAssessment(scopeContract map[string]any) string {
	if goals := stringList(scopeContract, "non_goals"); len(goals) > 0 {
		return "Declared non-goals:\n- " + strings.Join(goals, "\n- ")
	}
	return "No risk signals were surfaced during this Run."
}

func howToTest(testCommand, branch string) string {
	if testCommand == "" {
		return "No test command is configured for this repository."
	}
	var b strings.Builder
	if branch != "" {
		fmt.Fprintf(&b, "Check out `%s`, then run:\n\n", branch)
	}
	fmt.Fprintf(&b, "```\n%s\n```", testCommand)
	return b.String()
}

func stringList(m map[string]any, key string) []string {
	items, _ := m[key].([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func orFallback(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
