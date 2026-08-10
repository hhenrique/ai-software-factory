package conductor

import (
	"fmt"
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"factory/internal/harnesslimits"
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

	// A separate, shorter-timeout/retried ActivityOptions for event
	// recording (docs/01) — telemetry, not the Run's actual work, so it
	// gets its own small retry budget rather than sharing the main
	// MaximumAttempts:1 policy above (a transient projection-store
	// hiccup shouldn't behave like a real step failure).
	eventAO := workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	}
	eventActx := workflow.WithActivityOptions(ctx, eventAO)

	// Temporal's own WorkflowExecution.ID is the Run id (doc 05: "Run →
	// Temporal workflow execution") — no separate RunID field needed on
	// RunInput.
	runID := workflow.GetInfo(ctx).WorkflowExecution.ID

	var gate BudgetGate = RealBudgetGate{}

	runContext := make(map[string]any, len(in.InitialContext))
	for k, v := range in.InitialContext {
		runContext[k] = v
	}

	budgetAttempts := make(map[string]int)
	budgetTokensSpent := make(map[string]int)
	budgetHistory := make(map[string][]ActivityOutput)

	// harnessTokensSpent tracks cumulative token spend per (harness, model,
	// effort) combination for the whole Run — decoupled from step.Budget
	// (which is per named budget block/loop) and from role (two steps with
	// different roles that share a (harness, model, effort) combination
	// share this counter too). See in.HarnessLimits's doc comment.
	harnessTokensSpent := make(map[string]int)
	var stepsVisited []string

	// recordT/recordF close over runID/def.Workflow/in.SourceRef/runContext
	// so every call site below stays terse. ev.Outcome/ev.Produced (set by
	// the caller where meaningful) are what both the projection store
	// (internal/eventlog) and doc08's best-effort tracker mirror read —
	// see recordTransition.
	recordT := func(ev TransitionEvent) {
		recordTransition(eventActx, ev, in.SourceRef, runContext)
	}
	recordF := func(fromStep string, err error) (RunResult, error) {
		return recordFailure(eventActx, runID, def.Workflow, fromStep, err, in.SourceRef, runContext)
	}

	recordT(TransitionEvent{RunID: runID, Workflow: def.Workflow, FromStep: "", ToStep: startID})

	currentID := startID
	for {
		if currentID == "REVIEW_PENDING" {
			decision, err := waitForHumanDecision(ctx)
			if err != nil {
				return recordF(currentID, err)
			}

			if decision.Action == "cancel" {
				recordT(TransitionEvent{
					RunID: runID, Workflow: def.Workflow, FromStep: "REVIEW_PENDING", ToStep: "CANCELLED",
				})
				return RunResult{
					FinalState:   "CANCELLED",
					StepsVisited: stepsVisited,
					BudgetSpent:  budgetAttempts,
					FinalContext: runContext,
				}, nil
			}

			// Resume: doc 01 — "all of the Run's budget counters reset to
			// zero-spent, not just the one tied to whichever loop
			// escalated." A partial reset would leave stale counters in
			// loops the hint never touched.
			budgetAttempts = make(map[string]int)
			budgetTokensSpent = make(map[string]int)
			budgetHistory = make(map[string][]ActivityOutput)
			harnessTokensSpent = make(map[string]int)
			if decision.Hint != "" {
				runContext["human_hint"] = decision.Hint
			}
			recordT(TransitionEvent{
				RunID: runID, Workflow: def.Workflow, FromStep: "REVIEW_PENDING", ToStep: decision.ResumeStepID,
			})
			currentID = decision.ResumeStepID
			continue
		}

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
			return recordF(currentID,
				fmt.Errorf("conductor: step %q not found in definition %q", currentID, def.Workflow))
		}

		harness, model, params := roleConfig(in.RoleAssignments, step.Role)

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
				dest, err := route(actx, runID, step, "budget_exhausted", runContext)
				if err != nil {
					return recordF(step.ID,
						fmt.Errorf("conductor: step %q: budget %q exhausted but %w", step.ID, step.Budget, err))
				}
				recordT(TransitionEvent{
					RunID: runID, Workflow: def.Workflow, FromStep: step.ID, ToStep: dest,
					StepID: step.ID, AttemptNumber: attemptNumber, Outcome: "budget_exhausted",
				})
				currentID = dest
				continue
			}
		}

		// Harness/model/effort circuit breaker (decoupled from step.Budget
		// and from role — see in.HarnessLimits's doc comment). Unlike the
		// named-budget check above, there's no step-declared `on:` outcome
		// for this, so a trip routes unconditionally to REVIEW_PENDING, the
		// same conservative "escalate to a human" pattern doc 01 uses for
		// every other exhaustion case.
		if step.Type == workflowdef.StepTypeAgent {
			limitKey := harnesslimits.Key(harness, model, params["effort"])
			if limit, ok := in.HarnessLimits[limitKey]; ok && harnessTokensSpent[limitKey] > limit {
				recordT(TransitionEvent{
					RunID: runID, Workflow: def.Workflow, FromStep: step.ID, ToStep: "REVIEW_PENDING",
					StepID: step.ID, AttemptNumber: attemptNumber, Outcome: "harness_limit_exceeded",
				})
				currentID = "REVIEW_PENDING"
				continue
			}
		}

		stepsVisited = append(stepsVisited, step.ID)

		activityIn := ActivityInput{
			StepID:        step.ID,
			Action:        step.Action,
			Role:          step.Role,
			Harness:       harness,
			Model:         model,
			Params:        params,
			OutputSchema:  step.OutputSchema,
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
			return recordF(step.ID,
				fmt.Errorf("conductor: activity %q for step %q: %w", activityName, step.ID, err))
		}

		if step.Budget != "" {
			budgetTokensSpent[step.Budget] += out.TokensUsed
			budgetHistory[step.Budget] = append(budgetHistory[step.Budget], out)
		}
		if step.Type == workflowdef.StepTypeAgent {
			harnessTokensSpent[harnesslimits.Key(harness, model, params["effort"])] += out.TokensUsed
		}

		for k, v := range out.Produced {
			runContext[k] = v
		}

		if out.Malformed {
			if step.OnMalformedOutput == "" {
				return recordF(step.ID,
					fmt.Errorf("conductor: step %q produced malformed output with no on_malformed_output handler", step.ID))
			}
			recordT(TransitionEvent{
				RunID: runID, Workflow: def.Workflow, FromStep: step.ID, ToStep: step.OnMalformedOutput,
				StepID: step.ID, AttemptNumber: attemptNumber, TokenDelta: out.TokensUsed, ActivityCalls: 1,
				Outcome: "malformed_output", Produced: out.Produced, AgentStep: step.Type == workflowdef.StepTypeAgent,
			})
			currentID = step.OnMalformedOutput
			continue
		}

		dest, err := route(actx, runID, step, out.Outcome, runContext)
		if err != nil {
			return recordF(step.ID, err)
		}
		recordT(TransitionEvent{
			RunID: runID, Workflow: def.Workflow, FromStep: step.ID, ToStep: dest,
			StepID: step.ID, AttemptNumber: attemptNumber, TokenDelta: out.TokensUsed, ActivityCalls: 1,
			Outcome: out.Outcome, Produced: out.Produced, AgentStep: step.Type == workflowdef.StepTypeAgent,
		})
		currentID = dest
	}
}

