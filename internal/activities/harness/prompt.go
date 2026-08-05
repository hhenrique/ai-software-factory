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
func buildPrompt(in conductor.ActivityInput) string {
	var b strings.Builder

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
			"containing an object matching this shape. Each key's value below is either an " +
			"array of allowed literal strings to choose one from, or a TYPE PLACEHOLDER " +
			"naming what kind of value to put there (e.g. \"object\" means put an actual " +
			"JSON object there, not the literal string \"object\"; \"array\" means put an " +
			"actual JSON array there):\n")
		b.Write(schemaJSON)
		b.WriteString("\n")
	}

	return b.String()
}
