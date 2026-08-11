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
// config, no lookup). diff is the fresh base...HEAD diff CreateAndLink
// computed, not in.Context["diff"] (see its call site's comment for why).
func buildPRContent(in conductor.ActivityInput, diff string) (title, body string) {
	taskDescription, _ := in.Context["task_description"].(string)
	assessment, _ := in.Context["assessment"].(string)
	scopeContract, _ := in.Context["scope_contract"].(map[string]any)
	findings, _ := in.Context["findings"].([]any)
	branch, _ := in.Context["branch"].(string)

	title = prTitle(taskDescription, in.RunID)

	var b strings.Builder
	fmt.Fprintf(&b, "## Overview\n\n%s\n", orFallback(taskDescription, "No task description was recorded for this Run."))
	fmt.Fprintf(&b, "\n## Intention\n\n%s\n", orFallback(assessment, "No plan assessment was recorded for this Run."))
	fmt.Fprintf(&b, "\n## Changes\n\n%s\n", changesSummary(diff))
	fmt.Fprintf(&b, "\n## Risk assessment\n\n%s\n", riskAssessment(scopeContract, findings))
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

func changesSummary(diff string) string {
	if diff == "" {
		return "Diff summary unavailable."
	}
	files, added, removed := conductor.SummarizeDiff(diff)
	return fmt.Sprintf("%d file(s) changed, +%d/-%d lines.", files, added, removed)
}

// riskAssessment surfaces exactly the risk-relevant subset of what a
// Planner/Reviewer already produced — non_goals (boundaries the Planner
// itself flagged) and any findings still in context (an approved review
// can still carry advisory, non-blocking findings — doc01: "out-of-scope,
// advisory — logged, does not gate"). Deliberately not the rest of
// scope_contract (acceptance_criteria/in_scope_paths already read under
// Intention via assessment) — repeating it here would be noise, not risk.
func riskAssessment(scopeContract map[string]any, findings []any) string {
	var parts []string
	if goals := stringList(scopeContract, "non_goals"); len(goals) > 0 {
		parts = append(parts, "Declared non-goals:\n- "+strings.Join(goals, "\n- "))
	}
	if len(findings) > 0 {
		parts = append(parts, conductor.FormatFindings(findings))
	}
	if len(parts) == 0 {
		return "No risk signals were surfaced during this Run."
	}
	return strings.Join(parts, "\n\n")
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