// recordTransition emits one structured event (docs/01) via
// RecordEventActivityName — including ev.Outcome/ev.Produced, so the
// projection store carries the same verdict/scope_contract/findings/diff
// content a control-plane surface (internal/inbox, internal/backlog) can
// later render without needing Temporal's raw history — then best-effort
// mirrors it onto whatever external tracker(s) are resolved for this Run
// (docs/08 — see postTrackerComments). Both are best-effort: a
// projection-store hiccup or a comment-post failure must not fail the Run
// itself, so an error from either is logged, not propagated — unlike
// every step Activity's own errors, which do fail the Run.
func recordTransition(ctx workflow.Context, ev TransitionEvent, sourceRef SourceRef, runContext map[string]any) {
	if err := workflow.ExecuteActivity(ctx, RecordEventActivityName, ev).Get(ctx, nil); err != nil {
		workflow.GetLogger(ctx).Warn("conductor: failed to record transition event",
			"error", err, "run_id", ev.RunID, "from_step", ev.FromStep, "to_step", ev.ToStep)
	}
	postTrackerComments(ctx, sourceRef, runContext, ev)
}

// recordFailure records a FAILED transition event before returning err as
// RunWorkflow's own result — doc 01: "every state transition emits a
// structured event... including Runs that fail before any model call."
// Every hard failure in RunWorkflow's main loop returns through this
// (never a bare `return RunResult{}, err`), so a Run that fails outright —
// an Activity error, an unroutable outcome, a malformed-output step with
// no handler — leaves the same structured trace in run_events a normal
// terminal state would, instead of silently ending with only Temporal's
// own workflow history to explain what happened.
func recordFailure(ctx workflow.Context, runID, workflowName, fromStep string, err error, sourceRef SourceRef, runContext map[string]any) (RunResult, error) {
	recordTransition(ctx, TransitionEvent{
		RunID: runID, Workflow: workflowName, FromStep: fromStep, ToStep: "FAILED",
		FailureReason: err.Error(),
	}, sourceRef, runContext)
	return RunResult{}, err
}

