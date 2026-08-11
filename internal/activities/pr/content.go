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
	scopeContract, _ := in.Context["scope_contract"].(map[string]any)
	findings, _ := in.Context["findings"].([]any)
	branch, _ := in.Context["branch"].(string)

	title = prTitle(taskDescription, in.RunID)

	var b strings.Builder
	fmt.Fprintf(&b, "## Overview\n\n%s\n", orFallback(taskDescription, "No task description was recorded for this Run."))
	fmt.Fprintf(&b, "\n## Intention\n\n%s\n", orFallback(assessment, "No plan assessment was recorded for this Run."))
	fmt.Fprintf(&b, "\n## Changes\n\n%s\n", changesSummary(diffStat, findings))
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

// changesSummary is the per-file breakdown (git's own --stat output,
// monospaced) plus, when a review happened, the Reviewer's own
// findings — already a brief, file-referencing description of what
// changed and why (doc03's findings schema: description + location per
// item), produced by an agent call this Activity doesn't need to repeat.
// Reusing it here instead of dumping the diff content itself is Rule 1
// (don't re-derive/re-render what a prior step already produced) applied
// to "what changed," not just "how much changed."
func changesSummary(diffStat string, findings []any) string {
	var b strings.Builder
	if diffStat == "" {
		b.WriteString("Diff summary unavailable.")
	} else {
		fmt.Fprintf(&b, "```\n%s```", diffStat)
	}
	if len(findings) > 0 {
		fmt.Fprintf(&b, "\n\nWhat changed:\n%s", conductor.FormatFindings(findings))
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
