// Package harness holds the real harness.invoke Activity — the one
// generic Activity every type: agent step dispatches to (see
// internal/conductor/registry.go). It normalizes input (doc 03: prompt
// construction from context fields), dispatches to one of the three
// supported CLI adapters by Role.Harness, and normalizes output back into
// the conductor's ActivityOutput contract regardless of which CLI ran.
package harness

import (
	"context"
	"fmt"
	"os"

	"factory/internal/conductor"
)

// Activities holds the dependencies the real harness.invoke Activity
// needs (none, today — kept as a struct for symmetry with the other real
// Activity sets so cmd/worker registers every one the same way).
type Activities struct{}

// Registrations maps the Activity name the conductor package's
// step-to-Activity mapping expects (see internal/conductor/registry.go)
// to this struct's method.
func (a *Activities) Registrations() map[string]any {
	return map[string]any{
		conductor.HarnessInvokeActivityName: a.Invoke,
	}
}

// invocation is the normalized input every adapter's invoke receives —
// harness-agnostic; each adapter translates it into its own CLI's flags.
type invocation struct {
	WorktreePath string
	Prompt       string
	Model        string
	Effort       string // canonical param name; "" if not set
}

// invocationResult is the normalized output every adapter's invoke
// returns.
type invocationResult struct {
	// FinalText is the harness's final assistant-facing text response —
	// used to extract a JSON block for steps with an output_schema.
	// Irrelevant for schema-less (diff-only) steps, where what matters is
	// whatever the harness actually wrote to worktree files.
	FinalText string

	// TokensUsed is a best-effort normalized token count (doc 03: "a
	// single consistent token count (or best-effort estimate, clearly
	// flagged as such)") — see each adapter for how it's derived from
	// that CLI's own reporting.
	TokensUsed int
}

// adapter is implemented once per supported harness CLI.
type adapter interface {
	invoke(ctx context.Context, inv invocation) (invocationResult, error)
}

// adapters is the harness-identifier -> adapter dispatch table.
var adapters = map[string]adapter{
	"claude-code": claudeAdapter{},
	"codex":       codexAdapter{},
	"copilot":     copilotAdapter{},
}

// Invoke normalizes input from in.Context (doc 03: this is where any
// harness-specific prompt construction lives — it must not leak into the
// conductor or the Workflow Definition, so buildPrompt/adapters are the
// only place it exists), dispatches to the Role's harness, and normalizes
// output back into ActivityOutput.
//
// For steps with an output_schema (Planner, Reviewer, coder_response),
// the harness is asked to emit a JSON block matching that shape, which is
// then parsed — unparseable output is Malformed, never silently retried
// (doc 03). For steps whose context declares worktree_path (the Coder's
// execute/revise_verify/revise_review — Planner and Reviewer don't and
// shouldn't: they judge a task description or an already-produced diff
// text, never edit files), the harness runs with that as its cwd and gets
// real file access; this Activity computes the resulting diff itself via
// `git add -A` + `git diff --cached` after the harness runs (doc 03: "the
// conductor can apply" the diff — there's no separate DAG step for
// applying it, so it happens here, in the same Activity call that invoked
// the harness) rather than trying to parse a diff out of CLI text output.
// Steps without worktree_path run with a harmless temp-dir cwd and skip
// diff computation entirely — there's nothing to commit and nothing to
// look for.
func (a *Activities) Invoke(ctx context.Context, in conductor.ActivityInput) (conductor.ActivityOutput, error) {
	ad, ok := adapters[in.Harness]
	if !ok {
		return conductor.ActivityOutput{}, fmt.Errorf("harness: invoke: unknown harness %q", in.Harness)
	}

	worktreePath, hasWorktree := in.Context["worktree_path"].(string)
	cwd := worktreePath
	if !hasWorktree || cwd == "" {
		cwd = os.TempDir()
	}

	res, err := ad.invoke(ctx, invocation{
		WorktreePath: cwd,
		Prompt:       buildPrompt(in),
		Model:        in.Model,
		Effort:       in.Params["effort"],
	})
	if err != nil {
		return conductor.ActivityOutput{}, fmt.Errorf("harness: invoke: %w", err)
	}

	out := conductor.ActivityOutput{TokensUsed: res.TokensUsed}

	if len(in.OutputSchema) > 0 {
		parsed, ok := extractJSONBlock(res.FinalText)
		if !ok {
			out.Malformed = true
			return out, nil
		}
		out.Produced = parsed

		// If the schema declares a verdict (every schema-driven step in
		// both reference Workflow Definitions does — it's what route()
		// looks up), a parsed-but-verdict-less response is just as
		// unusable for routing as unparseable JSON would be. Found live:
		// the JSON parsed fine but "verdict" was missing, silently
		// producing Outcome "" — route() then had no on: mapping for ""
		// and hard-errored instead of this routing to
		// on_malformed_output the way doc 03 intends.
		if _, wantsVerdict := in.OutputSchema["verdict"]; wantsVerdict {
			v, ok := parsed["verdict"].(string)
			if !ok || v == "" {
				out.Malformed = true
				return out, nil
			}
			out.Outcome = v
		}
	}

	if hasWorktree && worktreePath != "" {
		diff, err := commitWorktreeChanges(ctx, worktreePath, in.StepID, in.AttemptNumber)
		if err != nil {
			return conductor.ActivityOutput{}, fmt.Errorf("harness: invoke: %w", err)
		}
		if diff != "" {
			if out.Produced == nil {
				out.Produced = map[string]any{}
			}
			out.Produced["diff"] = diff
		}
	}

	return out, nil
}
