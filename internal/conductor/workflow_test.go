package conductor_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

// placeholderTrackerPostComment stands in for TrackerPostCommentActivityName
// (docs/08) — same reasoning as placeholderRecordEvent above:
// postTrackerComments swallows a call to an unregistered/unmocked activity
// as a logged warning rather than failing the Run, so registering this by
// default means tests that do exercise the tracker mirror succeed because
// the call actually resolved, not by accident of that error-swallowing.
func placeholderTrackerPostComment(ctx context.Context, in conductor.TrackerCommentInput) error {
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
	env.RegisterActivityWithOptions(placeholderTrackerPostComment, activity.RegisterOptions{
		Name:                          conductor.TrackerPostCommentActivityName,
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
	require.Equal(t, []string{"provision", "execute", "verify", "create_pr"}, result.StepsVisited)
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
		}).Times(5) // start + provision->execute->verify->create_pr->COMPLETED

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
	require.Equal(t, "create_pr", events[3].ToStep)

	require.Equal(t, "create_pr", events[4].FromStep)
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
		[]string{"provision", "execute", "verify", "revise_verify", "verify", "create_pr"},
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

// TestRunWorkflowRecordsFailedTransitionOnHardActivityError is a
// regression test for a real bug: a step Activity call returning a raw
// error (e.g. gitops.WorktreeCreate's "mkdir /var/lib/factory: permission
// denied" — the exact failure that motivated this test) used to fail the
// Run with nothing recorded in run_events beyond the initial "start"
// transition, violating doc01's "every state transition emits a
// structured event... including Runs that fail before any model call."
// recordFailure (workflow.go) fixes this — every hard-failure return in
// RunWorkflow's main loop now goes through it.
func TestRunWorkflowRecordsFailedTransitionOnHardActivityError(t *testing.T) {
	def := workflowdef.Definition{
		Workflow: "hard-activity-error-test",
		Version:  1,
		Steps: []workflowdef.Step{
			{ID: "provision", Type: workflowdef.StepTypeTool, Action: "worktree.create", Next: "COMPLETED"},
		},
	}
	require.Empty(t, workflowdef.Validate(&def))

	env := newTestEnv(t)
	env.OnActivity("worktree.create", mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{}, errors.New("mkdir /var/lib/factory: permission denied")).Once()

	var events []conductor.TransitionEvent
	env.OnActivity(conductor.RecordEventActivityName, mock.Anything, mock.Anything).
		Return(func(ctx context.Context, ev conductor.TransitionEvent) error {
			events = append(events, ev)
			return nil
		})

	env.ExecuteWorkflow(conductor.RunWorkflow, conductor.RunInput{Definition: def})

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError(), "a hard Activity error must still fail the Run")

	require.NotEmpty(t, events, "the failure must be recorded, not left as only Temporal's own workflow history")
	last := events[len(events)-1]
	require.Equal(t, "provision", last.FromStep)
	require.Equal(t, "FAILED", last.ToStep)
	require.Contains(t, last.FailureReason, "permission denied",
		"the recorded event must carry the actual error text, not just that a failure happened")
}

// TestRunWorkflowFailureReasonStripsTemporalWrapping is a regression
// guard for a real failure that reached a human as: `conductor: activity
// "harness.invoke" for step "execute": activity error (type:
// harness.invoke, scheduledEventID: 51, startedEventID: 52, identity:
// ...): harness: invoke: codex: exit status 1: Reading additional input
// from stdin... (type: wrapError, retryable: true): codex: exit status
// 1: Reading additional input from stdin... (type: wrapError, retryable:
// true): exit status 1 (type: ExitError, retryable: true)` — Temporal's
// own ActivityError preamble plus visibly duplicated text (Temporal's
// error converter re-serializes each Go-level %w wrap as its own nested
// ApplicationError). The recorded FailureReason must be just what the
// Activity function actually returned.
func TestRunWorkflowFailureReasonStripsTemporalWrapping(t *testing.T) {
	def := workflowdef.Definition{
		Workflow: "failure-reason-test",
		Version:  1,
		Steps: []workflowdef.Step{
			{ID: "execute", Type: workflowdef.StepTypeTool, Action: "run.tests_lint_build", Next: "COMPLETED"},
		},
	}
	require.Empty(t, workflowdef.Validate(&def))

	env := newTestEnv(t)
	// Same shape as internal/activities/harness's real wrap chain:
	// harness.go wraps codex.go wraps *exec.ExitError.
	innermost := errors.New("exit status 1")
	middle := fmt.Errorf("codex: %w: %s", innermost, "Reading additional input from stdin...")
	outer := fmt.Errorf("harness: invoke: %w", middle)
	env.OnActivity("run.tests_lint_build", mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{}, outer).Once()

	var events []conductor.TransitionEvent
	env.OnActivity(conductor.RecordEventActivityName, mock.Anything, mock.Anything).
		Return(func(ctx context.Context, ev conductor.TransitionEvent) error {
			events = append(events, ev)
			return nil
		})

	env.ExecuteWorkflow(conductor.RunWorkflow, conductor.RunInput{Definition: def})

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError())

	require.NotEmpty(t, events)
	last := events[len(events)-1]
	require.Equal(t, "FAILED", last.ToStep)
	require.Equal(t, "harness: invoke: codex: exit status 1: Reading additional input from stdin...", last.FailureReason,
		"FailureReason must be exactly what the Activity function returned — no Temporal preamble, no duplicated text")
}

