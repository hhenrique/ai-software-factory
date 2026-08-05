package conductor_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"

	"factory/internal/conductor"
	"factory/internal/workflowdef"
)

// placeholderActivity is only used to register a name with the test
// environment so OnActivity(name, ...) can mock calls dispatched by
// string — RunWorkflow always calls workflow.ExecuteActivity with a name,
// never a function reference (see registry.go), so every Activity name it
// might dispatch to needs a registered stand-in here.
func placeholderActivity(ctx context.Context, in conductor.ActivityInput) (conductor.ActivityOutput, error) {
	return conductor.ActivityOutput{}, nil
}

// placeholderRecordEvent stands in for RecordEventActivityName, whose
// signature (TransitionEvent, not ActivityInput/ActivityOutput) differs
// from every step Activity — recordTransition swallows a call to an
// unregistered/unmocked activity as a logged warning rather than failing
// the Run (see workflow.go), so registering this by default means tests
// succeed because the call actually resolved, not by accident of that
// error-swallowing.
func placeholderRecordEvent(ctx context.Context, ev conductor.TransitionEvent) error {
	return nil
}

func newTestEnv(t *testing.T) *testsuite.TestWorkflowEnvironment {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(conductor.RunWorkflow)
	for _, name := range []string{
		"worktree.create",
		"run.tests_lint_build",
		"pr.create_and_link",
		conductor.HarnessInvokeActivityName,
	} {
		env.RegisterActivityWithOptions(placeholderActivity, activity.RegisterOptions{
			Name:                          name,
			DisableAlreadyRegisteredCheck: true,
		})
	}
	env.RegisterActivityWithOptions(placeholderRecordEvent, activity.RegisterOptions{
		Name:                          conductor.RecordEventActivityName,
		DisableAlreadyRegisteredCheck: true,
	})
	return env
}

func mustParseDependencyBumpMinimal(t *testing.T) workflowdef.Definition {
	t.Helper()
	def, err := workflowdef.Parse(workflowdef.DependencyBumpMinimalYAML)
	require.NoError(t, err)
	require.Empty(t, workflowdef.Validate(def))
	return *def
}

func TestRunWorkflowHappyPath(t *testing.T) {
	env := newTestEnv(t)
	def := mustParseDependencyBumpMinimal(t)

	env.OnActivity("worktree.create", mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{}, nil).Once()
	env.OnActivity(conductor.HarnessInvokeActivityName, mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{}, nil).Once()
	env.OnActivity("run.tests_lint_build", mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{Outcome: "pass"}, nil).Once()
	env.OnActivity("pr.create_and_link", mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{}, nil).Once()

	env.ExecuteWorkflow(conductor.RunWorkflow, conductor.RunInput{Definition: def})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result conductor.RunResult
	require.NoError(t, env.GetWorkflowResult(&result))

	require.Equal(t, "COMPLETED", result.FinalState)
	require.Equal(t, []string{"provision", "execute", "verify", "merge"}, result.StepsVisited)
	require.Equal(t, 1, result.BudgetSpent["verify_rounds"])
	env.AssertExpectations(t)
}

func TestRunWorkflowRecordsTransitionEvents(t *testing.T) {
	env := newTestEnv(t)
	def := mustParseDependencyBumpMinimal(t)

	env.OnActivity("worktree.create", mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{}, nil).Once()
	env.OnActivity(conductor.HarnessInvokeActivityName, mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{TokensUsed: 42}, nil).Once()
	env.OnActivity("run.tests_lint_build", mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{Outcome: "pass"}, nil).Once()
	env.OnActivity("pr.create_and_link", mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{}, nil).Once()

	var events []conductor.TransitionEvent
	env.OnActivity(conductor.RecordEventActivityName, mock.Anything, mock.Anything).
		Return(func(ctx context.Context, ev conductor.TransitionEvent) error {
			events = append(events, ev)
			return nil
		}).Times(5) // start + provision->execute->verify->merge->COMPLETED

	env.ExecuteWorkflow(conductor.RunWorkflow, conductor.RunInput{Definition: def})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	require.Len(t, events, 5)
	require.Equal(t, "", events[0].FromStep)
	require.Equal(t, "provision", events[0].ToStep)
	require.Equal(t, "dependency-bump-minimal", events[0].Workflow)

	require.Equal(t, "provision", events[1].FromStep)
	require.Equal(t, "execute", events[1].ToStep)

	require.Equal(t, "execute", events[2].FromStep)
	require.Equal(t, "verify", events[2].ToStep)
	require.Equal(t, 42, events[2].TokenDelta, "execute's TokensUsed should flow into its transition event")
	require.Equal(t, 1, events[2].ActivityCalls)

	require.Equal(t, "verify", events[3].FromStep)
	require.Equal(t, "merge", events[3].ToStep)

	require.Equal(t, "merge", events[4].FromStep)
	require.Equal(t, "COMPLETED", events[4].ToStep)

	env.AssertExpectations(t)
}

