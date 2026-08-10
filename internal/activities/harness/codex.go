package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// codexAdapter invokes the Codex CLI (`codex exec`) non-interactively.
//
// Reviewer-role invocation confirmed live; Coder/Planner still
// unverified (no API credits available in this environment — see
// internal/activities/harness's package-level notes/commit history).
// Flags otherwise taken from `codex exec --help` and this machine's own
// ~/.codex/config.toml (which already has model_reasoning_effort set,
// confirming that's a real config key, not guessed).
type codexAdapter struct{}

func (codexAdapter) invoke(ctx context.Context, inv invocation) (invocationResult, error) {
	// -o/--output-last-message writes just the final agent message to a
	// file — used instead of parsing the --json event stream for text,
	// since that stream's exact shape isn't verified live yet and this
	// flag is simple, documented, and doesn't depend on it.
	lastMsgFile := filepath.Join(inv.WorktreePath, ".factory-codex-last-message.txt")
	defer os.Remove(lastMsgFile)

	args := []string{
		"exec",
		"--json",
		"--sandbox", "workspace-write",
		// Planner/Reviewer calls run with a harmless temp-dir cwd (doc03:
		// they judge a task description or an already-produced diff,
		// never touch the worktree — see harness.go's hasWorktree check),
		// which isn't a git repo at all. Codex's own trust check refuses
		// to run outside one without this flag — found live (Reviewer
		// role): "Not inside a trusted directory and
		// --skip-git-repo-check was not specified." Applied
		// unconditionally, including for Coder-role calls that do run in
		// a real worktree: the factory's own scope enforcement (doc03's
		// contract) doesn't rely on Codex's own git-trust heuristic, so
		// there's nothing this bypasses that matters here.
		"--skip-git-repo-check",
		"-C", inv.WorktreePath,
		"-o", lastMsgFile,
	}
	if inv.Model != "" {
		args = append(args, "-m", inv.Model)
	}
	if inv.Effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+inv.Effort)
	}
	args = append(args, inv.Prompt)

	cmd := exec.CommandContext(ctx, "codex", args...)
	cmd.Dir = inv.WorktreePath
	jsonlOut, err := cmd.Output()
	if err != nil {
		return invocationResult{}, fmt.Errorf("codex: %w: %s", err, stderrOf(err))
	}

	finalText, err := os.ReadFile(lastMsgFile)
	if err != nil {
		return invocationResult{}, fmt.Errorf("codex: read last-message file: %w", err)
	}

	return invocationResult{
		FinalText:  string(finalText),
		TokensUsed: bestEffortCodexTokenCount(jsonlOut),
	}, nil
}

// bestEffortCodexTokenCount scans the --json event stream for any object
// carrying token usage and sums what it finds. Best-effort and
// intentionally lenient (doc 03 allows a "best-effort estimate, clearly
// flagged as such") rather than a strict parse against one assumed event
// shape, since that shape isn't verified live yet — a shape mismatch
// should degrade to an undercount, not an Activity failure.
func bestEffortCodexTokenCount(jsonl []byte) int {
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
		total += intField(usage, "input_tokens") + intField(usage, "output_tokens") +
			intField(usage, "cached_input_tokens")
	}
	return total
}

func intField(m map[string]any, key string) int {
	v, ok := m[key].(float64) // encoding/json decodes JSON numbers as float64
	if !ok {
		return 0
	}
	return int(v)
}