func TestRunWorkflowMalformedOutputRouting(t *testing.T) {
	def := workflowdef.Definition{
		Workflow: "malformed-output-routing",
		Version:  1,
		Roles: []string{"planner"},
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

// TestRunWorkflowResumeHintIsPersistedOnTheTransitionEvent is a
// regression guard for docs/01's mandatory plan-approval gate: "the
// record isn't just 'approved,' it's 'approved, and here's why'" is only
// true if the hint/justification text actually lands in the persisted
// TransitionEvent (what a control-plane surface renders Summary from,
// via FormatEventContent), not just merged into the resumed step's own
// live runContext and lost once the Run moves on.
func TestRunWorkflowResumeHintIsPersistedOnTheTransitionEvent(t *testing.T) {
	env := newTestEnv(t)
	def := workflowdef.Definition{
		Workflow: "resume-hint-persisted",
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

	var events []conductor.TransitionEvent
	env.OnActivity(conductor.RecordEventActivityName, mock.Anything, mock.Anything).
		Return(func(ctx context.Context, ev conductor.TransitionEvent) error {
			events = append(events, ev)
			return nil
		})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(conductor.HumanDecisionSignalName, conductor.HumanDecision{
			Action: "cancel",
		})
	}, time.Minute)

	// Resume back to the same step (no budget here, so nothing to
	// re-fail against) — what matters is only the resume transition's
	// own recorded Produced content, so cancel right after to keep this
	// test's shape minimal rather than modeling a full second pass.
	env.OnActivity("run.tests_lint_build", mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{Outcome: "fail"}, nil).Maybe()
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(conductor.HumanDecisionSignalName, conductor.HumanDecision{
			Action: "resume", ResumeStepID: "verify", Hint: "Approved: looks right, ship it",
		})
	}, time.Second)

	env.ExecuteWorkflow(conductor.RunWorkflow, conductor.RunInput{Definition: def})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var sawResumeEvent bool
	for _, ev := range events {
		if ev.FromStep == "REVIEW_PENDING" && ev.ToStep == "verify" {
			sawResumeEvent = true
			require.Equal(t, "Approved: looks right, ship it", ev.Produced["human_hint"],
				"the resume transition's own Produced must carry the hint, not just live runContext")
		}
	}
	require.True(t, sawResumeEvent, "expected a REVIEW_PENDING -> verify transition event")
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
		Roles:    []string{"coder"},
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
		Roles:    []string{"coder"},
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
		Roles: []string{"coder"},
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
		Definition:      def,
		HarnessLimits:   map[string]int{"claude-code/sonnet/high": 50},
		RoleAssignments: map[string]workflowdef.Role{"coder": {Harness: "claude-code", Model: "sonnet", Params: map[string]string{"effort": "high"}}},
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
		Roles: []string{"coder"},
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
		Roles: []string{"coder", "reviewer"},
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
		RoleAssignments: map[string]workflowdef.Role{
			"coder":    {Harness: "claude-code", Model: "sonnet", Params: map[string]string{"effort": "high"}},
			"reviewer": {Harness: "claude-code", Model: "sonnet", Params: map[string]string{"effort": "high"}},
		},
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