func TestRunWorkflowRecordsBudgetExhaustedTransitionWithoutAnActivityCall(t *testing.T) {
	env := newTestEnv(t)
	def := mustParseDependencyBumpMinimal(t) // verify_rounds: max_attempts: 2

	env.OnActivity("worktree.create", mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{}, nil).Once()
	env.OnActivity(conductor.HarnessInvokeActivityName, mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{}, nil).Times(3)
	env.OnActivity("run.tests_lint_build", mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{Outcome: "fail", Produced: map[string]any{"failing_tests_diff": "boom"}}, nil).
		Times(2)

	var events []conductor.TransitionEvent
	env.OnActivity(conductor.RecordEventActivityName, mock.Anything, mock.Anything).
		Return(func(ctx context.Context, ev conductor.TransitionEvent) error {
			events = append(events, ev)
			return nil
		}).Times(8)
	// start->provision, provision->execute, execute->verify, verify->revise_verify,
	// revise_verify->verify, verify->revise_verify, revise_verify->verify,
	// verify->FAILED (3rd verify entry: budget exhausted, no Activity call)

	env.ExecuteWorkflow(conductor.RunWorkflow, conductor.RunInput{Definition: def})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	last := events[len(events)-1]
	require.Equal(t, "verify", last.FromStep)
	require.Equal(t, "FAILED", last.ToStep)
	require.Equal(t, 0, last.ActivityCalls, "budget-exhausted transition happens without calling the step's Activity")
	require.Equal(t, 0, last.TokenDelta)

	env.AssertExpectations(t)
}

func TestRunWorkflowFinalContextAccumulatesProducedFields(t *testing.T) {
	env := newTestEnv(t)
	def := mustParseDependencyBumpMinimal(t)

	env.OnActivity("worktree.create", mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{Produced: map[string]any{"worktree_path": "/tmp/wt", "branch": "factory/run-1"}}, nil).Once()
	env.OnActivity(conductor.HarnessInvokeActivityName, mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{Produced: map[string]any{"diff": "some diff"}}, nil).Once()
	env.OnActivity("run.tests_lint_build", mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{Outcome: "pass"}, nil).Once()
	env.OnActivity("pr.create_and_link", mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{}, nil).Once()

	env.ExecuteWorkflow(conductor.RunWorkflow, conductor.RunInput{Definition: def})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result conductor.RunResult
	require.NoError(t, env.GetWorkflowResult(&result))

	require.Equal(t, "COMPLETED", result.FinalState)
	require.Equal(t, "/tmp/wt", result.FinalContext["worktree_path"])
	require.Equal(t, "factory/run-1", result.FinalContext["branch"])
	require.Equal(t, "some diff", result.FinalContext["diff"])
}

func TestRunWorkflowToolStepsGetFullContextWithoutDeclaringIt(t *testing.T) {
	env := newTestEnv(t)
	def := mustParseDependencyBumpMinimal(t) // verify (type: tool) declares no `context:`

	env.OnActivity("worktree.create", mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{Produced: map[string]any{"worktree_path": "/tmp/wt"}}, nil).Once()
	env.OnActivity(conductor.HarnessInvokeActivityName, mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{}, nil).Once()
	env.OnActivity("run.tests_lint_build", mock.Anything, mock.MatchedBy(func(in conductor.ActivityInput) bool {
		return in.Context["worktree_path"] == "/tmp/wt"
	})).Return(conductor.ActivityOutput{Outcome: "pass"}, nil).Once()
	env.OnActivity("pr.create_and_link", mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{}, nil).Once()

	env.ExecuteWorkflow(conductor.RunWorkflow, conductor.RunInput{Definition: def})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var result conductor.RunResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, "COMPLETED", result.FinalState)
	env.AssertExpectations(t)
}

