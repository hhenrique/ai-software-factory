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

	"factory/internal/activities/cmderr"
)

// codexAdapter invokes the Codex CLI (`codex exec`) non-interactively.
//
// Reviewer-role invocation confirmed live; Coder/Planner still
// unverified (no API credits available in this environment — see
// internal/activities/harness's package-level notes/commit history).
// Flags otherwise taken from `codex exec --help` and this machine's own
// ~/.codex/config.toml (which already has model_reasoning_effort set,
// confirming that's a real config key, not guessed).
//
// Known open issue, deliberately left unfixed (recorded here rather than
// only in a chat log, per the same "found live" convention as
// --skip-git-repo-check below): a real Coder-role (execute step) call
// failed with exit status 1, stderr "Reading additional input from
// stdin...", no further detail. Investigated, not resolved:
//
//   - `codex exec --help` documents this banner as expected, harmless
//     behavior — "If stdin is piped and a prompt is also provided, stdin
//     is appended as a `<stdin>` block" — and since this process's stdin
//     is always non-interactive, every invocation prints it, success or
//     failure. It is not itself the cause.
//   - Reproduced this adapter's exact invocation shape live multiple
//     times (both sandbox modes, a throwaway repo and a real worktree,
//     the actual failing Task's real prompt text) and every attempt
//     succeeded. Not a deterministic bug in how args/flags/stdin are
//     set up here, as far as live reproduction could show.
//   - An older failed Task's stderr (from before --skip-git-repo-check
//     existed) shows a different, already-fixed cause with the same
//     banner: "Not inside a trusted directory and --skip-git-repo-check
//     was not specified." The still-open failure postdates that fix and
//     has no comparable follow-up text, so it isn't the same bug.
//
// Best current guess: a transient CLI/API hiccup, not a fixable
// invocation bug — logged here rather than chased further at live API
// cost without a reproduction. internal/conductor/workflow.go's
// ActivityOptions sets RetryPolicy: MaximumAttempts: 1 for every
// Activity, with a comment noting that's "reserved for future
// infra-transient-failure handling" — if this recurs, a bounded
// Activity-level retry specifically for harness.invoke (not every
// Activity) is the natural next step, not another change here.
type codexAdapter struct{}

func (codexAdapter) invoke(ctx context.Context, inv invocation) (invocationResult, error) {
	// -o/--output-last-message writes just the final agent message to a
	// file — used instead of parsing the --json event stream for text,
	// since that stream's exact shape isn't verified live yet and this
	// flag is simple, documented, and doesn't depend on it.
	lastMsgFile := filepath.Join(inv.WorktreePath, ".factory-codex-last-message.txt")
	defer os.Remove(lastMsgFile)

	// workspace-write for a Coder-role call (real edits); read-only for
	// Planner (doc03: real repo access to draft a plan against, but never
	// to edit — backed by harness.Invoke's post-call git-status check,
	// not trusted alone). Reviewer never declares worktree_path at all,
	// so it never reaches this adapter with a real WorktreePath either
	// way.
	sandbox := "workspace-write"
	if inv.ReadOnly {
		sandbox = "read-only"
	}
	args := []string{
		"exec",
		"--json",
		"--sandbox", sandbox,
		// Reviewer calls run with a harmless temp-dir cwd (doc03: it
		// judges an already-produced diff, never touches the worktree —
		// see harness.go's hasWorktree check), which isn't a git repo at
		// all. Codex's own trust check refuses to run outside one without
		// this flag — found live (Reviewer role): "Not inside a trusted
		// directory and --skip-git-repo-check was not specified." Applied
		// unconditionally, including for Planner/Coder calls that do run
		// in a real worktree: the factory's own scope enforcement (doc03's
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
		return invocationResult{}, cmderr.Wrap("codex", err, cmderr.Stderr(err))
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