func TestRunWorkflowOscillationFailsFastBeforeAttemptCap(t *testing.T) {
	env := newTestEnv(t)
	def := workflowdef.Definition{
		Workflow: "oscillation-test",
		Version:  1,
		// max_attempts:5 is deliberately generous — the point of this test
		// is that a non-shrinking failing-test-set fails fast on attempt 2,
		// long before the attempt cap would ever kick in.
		Budgets: map[string]workflowdef.Budget{"verify_rounds": {MaxAttempts: 5}},
		Steps: []workflowdef.Step{
			{
				ID: "verify", Type: workflowdef.StepTypeTool, Action: "run.tests_lint_build",
				Budget: "verify_rounds",
				On: map[string]workflowdef.Target{
					"pass":             {StepOrState: "COMPLETED"},
					"fail":             {StepOrState: "verify"},
					"budget_exhausted": {StepOrState: "FAILED"},
				},
			},
		},
	}
	require.Empty(t, workflowdef.Validate(&def))

	// Same failing test on both attempts — attempt 2's set is equal to
	// (not shrinking from) attempt 1's, so CheckOscillation must trip
	// before a 3rd Activity call.
	env.OnActivity("run.tests_lint_build", mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{Outcome: "fail", Produced: map[string]any{"failing_tests_diff": "FAIL a"}}, nil).
		Twice()

	env.ExecuteWorkflow(conductor.RunWorkflow, conductor.RunInput{Definition: def})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result conductor.RunResult
	require.NoError(t, env.GetWorkflowResult(&result))

	require.Equal(t, "FAILED", result.FinalState)
	require.Equal(t, []string{"verify", "verify"}, result.StepsVisited,
		"the 3rd entry is oscillation-blocked, well before max_attempts:5")
	env.AssertExpectations(t)
}