func TestRunWorkflowLoopThenPass(t *testing.T) {
	env := newTestEnv(t)
	def := mustParseDependencyBumpMinimal(t)

	env.OnActivity("worktree.create", mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{}, nil).Once()
	env.OnActivity(conductor.HarnessInvokeActivityName, mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{}, nil).Twice() // execute, then revise_verify
	env.OnActivity("run.tests_lint_build", mock.Anything, mock.Anything).
		Return(func(ctx context.Context, in conductor.ActivityInput) (conductor.ActivityOutput, error) {
			if in.AttemptNumber == 1 {
				return conductor.ActivityOutput{Outcome: "fail", Produced: map[string]any{"failing_tests_diff": "boom"}}, nil
			}
			return conductor.ActivityOutput{Outcome: "pass"}, nil
		}).Twice()
	env.OnActivity("pr.create_and_link", mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{}, nil).Once()

	env.ExecuteWorkflow(conductor.RunWorkflow, conductor.RunInput{Definition: def})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result conductor.RunResult
	require.NoError(t, env.GetWorkflowResult(&result))

	require.Equal(t, "COMPLETED", result.FinalState)
	require.Equal(t,
		[]string{"provision", "execute", "verify", "revise_verify", "verify", "merge"},
		result.StepsVisited)
	require.Equal(t, 2, result.BudgetSpent["verify_rounds"])
	env.AssertExpectations(t)
}

func TestRunWorkflowBudgetExhausted(t *testing.T) {
	env := newTestEnv(t)
	def := mustParseDependencyBumpMinimal(t) // verify_rounds: max_attempts: 2

	env.OnActivity("worktree.create", mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{}, nil).Once()
	env.OnActivity(conductor.HarnessInvokeActivityName, mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{}, nil).Times(3) // execute, then two revise_verify calls
	env.OnActivity("run.tests_lint_build", mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{Outcome: "fail", Produced: map[string]any{"failing_tests_diff": "boom"}}, nil).
		Times(2) // exactly 2 — the 3rd re-entry must be blocked before another Activity call

	env.ExecuteWorkflow(conductor.RunWorkflow, conductor.RunInput{Definition: def})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result conductor.RunResult
	require.NoError(t, env.GetWorkflowResult(&result))

	require.Equal(t, "FAILED", result.FinalState)
	require.Equal(t,
		[]string{"provision", "execute", "verify", "revise_verify", "verify", "revise_verify"},
		result.StepsVisited, "the 3rd verify entry is budget-blocked and never appended")
	require.Equal(t, 3, result.BudgetSpent["verify_rounds"], "counter still increments on the blocked re-entry")
	env.AssertExpectations(t)
	env.AssertNotCalled(t, "pr.create_and_link", mock.Anything, mock.Anything)
}

func TestRunWorkflowMalformedOutputRouting(t *testing.T) {
	def := workflowdef.Definition{
		Workflow: "malformed-output-routing",
		Version:  1,
		Roles: map[string]workflowdef.Role{
			"planner": {Harness: "claude-plan", Model: "sonnet-5-medium"},
		},
		Steps: []workflowdef.Step{
			{
				ID:                "plan",
				Type:              workflowdef.StepTypeAgent,
				Role:              "planner",
				OutputSchema:      map[string]any{"verdict": []any{"proceed", "reject", "escalate"}},
				On:                map[string]workflowdef.Target{"proceed": {StepOrState: "COMPLETED"}, "reject": {StepOrState: "FAILED"}, "escalate": {StepOrState: "REVIEW_PENDING"}},
				OnMalformedOutput: "REVIEW_PENDING",
			},
		},
	}
	require.Empty(t, workflowdef.Validate(&def))

	env := newTestEnv(t)
	env.OnActivity(conductor.HarnessInvokeActivityName, mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{Malformed: true}, nil).Once()

	// REVIEW_PENDING now genuinely blocks on a signal (doc 05's
	// signal-wait) rather than returning immediately, so this test needs
	// to send a decision — a "cancel" here, since the point of this test
	// is proving malformed output routes to the REVIEW_PENDING gate at
	// all, not exercising resume (see the dedicated signal-wait tests
	// below). Reaching CANCELLED is only possible by first passing
	// through REVIEW_PENDING, so it still proves the routing.
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(conductor.HumanDecisionSignalName, conductor.HumanDecision{Action: "cancel"})
	}, time.Minute)

	env.ExecuteWorkflow(conductor.RunWorkflow, conductor.RunInput{Definition: def})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result conductor.RunResult
	require.NoError(t, env.GetWorkflowResult(&result))

	require.Equal(t, "CANCELLED", result.FinalState)
	require.Equal(t, []string{"plan"}, result.StepsVisited)
	env.AssertExpectations(t)
}

