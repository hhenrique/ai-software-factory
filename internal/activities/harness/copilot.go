package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

// copilotAdapter invokes the GitHub Copilot CLI (`copilot`) non-interactively.
//
// UNVERIFIED LIVE as of this writing (no API credits available in this
// environment to test against). Flags are taken from `copilot --help`;
// confirm end to end once credits exist, in particular the exact shape of
// --output-format json's JSONL events (finalTextFromJSONL below is a
// deliberately lenient/generic scan for exactly that reason — see its doc
// comment).
type copilotAdapter struct{}

func (copilotAdapter) invoke(ctx context.Context, inv invocation) (invocationResult, error) {
	args := []string{
		"-p", inv.Prompt,
		"-C", inv.WorktreePath,
		// Required for non-interactive mode — there's no terminal here to
		// approve tool use from (--help: "required for non-interactive
		// mode").
		"--allow-all",
		"--output-format", "json",
	}
	if inv.Model != "" {
		args = append(args, "--model", inv.Model)
	}
	if inv.Effort != "" {
		args = append(args, "--effort", inv.Effort)
	}

	cmd := exec.CommandContext(ctx, "copilot", args...)
	cmd.Dir = inv.WorktreePath
	out, err := cmd.Output()
	if err != nil {
		return invocationResult{}, fmt.Errorf("copilot: %w: %s", err, stderrOf(err))
	}

	return invocationResult{
		FinalText:  finalTextFromJSONL(out),
		TokensUsed: bestEffortTokenCountFromJSONL(out),
	}, nil
}

// finalTextFromJSONL scans --output-format json's JSONL events for the
// last one carrying assistant-facing text, checking a few plausible key
// names since the exact event schema isn't verified live yet — a
// generic, tolerant scan degrades to an empty result (routed to
// malformed_output by the caller for schema steps) rather than a parse
// panic if none of the guessed keys match reality.
func finalTextFromJSONL(jsonl []byte) string {
	var last string
	scanner := bufio.NewScanner(bytes.NewReader(jsonl))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var event map[string]any
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		for _, key := range []string{"content", "text", "message", "response"} {
			if s, ok := event[key].(string); ok && s != "" {
				last = s
			}
		}
	}
	return last
}

func bestEffortTokenCountFromJSONL(jsonl []byte) int {
	var total int
	scanner := bufio.NewScanner(bytes.NewReader(jsonl))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var event map[string]any
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		usage, ok := event["usage"].(map[string]any)
		if !ok {
			continue
		}
		total += intField(usage, "input_tokens") + intField(usage, "output_tokens")
	}
	return total
}
