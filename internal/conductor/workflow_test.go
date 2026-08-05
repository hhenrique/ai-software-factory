package conductor_test

import (
	"context"
	"testing"

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

	env.ExecuteWorkflow(conductor.RunWorkflow, conductor.RunInput{Definition: def})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result conductor.RunResult
	require.NoError(t, env.GetWorkflowResult(&result))

	require.Equal(t, "REVIEW_PENDING", result.FinalState)
	require.Equal(t, []string{"plan"}, result.StepsVisited)
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
