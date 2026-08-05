// Package conductor is the generic, data-driven Temporal workflow that
// walks a validated workflowdef.Definition's step graph. It is "Temporal
// Workflow code" in doc 05's sense — one generic walker, not a bespoke
// .go file per Workflow Definition and not a hand-rolled interpreter:
// Temporal remains the actual durable executor via workflow.ExecuteActivity.
package conductor

import "factory/internal/workflowdef"

// RunInput is everything RunWorkflow needs to execute one Run of a
// validated Workflow Definition against a Task.
type RunInput struct {
	Definition workflowdef.Definition

	// StartStepID overrides the entry point; defaults to
	// Definition.Steps[0].ID when empty (both reference definitions list
	// their entry step, "provision", first).
	StartStepID string

	// InitialContext seeds the Run's context map with Run-level inputs
	// (e.g. task description, repo path) available to every step from the
	// start, independent of any step's produced output.
	InitialContext map[string]any

	// FailVerifyUntilAttempt is threaded through to the stub
	// run.tests_lint_build Activity via RunParams so smoke-test scenarios
	// can deterministically control how many verify attempts fail before
	// passing — see internal/activities/stub.
	FailVerifyUntilAttempt int

	// Repo identifies which repository this Run targets — the minimum a
	// git-backed tool Activity (e.g. worktree.create) needs to reach it.
	// There's no persisted Repository entity yet (doc 04's Repositories
	// section is unbuilt), so today this is supplied directly by whatever
	// starts the Run.
	Repo Repo
}

// Repo is a slice of doc 04's Repository entity: just enough to clone and
// branch against a repo. Threaded from RunInput into every ActivityInput
// so any tool Activity that needs it doesn't have to re-derive it.
type Repo struct {
	Name          string
	CloneURL      string
	DefaultBranch string // empty means "resolve from origin/HEAD"

	// TestCommand is the repo's declared build/test/lint step (doc 04:
	// "Build / test / lint commands, consumed by the run.tests_lint_build
	// tool action"), a single shell command run in the Run's worktree.
	// One combined command rather than three separate fields — sequencing
	// build vs. test vs. lint is the repo's own tooling's job (e.g. its
	// Makefile), not the conductor's.
	TestCommand string
}

// RunResult is RunWorkflow's return value: the Run's terminal state plus
// enough of a trace to assert against in tests without querying Temporal's
// raw workflow history.
type RunResult struct {
	FinalState   string
	StepsVisited []string
	BudgetSpent  map[string]int

	// FinalContext is every Produced field accumulated across the Run's
	// steps by the time it reached a terminal state — e.g.
	// worktree.create's worktree_path/branch/clone_dir. Lets a caller
	// (control plane, tests) inspect real step output without querying
	// Temporal's raw workflow history.
	FinalContext map[string]any
}

// ActivityInput is the normalized input every Activity this package
// invokes receives, regardless of whether the step is type: tool or
// type: agent.
type ActivityInput struct {
	StepID  string
	Action  string // tool steps only
	Role    string // agent steps only
	Harness string // agent steps only; resolved from Definition.Roles[Role]
	Model   string // agent steps only; resolved from Definition.Roles[Role]

	// Params carries harness-invocation parameters beyond the model
	// itself (e.g. "effort"), copied from Definition.Roles[Role].Params.
	// Canonical, adapter-agnostic keys — see workflowdef.Role's doc
	// comment for the translation contract.
	Params map[string]string

	// OutputSchema is the step's declared output_schema (nil if none) —
	// an agent-step harness adapter needs this to know whether to expect
	// a structured verdict/findings response or just a diff (doc 03: no
	// schema required for the Coder's initial EXECUTING pass).
	OutputSchema map[string]any

	// Context carries the Run's accumulated context available to this
	// step. For type: agent steps, only the fields declared via
	// `context:` — the conductor computes this diff deterministically so
	// the agent never has to re-derive information a prior step already
	// produced (CLAUDE.md's cardinal rule 1, which is about token cost).
	// For type: tool steps, the full accumulated context: doc 02
	// documents `context:` as an agent-steps-only field precisely because
	// tool Activities aren't token-constrained (no LLM call) and may need
	// infrastructure data no step declared, e.g. run.tests_lint_build
	// needs worktree_path from provision without provision's step id or
	// output shape being wired through every intermediate step's
	// `context:` list.
	Context map[string]any

	// AttemptNumber is this call's 1-based count within the step's budget
	// loop (1 if the step has no budget).
	AttemptNumber int

	// RunID identifies the Run this Activity call belongs to — Temporal's
	// own WorkflowExecution.ID, which doc 05 maps 1:1 onto a Run. Used to
	// key per-Run resources (e.g. worktree.create's worktree directory
	// and branch name) so retries (new Runs, never mutated ones) never
	// collide with a prior attempt.
	RunID string

	// Repo identifies which repository this Run targets, copied from
	// RunInput.Repo.
	Repo Repo

	// RunParams carries Run-level parameters unrelated to step context,
	// e.g. the deterministic verify-failure threshold used by the stub
	// test-runner Activity.
	RunParams map[string]any
}

// ActivityOutput is the normalized output every Activity this package
// invokes returns.
type ActivityOutput struct {
	// Outcome is the key looked up in the step's `on:` map — e.g.
	// "pass"/"fail" for verify, "proceed"/"reject"/"escalate" for plan.
	// Ignored for steps that use `next:` instead of `on:`.
	Outcome string

	// Malformed marks an agent step's output as unparseable against its
	// output_schema — routes via on_malformed_output, never silently
	// retried (doc 03).
	Malformed bool

	// Produced fields are merged into the Run's context map, becoming
	// available to every downstream step per rule 5.
	Produced map[string]any

	// TokensUsed is this call's normalized token count (see doc 03's
	// harness-adapter token-accounting requirement) — accumulated per
	// budget and checked against max_tokens via BudgetGate.
	TokensUsed int
}
