package conductor

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"factory/internal/workflowdef"
)

// activityTimeout bounds a single Activity call. Generous for this slice
// since real harness/tool calls aren't wired in yet; the stub Activities
// return near-instantly.
const activityTimeout = 10 * time.Minute

// RunWorkflow is the one generic Temporal workflow function for every
// Workflow Definition — doc 05's "compile to Temporal Workflow code, not
// become its own interpreter" resolved as: this loop is itself ordinary
// deterministic Temporal Workflow code, and Temporal remains the actual
// durable executor via workflow.ExecuteActivity. Adding a new Workflow
// Definition (a YAML edit) never requires touching this function.
func RunWorkflow(ctx workflow.Context, in RunInput) (RunResult, error) {
	def := in.Definition

	index := make(map[string]*workflowdef.Step, len(def.Steps))
	for i := range def.Steps {
		s := &def.Steps[i]
		index[s.ID] = s
	}

	startID := in.StartStepID
	if startID == "" {
		if len(def.Steps) == 0 {
			return RunResult{}, fmt.Errorf("conductor: definition %q has no steps", def.Workflow)
		}
		startID = def.Steps[0].ID
	}

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: activityTimeout,
		// Activity-level retry is reserved for future infra-transient-
		// failure handling and deliberately off this slice — domain budget
		// enforcement below is the actual bounded-retry mechanism, not
		// Temporal's RetryPolicy (see docs/05, "Implementation note").
		RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1},
	}
	actx := workflow.WithActivityOptions(ctx, ao)

	// Temporal's own WorkflowExecution.ID is the Run id (doc 05: "Run →
	// Temporal workflow execution") — no separate RunID field needed on
	// RunInput.
	runID := workflow.GetInfo(ctx).WorkflowExecution.ID

	var gate BudgetGate = NoopBudgetGate{}

	runContext := make(map[string]any, len(in.InitialContext))
	for k, v := range in.InitialContext {
		runContext[k] = v
	}

	budgetAttempts := make(map[string]int)
	budgetTokensSpent := make(map[string]int)
	budgetHistory := make(map[string][]ActivityOutput)
	var stepsVisited []string

	currentID := startID
	for {
		if workflowdef.IsTerminalState(currentID) {
			return RunResult{
				FinalState:   currentID,
				StepsVisited: stepsVisited,
				BudgetSpent:  budgetAttempts,
				FinalContext: runContext,
			}, nil
		}

		step, ok := index[currentID]
		if !ok {
			return RunResult{}, fmt.Errorf("conductor: step %q not found in definition %q", currentID, def.Workflow)
		}

		attemptNumber := 1
		if step.Budget != "" {
			budgetAttempts[step.Budget]++
			attemptNumber = budgetAttempts[step.Budget]
			budget := def.Budgets[step.Budget]

			exhausted := (budget.MaxAttempts > 0 && attemptNumber > budget.MaxAttempts) ||
				(budget.MaxRounds > 0 && attemptNumber > budget.MaxRounds) ||
				!gate.CheckTokenBudget(budgetTokensSpent[step.Budget], budget) ||
				!gate.CheckOscillation(step.ID, budgetHistory[step.Budget])

			if exhausted {
				dest, err := route(step, "budget_exhausted")
				if err != nil {
					return RunResult{}, fmt.Errorf("conductor: step %q: budget %q exhausted but %w", step.ID, step.Budget, err)
				}
				currentID = dest
				continue
			}
		}

		stepsVisited = append(stepsVisited, step.ID)

		activityIn := ActivityInput{
			StepID:        step.ID,
			Action:        step.Action,
			Role:          step.Role,
			Harness:       roleHarness(def, step.Role),
			Context:       stepContext(step, runContext),
			AttemptNumber: attemptNumber,
			RunID:         runID,
			Repo:          in.Repo,
			RunParams: map[string]any{
				"fail_verify_until_attempt": in.FailVerifyUntilAttempt,
			},
		}

		var out ActivityOutput
		activityName := activityNameFor(*step)
		if err := workflow.ExecuteActivity(actx, activityName, activityIn).Get(actx, &out); err != nil {
			return RunResult{}, fmt.Errorf("conductor: activity %q for step %q: %w", activityName, step.ID, err)
		}

		if step.Budget != "" {
			budgetTokensSpent[step.Budget] += out.TokensUsed
			budgetHistory[step.Budget] = append(budgetHistory[step.Budget], out)
		}

		for k, v := range out.Produced {
			runContext[k] = v
		}

		if out.Malformed {
			if step.OnMalformedOutput == "" {
				return RunResult{}, fmt.Errorf("conductor: step %q produced malformed output with no on_malformed_output handler", step.ID)
			}
			currentID = step.OnMalformedOutput
			continue
		}

		dest, err := route(step, out.Outcome)
		if err != nil {
			return RunResult{}, err
		}
		currentID = dest
	}
}

// route resolves a step's next destination. Steps with an unconditional
// `next:` ignore the Activity's reported outcome; steps with `on:` look
// the outcome up. Doc 02's rule 4 can only be statically checked where
// output_schema declares a verdict enum (see workflowdef's
// validateOutcomes) — this lookup is the runtime safety net for every
// other case: an outcome with no mapping hard-errors here rather than
// routing somewhere undefined.
func route(step *workflowdef.Step, outcome string) (string, error) {
	if step.Next != "" {
		return step.Next, nil
	}
	t, ok := step.On[outcome]
	if !ok {
		return "", fmt.Errorf("no on: mapping for outcome %q", outcome)
	}
	// TODO: dispatch t.Action for side-effecting targets (e.g.
	// task.create(...)) before routing — not exercised this slice since
	// only dependency-bump-minimal runs live, and it has no compound
	// targets.
	return t.Destination(), nil
}

func roleHarness(def workflowdef.Definition, roleName string) string {
	if roleName == "" {
		return ""
	}
	return def.Roles[roleName].Harness
}

// stepContext builds the Context an Activity call receives: the full
// accumulated Run context for type: tool steps (not token-constrained,
// may need infrastructure data no step declared — see ActivityInput.Context),
// or the pruned `context:`-declared subset for type: agent steps.
func stepContext(step *workflowdef.Step, runContext map[string]any) map[string]any {
	if step.Type != workflowdef.StepTypeAgent {
		out := make(map[string]any, len(runContext))
		for k, v := range runContext {
			out[k] = v
		}
		return out
	}
	return snapshotContext(runContext, step.Context)
}

// snapshotContext returns only the fields a step declared via `context:` —
// the conductor computes this diff deterministically so the agent never
// has to re-derive information a prior step already produced.
func snapshotContext(runContext map[string]any, fields []string) map[string]any {
	if len(fields) == 0 {
		return nil
	}
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		if v, ok := runContext[f]; ok {
			out[f] = v
		}
	}
	return out
}
