package harness

import (
	"encoding/json"
	"regexp"
	"strings"
)

var fencedJSONBlock = regexp.MustCompile("(?s)```json\\s*(.*?)\\s*```")

// extractJSONBlock finds a fenced ```json code block in text and parses
// it as a JSON object. Falls back to treating the whole trimmed text as
// JSON if no fenced block is present, in case the harness ignored the
// fencing instruction but still returned bare JSON. Returns ok=false
// (routed by the caller to malformed_output, never silently retried —
// doc 03) if neither parses.
func extractJSONBlock(text string) (map[string]any, bool) {
	candidate := text
	if m := fencedJSONBlock.FindStringSubmatch(text); m != nil {
		candidate = m[1]
	}
	candidate = strings.TrimSpace(candidate)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(candidate), &parsed); err != nil {
		return nil, false
	}
	return parsed, true
}

// scalarString extracts a plain string from a parsed JSON value, also
// accepting a single-element array containing one. Found live: a real
// model (gpt-5-mini, via the copilot CLI) returned {"verdict": ["approved"]}
// instead of {"verdict": "approved"} for an enum field declared in
// output_schema as ["approved", "changes_required"] — a defensible
// reading of "here's the array of choices" as "wrap your choice in an
// array" despite the prompt's instructions to the contrary (since fixed
// to be more explicit, but this is cheap, permanent insurance against
// the same ambiguity tripping up some other model). A multi-element
// array, an empty array, or any other shape still returns ok=false —
// this tolerates one specific, now-confirmed real quirk, not "guess at
// whatever shape shows up."
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
