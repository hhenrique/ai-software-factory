package conductor

import "factory/internal/workflowdef"

// HarnessInvokeActivityName is the single generic Activity every
// type: agent step dispatches to this slice — no real per-harness routing
// yet (that's a future harness-adapter dispatch layer), though
// ActivityInput.Harness is already pre-wired for it.
const HarnessInvokeActivityName = "harness.invoke"

// RecordEventActivityName is the Activity RunWorkflow calls directly (not
// via activityNameFor's step dispatch — this isn't a Workflow Definition
// step, it's conductor-internal infrastructure) after every step
// transition, to persist a structured event (docs/01: "every state
// transition emits a structured event"). See internal/eventlog.
const RecordEventActivityName = "conductor.record_event"

// HumanDecisionSignalName is the signal RunWorkflow blocks on when a Run
// reaches REVIEW_PENDING (doc 05: signal-wait, preferred over polling).
// See HumanDecision.
const HumanDecisionSignalName = "human_decision"

// activityNameFor maps a step to the Temporal Activity name that executes
// it: a tool step's own action identifier (e.g. "worktree.create"), or
// HarnessInvokeActivityName for every agent step. Doc 05: "the conductor
// does not need a different execution primitive for agent vs. tool
// steps, only a different implementation inside the Activity."
func activityNameFor(step workflowdef.Step) string {
	if step.Type == workflowdef.StepTypeAgent {
		return HarnessInvokeActivityName
	}
	return step.Action
}
