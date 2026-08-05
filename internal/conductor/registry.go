package conductor

import "factory/internal/workflowdef"

// HarnessInvokeActivityName is the single generic Activity every
// type: agent step dispatches to this slice — no real per-harness routing
// yet (that's a future harness-adapter dispatch layer), though
// ActivityInput.Harness is already pre-wired for it.
const HarnessInvokeActivityName = "harness.invoke"

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