func TestRunWorkflowShrinkingFailingTestsDoesNotTripOscillation(t *testing.T) {
	env := newTestEnv(t)
	def := workflowdef.Definition{
		Workflow: "oscillation-shrinking-test",
		Version:  1,
		Budgets:  map[string]workflowdef.Budget{"verify_rounds": {MaxAttempts: 5}},
		Steps: []workflowdef.Step{
			{
				ID: "verify", Type: workflowdef.StepTypeTool, Action: "run.tests_lint_build",
				Budget: "verify_rounds",
				On: map[string]workflowdef.Target{
					"pass":             {StepOrState: "COMPLETED"},
					"fail":             {StepOrState: "verify"},
					"budget_exhausted": {StepOrState: "FAILED"},
				},
			},
		},
	}
	require.Empty(t, workflowdef.Validate(&def))

	env.OnActivity("run.tests_lint_build", mock.Anything, mock.Anything).
		Return(func(ctx context.Context, in conductor.ActivityInput) (conductor.ActivityOutput, error) {
			if in.AttemptNumber == 1 {
				return conductor.ActivityOutput{Outcome: "fail", Produced: map[string]any{"failing_tests_diff": "FAIL a\nFAIL b"}}, nil
			}
			return conductor.ActivityOutput{Outcome: "pass"}, nil
		}).Twice()

	env.ExecuteWorkflow(conductor.RunWorkflow, conductor.RunInput{Definition: def})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result conductor.RunResult
	require.NoError(t, env.GetWorkflowResult(&result))

	require.Equal(t, "COMPLETED", result.FinalState)
	require.Equal(t, []string{"verify", "verify"}, result.StepsVisited)
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

// TestRunWorkflowPostsTrackerCommentOnceRunHasAPRURL is docs/08's PR-side
// mirror: once pr.create_and_link produces pr_url, the transition that
// merged it (create_pr -> COMPLETED) must post a github_pr tracker comment
// carrying that same URL.
func TestRunWorkflowPostsTrackerCommentOnceRunHasAPRURL(t *testing.T) {
	env := newTestEnv(t)
	def := mustParseDependencyBumpMinimal(t)

	env.OnActivity("worktree.create", mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{}, nil).Once()
	env.OnActivity(conductor.HarnessInvokeActivityName, mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{}, nil).Once()
	env.OnActivity("run.tests_lint_build", mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{Outcome: "pass"}, nil).Once()
	env.OnActivity("pr.create_and_link", mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{Produced: map[string]any{"pr_url": "https://github.com/o/r/pull/7"}}, nil).Once()

	var comments []conductor.TrackerCommentInput
	env.OnActivity(conductor.TrackerPostCommentActivityName, mock.Anything, mock.Anything).
		Return(func(ctx context.Context, in conductor.TrackerCommentInput) error {
			comments = append(comments, in)
			return nil
		})

	env.ExecuteWorkflow(conductor.RunWorkflow, conductor.RunInput{Definition: def})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	require.NotEmpty(t, comments, "expected at least one tracker comment once pr_url is known")
	last := comments[len(comments)-1]
	require.Equal(t, "github_pr", last.TargetKind)
	require.Equal(t, "https://github.com/o/r/pull/7", last.TargetRef)
	require.Contains(t, last.Body, "create_pr")
}

// TestRunWorkflowTrackerCommentAuthorLine is the end-to-end version of
// TestAuthorLineWorkerIdentityForAgentTransition/
// TestAuthorLineConductorForToolOwnedTransition — proves the wiring from
// RunInput.RoleAssignments through the step loop's authorRole/
// authorHarness/authorModel/authorEffort locals into a posted comment's
// actual first line, not just the formatting function in isolation.
func TestRunWorkflowTrackerCommentAuthorLine(t *testing.T) {
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

	var comments []conductor.TrackerCommentInput
	env.OnActivity(conductor.TrackerPostCommentActivityName, mock.Anything, mock.Anything).
		Return(func(ctx context.Context, in conductor.TrackerCommentInput) error {
			comments = append(comments, in)
			return nil
		})

	env.ExecuteWorkflow(conductor.RunWorkflow, conductor.RunInput{
		Definition:      def,
		SourceRef:       conductor.SourceRef{Kind: "github_issue", Ref: "https://github.com/o/r/issues/1"},
		RoleAssignments: map[string]workflowdef.Role{"coder": {Harness: "codex", Model: "gpt-5.6-luna", Params: map[string]string{"effort": "medium"}}},
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.NotEmpty(t, comments)

	var sawAgentAuthor, sawConductorAuthor bool
	for _, c := range comments {
		if strings.HasPrefix(c.Body, "coder:codex/gpt-5.6-luna/medium\n") {
			sawAgentAuthor = true
		}
		if strings.HasPrefix(c.Body, "conductor\n") {
			sawConductorAuthor = true
		}
	}
	require.True(t, sawAgentAuthor, "expected at least one comment authored by the coder Worker, got: %+v", comments)
	require.True(t, sawConductorAuthor, "expected at least one comment authored by conductor (COMPLETED is tool-owned), got: %+v", comments)
}

// TestRunWorkflowPostsTrackerCommentToSourceRefFromTheStart is docs/08's
// source-side mirror, curated (per doc08's later "leave only the
// interactions with the agents... and any human pending action"
// decision — see shouldPostComment): SourceRef is resolved before the Run
// even starts (taskintake.Submit), but the Run's own start/provision
// transitions are routing plumbing, not an agent result or a terminal
// state, so they don't post. The first real post is execute's own result
// (the one agent step in dependency-bump-minimal); the last is the Run
// reaching COMPLETED — also commentable now (a Run's final result is
// worth knowing about even though it's not a "pending" action).
func TestRunWorkflowPostsTrackerCommentToSourceRefFromTheStart(t *testing.T) {
	env := newTestEnv(t)
	def := mustParseDependencyBumpMinimal(t)

	env.OnActivity("worktree.create", mock.Anything, mock.Anything).Return(conductor.ActivityOutput{}, nil).Once()
	env.OnActivity(conductor.HarnessInvokeActivityName, mock.Anything, mock.Anything).Return(conductor.ActivityOutput{}, nil).Once()
	env.OnActivity("run.tests_lint_build", mock.Anything, mock.Anything).Return(conductor.ActivityOutput{Outcome: "pass"}, nil).Once()
	env.OnActivity("pr.create_and_link", mock.Anything, mock.Anything).Return(conductor.ActivityOutput{}, nil).Once()

	var comments []conductor.TrackerCommentInput
	env.OnActivity(conductor.TrackerPostCommentActivityName, mock.Anything, mock.Anything).
		Return(func(ctx context.Context, in conductor.TrackerCommentInput) error {
			comments = append(comments, in)
			return nil
		})

	env.ExecuteWorkflow(conductor.RunWorkflow, conductor.RunInput{
		Definition: def,
		SourceRef:  conductor.SourceRef{Kind: "github_issue", Ref: "https://github.com/o/r/issues/3"},
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	// execute -> verify (the agent result) and create_pr -> COMPLETED (the
	// Run's final result) — nothing else in this DAG is either.
	require.Len(t, comments, 2)
	first := comments[0]
	require.Equal(t, "github_issue", first.TargetKind)
	require.Equal(t, "https://github.com/o/r/issues/3", first.TargetRef)
	require.Contains(t, first.Body, "execute")
	last := comments[len(comments)-1]
	require.Contains(t, last.Body, "COMPLETED")
}

// TestRunWorkflowIssueCommentLinksToPROnceKnown is the other half of the
// same curation decision: a human reading the issue can't act on
// anything without knowing where the PR is, so a terminal-state comment
// (here, COMPLETED) posted to the source issue includes a link to it
// whenever pr_url is already known — not just the PR-side comment on the
// PR itself, which obviously doesn't need to link to itself.
func TestRunWorkflowIssueCommentLinksToPROnceKnown(t *testing.T) {
	env := newTestEnv(t)
	def := mustParseDependencyBumpMinimal(t)

	env.OnActivity("worktree.create", mock.Anything, mock.Anything).Return(conductor.ActivityOutput{}, nil).Once()
	env.OnActivity(conductor.HarnessInvokeActivityName, mock.Anything, mock.Anything).Return(conductor.ActivityOutput{}, nil).Once()
	env.OnActivity("run.tests_lint_build", mock.Anything, mock.Anything).Return(conductor.ActivityOutput{Outcome: "pass"}, nil).Once()
	env.OnActivity("pr.create_and_link", mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{Produced: map[string]any{"pr_url": "https://github.com/o/r/pull/7"}}, nil).Once()

	var comments []conductor.TrackerCommentInput
	env.OnActivity(conductor.TrackerPostCommentActivityName, mock.Anything, mock.Anything).
		Return(func(ctx context.Context, in conductor.TrackerCommentInput) error {
			comments = append(comments, in)
			return nil
		})

	env.ExecuteWorkflow(conductor.RunWorkflow, conductor.RunInput{
		Definition: def,
		SourceRef:  conductor.SourceRef{Kind: "github_issue", Ref: "https://github.com/o/r/issues/3"},
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var issueComments []conductor.TrackerCommentInput
	for _, c := range comments {
		if c.TargetKind == "github_issue" {
			issueComments = append(issueComments, c)
		}
	}
	require.NotEmpty(t, issueComments)
	last := issueComments[len(issueComments)-1]
	require.Contains(t, last.Body, "COMPLETED")
	require.Contains(t, last.Body, "PR: https://github.com/o/r/pull/7")

	// The PR-side comment must not link to itself.
	for _, c := range comments {
		if c.TargetKind == "github_pr" {
			require.NotContains(t, c.Body, "PR: https://github.com/o/r/pull/7")
		}
	}
}

// TestRunWorkflowSurvivesTrackerPostCommentFailure is docs/08's "best-
// effort, never blocks the Run": a comment-post failure (a GitHub API
// hiccup, an expired token) must not fail the Run.
func TestRunWorkflowSurvivesTrackerPostCommentFailure(t *testing.T) {
	env := newTestEnv(t)
	def := mustParseDependencyBumpMinimal(t)

	env.OnActivity("worktree.create", mock.Anything, mock.Anything).Return(conductor.ActivityOutput{}, nil).Once()
	env.OnActivity(conductor.HarnessInvokeActivityName, mock.Anything, mock.Anything).Return(conductor.ActivityOutput{}, nil).Once()
	env.OnActivity("run.tests_lint_build", mock.Anything, mock.Anything).Return(conductor.ActivityOutput{Outcome: "pass"}, nil).Once()
	env.OnActivity("pr.create_and_link", mock.Anything, mock.Anything).
		Return(conductor.ActivityOutput{Produced: map[string]any{"pr_url": "https://github.com/o/r/pull/7"}}, nil).Once()
	env.OnActivity(conductor.TrackerPostCommentActivityName, mock.Anything, mock.Anything).
		Return(errors.New("gh: authentication expired"))

	env.ExecuteWorkflow(conductor.RunWorkflow, conductor.RunInput{Definition: def})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError(), "a tracker comment-post failure must not fail the Run")

	var result conductor.RunResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, "COMPLETED", result.FinalState)
}
