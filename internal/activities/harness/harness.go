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

	// ReadOnly is true for a Planner-role call (doc03: real repo access
	// to draft a plan against, but never to edit). Each adapter
	// translates this into its own harness's read-only mechanism — not
	// trusted alone; see harness.Invoke's post-call git-status check.
	ReadOnly bool
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
// (doc 03). For steps whose context declares worktree_path (Coder's
// execute/revise_verify/revise_review, and now Planner's plan — Reviewer
// still doesn't and shouldn't: it judges an already-produced diff, never
// touches the worktree), the harness runs with that as its cwd and gets
// real file access; this Activity computes the resulting diff itself via
// `git add -A` + `git diff --cached` after the harness runs (doc 03: "the
// conductor can apply" the diff — there's no separate DAG step for
// applying it, so it happens here, in the same Activity call that invoked
// the harness) rather than trying to parse a diff out of CLI text output.
// Steps without worktree_path run with a harmless temp-dir cwd and skip
// diff computation entirely — there's nothing to commit and nothing to
// look for.
//
// A Planner-role call is read-only instead: it gets worktree access to
// draft a real plan, but never to edit (doc03). Enforced twice — a
// harness-level flag (each adapter's own translation) as the first line
// of defense, then a deterministic git-status check after the call
// returns as the actual verified backstop, since a harness's own
// read-only claim isn't something this factory verifies at the source.
func (a *Activities) Invoke(ctx context.Context, in conductor.ActivityInput) (conductor.ActivityOutput, error) {
	ad, ok := adapters[in.Harness]
	if !ok {
		return conductor.ActivityOutput{}, fmt.Errorf("unknown harness %q", in.Harness)
	}

	worktreePath, hasWorktree := in.Context["worktree_path"].(string)
	hasWorktree = hasWorktree && worktreePath != ""
	cwd := worktreePath
	if !hasWorktree {
		cwd = os.TempDir()
	}
	readOnly := hasWorktree && in.Role == "planner"

	res, err := ad.invoke(ctx, invocation{
		WorktreePath: cwd,
		Prompt:       buildPrompt(in, hasWorktree),
		Model:        in.Model,
		Effort:       in.Params["effort"],
		ReadOnly:     readOnly,
	})
	if err != nil {
		// err already names which harness failed and why (each adapter's
		// own "claude:"/"codex:"/"copilot:" prefix) — no extra "harness:
		// invoke:" wrapping needed; that would just repeat what the from-
		// step field already records this Activity as.
		return conductor.ActivityOutput{}, err
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
			v, ok := scalarString(parsed["verdict"])
			if !ok || v == "" {
				out.Malformed = true
				return out, nil
			}
			out.Outcome = v
			parsed["verdict"] = v // normalize in Produced too — a single-element-array verdict (see scalarString) should read as a plain string everywhere downstream, not just for routing.
		}
	}

	if hasWorktree {
		if readOnly {
			violated, err := enforceReadOnlyWorktree(ctx, worktreePath)
			if err != nil {
				return conductor.ActivityOutput{}, err
			}
			if violated {
				if out.Produced == nil {
					out.Produced = map[string]any{}
				}
				// Surfaced to a human reviewing the plan (Pending
				// Approvals), not just logged — a harness that writes
				// during a read-only pass is real information about
				// whether it should keep playing this role at all
				// (doc03).
				out.Produced["read_only_violation"] = true
			}
		} else {
			diff, err := commitWorktreeChanges(ctx, worktreePath, in.StepID, in.AttemptNumber)
			if err != nil {
				return conductor.ActivityOutput{}, err
			}
			if diff != "" {
				if out.Produced == nil {
					out.Produced = map[string]any{}
				}
				out.Produced["diff"] = diff
			}
		}
	}

	return out, nil
}
