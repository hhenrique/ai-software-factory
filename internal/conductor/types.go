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
}

// RunResult is RunWorkflow's return value: the Run's terminal state plus
// enough of a trace to assert against in tests without querying Temporal's
// raw workflow history.
type RunResult struct {
	FinalState   string
	StepsVisited []string
	BudgetSpent  map[string]int
}

// ActivityInput is the normalized input every Activity this package
// invokes receives, regardless of whether the step is type: tool or
// type: agent.
type ActivityInput struct {
	StepID  string
	Action  string // tool steps only
	Role    string // agent steps only
	Harness string // agent steps only; resolved from Definition.Roles[Role]

	// Context carries only the fields the step declared via `context:` —
	// the conductor computes this diff deterministically so the agent
	// never has to re-derive information a prior step already produced
	// (see CLAUDE.md's cardinal rule 1).
	Context map[string]any

	// AttemptNumber is this call's 1-based count within the step's budget
	// loop (1 if the step has no budget).
	AttemptNumber int

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
