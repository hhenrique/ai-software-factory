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
