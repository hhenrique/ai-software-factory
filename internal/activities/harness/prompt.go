package harness

import (
	"encoding/json"
	"strings"

	"factory/internal/conductor"
)

// buildPrompt assembles the task prompt from the step's declared context
// fields (doc 03: "this is where any harness-specific prompt construction
// lives" — the harness-agnostic part of it, shared by every adapter;
// each adapter still owns translating model/effort into its own flags).
//
// hasWorktree mirrors Invoke's own cwd decision (doc 03: Planner/Reviewer
// "judge a task description or an already-produced diff and never touch
// the worktree"). Without an explicit note, an agentic harness given no
// real repo tends to go look for one anyway, find an empty temp dir, and
// escalate the resulting confusion as if the task itself were blocked —
// this tells it up front that the absence of a checked-out repo is
// expected, not a problem to report.
func buildPrompt(in conductor.ActivityInput, hasWorktree bool) string {
	var b strings.Builder

	if !hasWorktree {
		b.WriteString("You do not have access to this repository's files in this environment — there is " +
			"no working directory to explore, and no shell/file/search tool will find a real checkout. " +
			"This is expected, not an error: base your assessment entirely on the information given below. " +
			"Do not attempt to read, search, list, or clone files, and do not treat the absence of a " +
			"checked-out repository itself as a reason to reject or escalate.\n\n")
	}

	if task, ok := in.Context["task_description"].(string); ok && task != "" {
		b.WriteString(task)
		b.WriteString("\n\n")
	}

	if diff, ok := in.Context["failing_tests_diff"].(string); ok && diff != "" {
		b.WriteString("The previous attempt failed verification with this output:\n\n")
		b.WriteString(diff)
		b.WriteString("\n\nFix the issue, then stop.\n\n")
	}

	if scope, ok := in.Context["scope_contract"]; ok {
		if scopeJSON, err := json.MarshalIndent(scope, "", "  "); err == nil {
			b.WriteString("Scope contract (stay within this):\n")
			b.Write(scopeJSON)
			b.WriteString("\n\n")
		}
	}

	if diff, ok := in.Context["diff"].(string); ok && diff != "" {
		b.WriteString("Diff to review:\n\n")
		b.WriteString(diff)
		b.WriteString("\n\n")
	}

	if findings, ok := in.Context["findings"]; ok {
		if findingsJSON, err := json.MarshalIndent(findings, "", "  "); err == nil {
			b.WriteString("Review findings to respond to:\n")
			b.Write(findingsJSON)
			b.WriteString("\n\n")
		}
	}

	if len(in.OutputSchema) > 0 {
		schemaJSON, _ := json.MarshalIndent(in.OutputSchema, "", "  ")
		b.WriteString("Respond with a fenced ```json code block (and nothing else after it) " +
			"containing an object matching this shape. Each key's value below tells you what " +
			"to put there, using one of these conventions:\n" +
			"- An array of literal strings, e.g. [\"approved\", \"changes_required\"], is an " +
			"ENUM: pick exactly ONE of them and write it as a plain JSON string value — e.g. " +
			"\"verdict\": \"approved\" — never as an array, even though the enum itself is " +
			"shown as one.\n" +
			"- A bare string naming a JSON type (\"object\", \"array\", \"string\") is a TYPE " +
			"PLACEHOLDER: put an actual value of that type there, not the literal placeholder " +
			"text — e.g. \"object\" means put a real JSON object there.\n" +
			"- An array containing exactly one example object is a SHAPE TEMPLATE: produce an " +
			"array of zero or more objects shaped like that example (same field names — do " +
			"not invent your own), each field value itself following these same rules (so an " +
			"enum field inside the example is still a single string per object, not an " +
			"array).\n")
		b.Write(schemaJSON)
		b.WriteString("\n")
	}

	return b.String()
}