func TestRunWorkflowReviewPendingCancelViaSignal(t *testing.T) {
	env := newTestEnv(t)
	def := workflowdef.Definition{
		Workflow: "cancel-test",
		Version:  1,
		Steps: []workflowdef.Step{
			{
				ID: "verify", Type: workflowdef.StepTypeTool, Action: "run.tests_lint_build",
				On: map[string]workflowdef.Target{"pass": {StepOrState: "COMPLETED"}, "fail": {StepOrState: "REVIEW_PENDING"}},
			},
		},
	}
	require.Empty(t, workflowdef.Validate(&def))

	env.OnActivity("run.tests_lint_build", mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{Outcome: "fail"}, nil).Once()

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(conductor.HumanDecisionSignalName, conductor.HumanDecision{Action: "cancel"})
	}, time.Minute)

	env.ExecuteWorkflow(conductor.RunWorkflow, conductor.RunInput{Definition: def})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result conductor.RunResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, "CANCELLED", result.FinalState)
	env.AssertExpectations(t)
}

func TestRunWorkflowResumeResetsAllBudgetCountersAndMergesHint(t *testing.T) {
	env := newTestEnv(t)
	def := workflowdef.Definition{
		Workflow: "resume-budget-reset",
		Version:  1,
		Budgets:  map[string]workflowdef.Budget{"verify_rounds": {MaxAttempts: 1}},
		Steps: []workflowdef.Step{
			{
				ID: "verify", Type: workflowdef.StepTypeTool, Action: "run.tests_lint_build",
				Budget: "verify_rounds",
				On: map[string]workflowdef.Target{
					"pass": {StepOrState: "COMPLETED"},
					"fail": {StepOrState: "REVIEW_PENDING"},
				},
			},
		},
	}
	require.Empty(t, workflowdef.Validate(&def))

	var attemptNumbers []int
	env.OnActivity("run.tests_lint_build", mock.Anything, mock.Anything).
		Return(func(ctx context.Context, in conductor.ActivityInput) (conductor.ActivityOutput, error) {
			attemptNumbers = append(attemptNumbers, in.AttemptNumber)
			if len(attemptNumbers) == 1 {
				return conductor.ActivityOutput{Outcome: "fail"}, nil
			}
			return conductor.ActivityOutput{Outcome: "pass"}, nil
		}).Twice()

	// Reaching REVIEW_PENDING here is via the "fail" outcome directly
	// (max_attempts:1 means the very first failure already exhausts the
	// budget's own attempt count, but this test signals resume before
	// that path is even reached — it's exercising resume's reset, not
	// the budget_exhausted route).
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(conductor.HumanDecisionSignalName, conductor.HumanDecision{
			Action: "resume", ResumeStepID: "verify", Hint: "try again",
		})
	}, time.Minute)

	env.ExecuteWorkflow(conductor.RunWorkflow, conductor.RunInput{Definition: def})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result conductor.RunResult
	require.NoError(t, env.GetWorkflowResult(&result))

	require.Equal(t, "COMPLETED", result.FinalState)
	require.Equal(t, []int{1, 1}, attemptNumbers,
		"budget counter must reset to zero-spent on resume (doc 01), so the post-resume call is attempt 1 again, not 2")
	require.Equal(t, "try again", result.FinalContext["human_hint"])
	env.AssertExpectations(t)
}

func TestRunWorkflowDispatchesCompoundActionSideEffect(t *testing.T) {
	env := newTestEnv(t)
	env.RegisterActivityWithOptions(placeholderActivity, activity.RegisterOptions{
		Name:                          "task.create",
		DisableAlreadyRegisteredCheck: true,
	})

	def := workflowdef.Definition{
		Workflow: "out-of-scope-test",
		Version:  1,
		Roles:    map[string]workflowdef.Role{"coder": {Harness: "claude-code", Model: "x"}},
		Steps: []workflowdef.Step{
			{
				ID: "coder_response", Type: workflowdef.StepTypeAgent, Role: "coder",
				OutputSchema: map[string]any{"verdict": []any{"address", "dispute", "escalate", "out_of_scope"}},
				On: map[string]workflowdef.Target{
					"address":      {StepOrState: "COMPLETED"},
					"dispute":      {StepOrState: "FAILED"},
					"escalate":     {StepOrState: "FAILED"},
					"out_of_scope": {Action: "task.create(source=review-finding)", Next: "COMPLETED"},
				},
				OnMalformedOutput: "FAILED",
			},
		},
	}
	require.Empty(t, workflowdef.Validate(&def))

	env.OnActivity(conductor.HarnessInvokeActivityName, mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{Outcome: "out_of_scope"}, nil).Once()
	env.OnActivity("task.create", mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{Produced: map[string]any{"spawned_task_id": "task-abc-123"}}, nil).Once()

	env.ExecuteWorkflow(conductor.RunWorkflow, conductor.RunInput{Definition: def})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result conductor.RunResult
	require.NoError(t, env.GetWorkflowResult(&result))

	require.Equal(t, "COMPLETED", result.FinalState)
	require.Equal(t, "task-abc-123", result.FinalContext["spawned_task_id"])
	require.Equal(t, []string{"coder_response"}, result.StepsVisited,
		"task.create is a side-effect dispatch inside route(), not its own DAG step visit")
	env.AssertExpectations(t)
}

