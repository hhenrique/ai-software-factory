package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os/exec"

	"factory/internal/activities/cmderr"
)

// copilotAdapter invokes the GitHub Copilot CLI (`copilot`) non-interactively.
//
// Confirmed live (real `copilot` CLI v1.0.78, both a plain prompt and an
// output_schema-style "respond with a fenced json block" prompt): the
// --output-format json JSONL event shape is
// {"type": "assistant.message", "data": {"content": "...", "outputTokens": N}}
// — nested under "data", not top-level, which is what the previous
// generic top-level key scan missed (found live, Reviewer role: every
// real call returned Produced: null, TokensUsed: 0 — the scan never
// matched anything, routing every call to malformed_output).
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
	if inv.ReadOnly {
		// Copilot has no real sandboxed read-only mode the way Claude
		// Code's "plan" mode or Codex's --sandbox read-only do — this is
		// an explicit denylist, only as complete as the tool names
		// enumerated here, which is exactly why harness.Invoke's post-call
		// git-status check exists as the real backstop (doc03) rather
		// than trusting this alone.
		args = append(args, "--deny-tool", "write", "--deny-tool", "shell")
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
		return invocationResult{}, cmderr.Wrap("copilot", err, cmderr.Stderr(err))
	}

	return invocationResult{
		FinalText:  finalTextFromJSONL(out),
		TokensUsed: bestEffortTokenCountFromJSONL(out),
	}, nil
}

// copilotEvent is one --output-format json JSONL line's relevant fields —
// only "assistant.message" events carry a complete (non-streaming-delta)
// turn's final content and its token count; every other event type
// (session.*, model.call_start, assistant.message_delta's partial
// chunks, mcp.*, ...) is irrelevant here and simply won't unmarshal
// meaningful Data fields, which is fine — the Type check below skips them.
type copilotEvent struct {
	Type string `json:"type"`
	Data struct {
		Content      string `json:"content"`
		OutputTokens int    `json:"outputTokens"`
	} `json:"data"`
}

// finalTextFromJSONL scans --output-format json's JSONL events for the
// last "assistant.message" event's content — the complete text of that
// turn. A multi-turn tool-use call emits one such event per turn
// (intermediate ones alongside tool requests, the final one once it's
// done); taking the last one is what "the final answer" means here, same
// principle as Codex's -o (last message) and Claude's single result
// object, just via a different mechanism since copilot has neither.
func finalTextFromJSONL(jsonl []byte) string {
	var last string
	for _, ev := range scanCopilotEvents(jsonl) {
		if ev.Type == "assistant.message" && ev.Data.Content != "" {
			last = ev.Data.Content
		}
	}
	return last
}

// bestEffortTokenCountFromJSONL sums outputTokens off every
// "assistant.message" event — an undercount by design (doc03: "a
// best-effort estimate, clearly flagged as such"): this schema doesn't
// expose an input-token count anywhere found so far, only output.
func bestEffortTokenCountFromJSONL(jsonl []byte) int {
	var total int
	for _, ev := range scanCopilotEvents(jsonl) {
		if ev.Type == "assistant.message" {
			total += ev.Data.OutputTokens
		}
	}
	return total
}

func scanCopilotEvents(jsonl []byte) []copilotEvent {
	var events []copilotEvent
	scanner := bufio.NewScanner(bytes.NewReader(jsonl))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var ev copilotEvent
		if json.Unmarshal(scanner.Bytes(), &ev) != nil {
			continue
		}
		events = append(events, ev)
	}
	return events
}