// route resolves a step's next destination. Steps with an unconditional
// `next:` ignore the Activity's reported outcome; steps with `on:` look
// the outcome up. Doc 02's rule 4 can only be statically checked where
// output_schema declares a verdict enum (see workflowdef's
// validateOutcomes) — this lookup is the runtime safety net for every
// other case: an outcome with no mapping hard-errors here rather than
// routing somewhere undefined.
//
// A compound target (doc 02: `{ action: ..., next: ... }`, e.g.
// coder_response's out_of_scope) dispatches its action as an ordinary
// Activity call before routing — a side effect, so it needs actx/runID
// and gets to read/merge into runContext the same way a step's own
// Activity call does.
func route(actx workflow.Context, runID string, step *workflowdef.Step, outcome string, runContext map[string]any) (string, error) {
	if step.Next != "" {
		return step.Next, nil
	}
	t, ok := step.On[outcome]
	if !ok {
		return "", fmt.Errorf("no on: mapping for outcome %q", outcome)
	}
	if t.HasSideEffect() {
		if err := dispatchAction(actx, runID, step.ID, t.Action, runContext); err != nil {
			return "", fmt.Errorf("step %q: side-effecting action %q: %w", step.ID, t.Action, err)
		}
	}
	return t.Destination(), nil
}

// dispatchAction runs a compound target's action string (e.g.
// "task.create(source=review-finding)") as an Activity call, merging its
// Produced fields into runContext the same way a step's own output is
// merged. The only action this package knows about today is task.create —
// doc 04's Work section (a real Task/backlog entity) is otherwise
// unbuilt, so this is a minimal real implementation (a projection-store
// row via internal/backlog), not a placeholder: it genuinely records the
// out-of-scope finding as a queryable backlog item, just without the rest
// of doc 04's Task machinery (priority, assigned Workflow, a UI to triage
// it) built out yet.
func dispatchAction(actx workflow.Context, runID, stepID, action string, runContext map[string]any) error {
	name, params := parseActionCall(action)

	activityIn := ActivityInput{
		StepID: stepID,
		RunID:  runID,
		Context: map[string]any{
			"source":           params["source"],
			"task_description": runContext["task_description"],
			"findings":         runContext["findings"],
		},
	}

	var out ActivityOutput
	if err := workflow.ExecuteActivity(actx, name, activityIn).Get(actx, &out); err != nil {
		return err
	}
	for k, v := range out.Produced {
		runContext[k] = v
	}
	return nil
}

// parseActionCall parses "name(key=value, key2=value2)" into its name and
// param map. Doc 02's only example is "task.create(source=review-finding)" —
// this is a minimal parser for exactly that shape, not a general
// expression language.
func parseActionCall(s string) (name string, params map[string]string) {
	open := strings.Index(s, "(")
	if open < 0 {
		return s, nil
	}
	name = s[:open]
	inner := strings.TrimSuffix(s[open+1:], ")")
	params = map[string]string{}
	if inner == "" {
		return name, params
	}
	for _, part := range strings.Split(inner, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 {
			params[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	return name, params
}

// waitForHumanDecision blocks on HumanDecisionSignalName — doc 05's
// signal-wait, preferred over polling for REVIEW_PENDING. Indefinite:
// there's no timeout, since "wait for a human" has no natural deadline,
// and a durable Temporal signal-wait costs nothing while parked (unlike a
// polling loop would).
func waitForHumanDecision(ctx workflow.Context) (HumanDecision, error) {
	var decision HumanDecision
	workflow.GetSignalChannel(ctx, HumanDecisionSignalName).Receive(ctx, &decision)
	if decision.Action == "resume" && decision.ResumeStepID == "" {
		return HumanDecision{}, fmt.Errorf("conductor: human_decision signal: action=resume requires resume_step_id")
	}
	return decision, nil
}

func roleConfig(assignments map[string]workflowdef.Role, roleName string) (harness, model string, params map[string]string) {
	if roleName == "" {
		return "", "", nil
	}
	r := assignments[roleName]
	return r.Harness, r.Model, r.Params
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