func TestRunWorkflowNamedBudgetMaxTokensExhausted(t *testing.T) {
	env := newTestEnv(t)
	def := workflowdef.Definition{
		Workflow: "token-budget-test",
		Version:  1,
		Roles:    map[string]workflowdef.Role{"coder": {Harness: "claude-code", Model: "sonnet"}},
		Budgets:  map[string]workflowdef.Budget{"execute_budget": {MaxTokens: 100}},
		Steps: []workflowdef.Step{
			{
				ID: "execute", Type: workflowdef.StepTypeAgent, Role: "coder",
				Budget: "execute_budget",
				On: map[string]workflowdef.Target{
					"retry":            {StepOrState: "execute"},
					"budget_exhausted": {StepOrState: "FAILED"},
				},
			},
		},
	}
	require.Empty(t, workflowdef.Validate(&def))

	// 60 + 60 = 120 > 100: the 3rd entry must be blocked by
	// RealBudgetGate.CheckTokenBudget before a 3rd Activity call happens.
	env.OnActivity(conductor.HarnessInvokeActivityName, mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{Outcome: "retry", TokensUsed: 60}, nil).Twice()

	env.ExecuteWorkflow(conductor.RunWorkflow, conductor.RunInput{Definition: def})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result conductor.RunResult
	require.NoError(t, env.GetWorkflowResult(&result))

	require.Equal(t, "FAILED", result.FinalState)
	require.Equal(t, []string{"execute", "execute"}, result.StepsVisited,
		"the 3rd entry is budget-blocked (MaxTokens) and never appended")
	env.AssertExpectations(t)
}

