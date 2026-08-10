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

	// HarnessLimits caps cumulative token spend per (harness, model,
	// effort) combination for the whole Run, decoupled from which
	// role/step uses that combination — e.g. claude-code/sonnet/high can
	// have a stricter cap than claude-code/sonnet/low, shared across
	// every step that happens to use it, regardless of role. Keyed by
	// internal/harnesslimits.Key(harness, model, effort); a combination
	// absent from the map has no limit. Resolved once by whatever starts
	// the Run (internal/harnesslimits.ParseEnv) — never looked up inside
	// RunWorkflow itself, which must stay a deterministic function of its
	// input.
	HarnessLimits map[string]int

	// RoleAssignments is the resolved harness/model/params triad for
	// every role Definition.Roles declares, keyed by role name — the
	// Worker currently assigned to play that role for this Workflow, at
	// the moment the Run was submitted. Resolved once by whatever starts
	// the Run (internal/roleassignment.Resolve) for exactly the same
	// determinism reason as HarnessLimits: Definition.Roles itself is
	// just role *names* now (docs/03), no longer harness/model/params, so
	// this is the only source roleConfig has for that data inside
	// RunWorkflow.
	RoleAssignments map[string]workflowdef.Role

	// SourceRef identifies the Task's source (docs/08's source-side
	// Tracker target) — Kind "" means no known source, so recordTransition
	// posts no source-side comment. Resolved once by whatever starts the
	// Run (internal/taskintake.Submit), same determinism reason as Repo/
	// HarnessLimits/RoleAssignments above.
	SourceRef SourceRef
}

// SourceRef mirrors internal/backlog.SourceRef's shape without importing
// that package — internal/backlog already imports conductor (for
// ActivityInput/ActivityOutput), so the dependency can only run one way,
// same reason RunInput.Repo is its own conductor.Repo rather than
// internal/repositories.Repository.
type SourceRef struct {
	Kind string // "github_issue" | "aha_idea" | ""
	Ref  string
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

// HumanDecision is the payload of HumanDecisionSignalName — how a human
// resumes or terminates a Run parked at REVIEW_PENDING. Doc 01 says a
// human "resumes a Run from REVIEW_PENDING with a hint" but doesn't
// specify the resume mechanics; this is the resolution: the human (or
// whatever control-plane surface acts on their behalf) names the step to
// resume at directly, since only they/it know where the escalation should
// continue from.
type HumanDecision struct {
	// Action is "resume" or "cancel".
	Action string

	// ResumeStepID is required when Action is "resume" — the step id to
	// continue the Run at. Not validated against the Definition here; an
	// invalid id surfaces the same way any other unknown step id does
	// (RunWorkflow's step lookup).
	ResumeStepID string

	// Hint is merged into the Run's context as "human_hint" on resume,
	// available to any step that declares it in `context:` (or any tool
	// step, which gets full context regardless).
	Hint string
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

// TransitionEvent is the payload for RecordEventActivityName — one
// structured record per step transition (docs/01: Task ID, Run ID,
// from/to-state, timestamp, token delta, tool-calls-made). "State" here
// is the conductor's step id; see RecordEventActivityName's doc comment
// for why this isn't a Workflow Definition step.
type TransitionEvent struct {
	RunID    string
	Workflow string

	// FromStep/ToStep are step ids, or "" for FromStep on the very first
	// event (Run start) and a terminal state name for ToStep on the last.
	FromStep string
	ToStep   string

	// StepID is the step whose Activity call produced this transition;
	// "" for the initial Run-start event, which precedes any Activity call.
	StepID        string
	AttemptNumber int
	TokenDelta    int
	ActivityCalls int

	// FailureReason is set only on the FAILED transition recordFailure
	// emits for a hard Run failure (an Activity error, an unroutable
	// outcome, ...) — "" for every ordinary transition. The human-readable
	// error text, so a control-plane surface (docs/04's Work section) can
	// show *why* a Run failed, not just that it did.
	FailureReason string

	// Outcome is the routing signal that produced this transition — an
	// agent step's verdict (e.g. "escalate", "changes_required"), a tool
	// step's result (e.g. "pass", "fail"), or a synthetic label for a
	// transition with no Activity call of its own ("budget_exhausted",
	// "malformed_output"). "" where no single outcome applies (Run start,
	// resume, cancel).
	Outcome string

	// AgentStep is true only for the transition immediately following a
	// type: agent step's own successful (non-malformed) Activity call —
	// i.e. an actual Planner/Coder/Reviewer result, never a tool step's
	// routing, a budget-exhausted/harness-limit synthetic transition (no
	// Activity call happened), or the Run-start/resume/cancel bookkeeping
	// transitions. Not persisted to run_events (internal/eventlog only
	// extracts specific columns) — purely a same-process signal for
	// postTrackerComments' filtering (docs/08: "leave only the
	// interactions with the agents... and any human pending action" —
	// the ToStep == REVIEW_PENDING check is the other half of that rule).
	AgentStep bool

	// Produced is the triggering Activity call's Produced fields — nil
	// for transitions with no Activity call of their own. Persisted
	// verbatim (see internal/eventlog) so a control-plane surface
	// (internal/inbox, internal/backlog) can render the same
	// verdict/scope_contract/findings/diff content doc08's tracker mirror
	// already formats for external posting (see FormatEventContent),
	// without needing Temporal's raw history to find it — doc04's "full
	// trace/replay per Run" is non-negotiable even for a minimal build.
	Produced map[string]any
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