func TestRunWorkflowHarnessLimitTripsToReviewPending(t *testing.T) {
	env := newTestEnv(t)
	def := workflowdef.Definition{
		Workflow: "harness-limit-test",
		Version:  1,
		Roles: map[string]workflowdef.Role{
			"coder": {Harness: "claude-code", Model: "sonnet", Params: map[string]string{"effort": "high"}},
		},
		Budgets: map[string]workflowdef.Budget{"loop_budget": {MaxAttempts: 10}},
		Steps: []workflowdef.Step{
			{
				ID: "execute", Type: workflowdef.StepTypeAgent, Role: "coder",
				Budget: "loop_budget",
				On: map[string]workflowdef.Target{
					"retry":            {StepOrState: "execute"},
					"done":             {StepOrState: "COMPLETED"},
					"budget_exhausted": {StepOrState: "FAILED"},
				},
			},
		},
	}
	require.Empty(t, workflowdef.Validate(&def))

	// A single call already spends more than the configured limit, so the
	// 2nd entry must be blocked before another Activity call — routed to
	// REVIEW_PENDING unconditionally, bypassing the step's own "retry" `on:`
	// mapping entirely (there's no `on:` outcome for this).
	env.OnActivity(conductor.HarnessInvokeActivityName, mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{Outcome: "retry", TokensUsed: 60}, nil).Once()

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(conductor.HumanDecisionSignalName, conductor.HumanDecision{Action: "cancel"})
	}, time.Minute)

	env.ExecuteWorkflow(conductor.RunWorkflow, conductor.RunInput{
		Definition:    def,
		HarnessLimits: map[string]int{"claude-code/sonnet/high": 50},
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result conductor.RunResult
	require.NoError(t, env.GetWorkflowResult(&result))

	require.Equal(t, "CANCELLED", result.FinalState)
	require.Equal(t, []string{"execute"}, result.StepsVisited,
		"the 2nd entry is harness-limit-blocked before being appended")
	env.AssertExpectations(t)
}

func TestRunWorkflowUnconfiguredHarnessComboHasNoLimit(t *testing.T) {
	env := newTestEnv(t)
	def := workflowdef.Definition{
		Workflow: "harness-limit-unconfigured-test",
		Version:  1,
		Roles: map[string]workflowdef.Role{
			"coder": {Harness: "claude-code", Model: "sonnet", Params: map[string]string{"effort": "high"}},
		},
		Budgets: map[string]workflowdef.Budget{"loop_budget": {MaxAttempts: 10}},
		Steps: []workflowdef.Step{
			{
				ID: "execute", Type: workflowdef.StepTypeAgent, Role: "coder",
				Budget: "loop_budget",
				On: map[string]workflowdef.Target{
					"retry":            {StepOrState: "execute"},
					"done":             {StepOrState: "COMPLETED"},
					"budget_exhausted": {StepOrState: "FAILED"},
				},
			},
		},
	}
	require.Empty(t, workflowdef.Validate(&def))

	env.OnActivity(conductor.HarnessInvokeActivityName, mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{Outcome: "retry", TokensUsed: 6000}, nil).Once()
	env.OnActivity(conductor.HarnessInvokeActivityName, mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{Outcome: "done", TokensUsed: 6000}, nil).Once()

	// No HarnessLimits set at all — an unconfigured (harness, model,
	// effort) combination must never be blocked, no matter how much it
	// spends (opt-in enforcement, not a blanket cap).
	env.ExecuteWorkflow(conductor.RunWorkflow, conductor.RunInput{Definition: def})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result conductor.RunResult
	require.NoError(t, env.GetWorkflowResult(&result))

	require.Equal(t, "COMPLETED", result.FinalState)
	require.Equal(t, []string{"execute", "execute"}, result.StepsVisited)
	env.AssertExpectations(t)
}

func TestRunWorkflowHarnessLimitSharedAcrossDifferentRoles(t *testing.T) {
	env := newTestEnv(t)
	def := workflowdef.Definition{
		Workflow: "harness-limit-shared-test",
		Version:  1,
		Roles: map[string]workflowdef.Role{
			"coder":    {Harness: "claude-code", Model: "sonnet", Params: map[string]string{"effort": "high"}},
			"reviewer": {Harness: "claude-code", Model: "sonnet", Params: map[string]string{"effort": "high"}},
		},
		Steps: []workflowdef.Step{
			{
				ID: "execute", Type: workflowdef.StepTypeAgent, Role: "coder",
				On: map[string]workflowdef.Target{"next": {StepOrState: "review"}},
			},
			{
				ID: "review", Type: workflowdef.StepTypeAgent, Role: "reviewer",
				On: map[string]workflowdef.Target{"verdict": {StepOrState: "COMPLETED"}},
			},
		},
	}
	require.Empty(t, workflowdef.Validate(&def))

	// execute (role: coder) and review (role: reviewer) share the same
	// (harness, model, effort) combination despite different roles — the
	// limit is keyed purely on that combination, so execute's spend alone
	// must be enough to block review before review's own Activity call.
	env.OnActivity(conductor.HarnessInvokeActivityName, mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{Outcome: "next", TokensUsed: 60}, nil).Once()

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(conductor.HumanDecisionSignalName, conductor.HumanDecision{Action: "cancel"})
	}, time.Minute)

	env.ExecuteWorkflow(conductor.RunWorkflow, conductor.RunInput{
		Definition:    def,
		HarnessLimits: map[string]int{"claude-code/sonnet/high": 50},
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result conductor.RunResult
	require.NoError(t, env.GetWorkflowResult(&result))

	require.Equal(t, "CANCELLED", result.FinalState)
	require.Equal(t, []string{"execute"}, result.StepsVisited,
		"review is blocked before being appended, purely from execute's spend on the shared combination")
	env.AssertExpectations(t)
}

func TestRunWorkflowStartsAtTerminalState(t *testing.T) {
	env := newTestEnv(t)
	def := workflowdef.Definition{
		Workflow: "trivially-terminal",
		Version:  1,
		Steps:    []workflowdef.Step{{ID: "unused", Type: workflowdef.StepTypeTool, Action: "noop", Next: "COMPLETED"}},
	}

	env.ExecuteWorkflow(conductor.RunWorkflow, conductor.RunInput{
		Definition:  def,
		StartStepID: "CANCELLED",
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result conductor.RunResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, "CANCELLED", result.FinalState)
	require.Empty(t, result.StepsVisited)
}
