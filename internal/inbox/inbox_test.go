package inbox

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"factory/internal/activities/stub"
	"factory/internal/conductor"
	"factory/internal/eventlog"
	"factory/internal/temporalconn"
	"factory/internal/workflowdef"
)

func requirePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pool, err := eventlog.NewPool(ctx)
	if err != nil {
		t.Skip("projection store not configured:", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skip("projection store not reachable (is `docker compose up` running?):", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func requireTemporal(t *testing.T) client.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := temporalconn.DialWithRetry(ctx, "localhost:7233", "default", 3, 500*time.Millisecond)
	if err != nil {
		t.Skip("Temporal not reachable (is `docker compose up` running?):", err)
	}
	t.Cleanup(c.Close)
	return c
}

// startTestWorker runs a real worker against a real Temporal server, on
// its own task queue so it never competes with cmd/worker or other tests.
// Every Activity is the stub (internal/activities/stub) — no real harness
// call, no cost — plus internal/eventlog's real RecordEvent so run_events
// actually gets the transitions List queries against. This is what makes
// SignalResume/SignalCancel testable for real: without an actual worker
// executing conductor.RunWorkflow, there's no live signal channel for
// client.SignalWorkflow to reach.
func startTestWorker(t *testing.T, c client.Client, pool *pgxpool.Pool, taskQueue string) {
	t.Helper()
	w := worker.New(c, taskQueue, worker.Options{})
	w.RegisterWorkflow(conductor.RunWorkflow)

	eventActivities := &eventlog.Activities{Pool: pool}
	for name, fn := range eventActivities.Registrations() {
		w.RegisterActivityWithOptions(fn, activity.RegisterOptions{Name: name, DisableAlreadyRegisteredCheck: true})
	}
	for name, fn := range stub.Registrations {
		w.RegisterActivityWithOptions(fn, activity.RegisterOptions{Name: name, DisableAlreadyRegisteredCheck: true})
	}

	if err := w.Start(); err != nil {
		t.Fatalf("start test worker: %v", err)
	}
	t.Cleanup(w.Stop)
}

// waitForReviewPending polls run_events (via List) until runID appears,
// bounded — RunWorkflow's step transitions are async relative to this
// test process, so a fixed sleep would be both slow and flaky.
func waitForReviewPending(t *testing.T, pool *pgxpool.Pool, runID string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		pending, err := List(context.Background(), pool)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, p := range pending {
			if p.RunID == runID {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("run %q never reached REVIEW_PENDING within the deadline", runID)
}

func reviewPendingTestDefinition(workflowName string) workflowdef.Definition {
	return workflowdef.Definition{
		Workflow: workflowName,
		Version:  1,
		Steps: []workflowdef.Step{
			{
				ID: "verify", Type: workflowdef.StepTypeTool, Action: "run.tests_lint_build",
				On: map[string]workflowdef.Target{
					"pass": {StepOrState: "COMPLETED"},
					"fail": {StepOrState: "REVIEW_PENDING"},
				},
			},
		},
	}
}

func TestListFindsARunParkedAtReviewPending(t *testing.T) {
	pool := requirePool(t)
	c := requireTemporal(t)
	taskQueue := "inbox-test-" + time.Now().Format("20060102T150405.000000000")
	startTestWorker(t, c, pool, taskQueue)

	runID := "inbox-list-test-" + time.Now().Format("20060102T150405.000000000")
	def := reviewPendingTestDefinition("inbox-list-test")
	run, err := c.ExecuteWorkflow(context.Background(), client.StartWorkflowOptions{ID: runID, TaskQueue: taskQueue},
		conductor.RunWorkflow, conductor.RunInput{Definition: def, FailVerifyUntilAttempt: 1})
	if err != nil {
		t.Fatalf("ExecuteWorkflow: %v", err)
	}

	waitForReviewPending(t, pool, runID)

	pending, err := List(context.Background(), pool)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var found *PendingRun
	for i := range pending {
		if pending[i].RunID == runID {
			found = &pending[i]
		}
	}
	if found == nil {
		t.Fatalf("List did not include %q: %+v", runID, pending)
	}
	if found.Workflow != "inbox-list-test" {
		t.Errorf("Workflow = %q", found.Workflow)
	}
	if found.FromStep != "verify" {
		t.Errorf("FromStep = %q, want verify", found.FromStep)
	}
	// stub.RunTestsLintBuild reports Outcome: "fail" while
	// AttemptNumber <= FailVerifyUntilAttempt — this is the routing
	// signal that actually landed the Run here, now visible without
	// reading Temporal's raw history to find it.
	if found.Outcome != "fail" {
		t.Errorf("Outcome = %q, want fail", found.Outcome)
	}

	// Cleanup: cancel and wait for it to actually take effect before the
	// test worker stops polling — a fire-and-forget signal here can leave
	// the execution genuinely stuck open in Temporal forever (nothing else
	// will ever deliver it once this test's worker is gone).
	if err := SignalCancel(context.Background(), c, runID); err != nil {
		t.Logf("cleanup SignalCancel: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := run.Get(ctx, &conductor.RunResult{}); err != nil {
		t.Logf("cleanup: waiting for cancellation: %v", err)
	}
}

// TestListSurfacesOutcomeAndSummaryFromLatestEvent is a hermetic
// regression guard (a direct run_events insert, no real Temporal
// execution needed) for the actual gap this closes: a human looking at
// the Inbox used to see only "escalated from plan," nothing about *why*
// — the Planner's verdict and scope_contract were only visible by reading
// Temporal's raw Activity history by hand. List must now surface them.
func TestListSurfacesOutcomeAndSummaryFromLatestEvent(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()

	runID := "inbox-summary-test-" + time.Now().Format(time.RFC3339Nano)
	// This row lands in the real, live Inbox view the moment it's
	// inserted (to_step = REVIEW_PENDING is exactly List's WHERE clause)
	// — must be cleaned up like any other user-visible test row.
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM run_events WHERE run_id = $1`, runID)
	})
	produced := map[string]any{
		"verdict":        "escalate",
		"scope_contract": map[string]any{"acceptance_criteria": []any{"a", "b"}},
	}
	producedJSON, err := json.Marshal(produced)
	if err != nil {
		t.Fatalf("marshal produced: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO run_events (run_id, workflow, from_step, to_step, occurred_at, outcome, produced)
		VALUES ($1, 'issue-to-pr-claude-only', 'plan', 'REVIEW_PENDING', now(), 'escalate', $2)
	`, runID, producedJSON); err != nil {
		t.Fatalf("insert run_events: %v", err)
	}

	pending, err := List(ctx, pool)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var found *PendingRun
	for i := range pending {
		if pending[i].RunID == runID {
			found = &pending[i]
		}
	}
	if found == nil {
		t.Fatalf("List did not include %q", runID)
	}
	if found.Outcome != "escalate" {
		t.Errorf("Outcome = %q, want escalate", found.Outcome)
	}
	if !strings.Contains(found.Summary, "Verdict: escalate") {
		t.Errorf("Summary = %q, want it to contain the verdict", found.Summary)
	}
	if !strings.Contains(found.Summary, "acceptance_criteria: a; b") {
		t.Errorf("Summary = %q, want it to contain the scope contract", found.Summary)
	}
}

// TestListExcludesPendingApprovalsAndViceVersa is the split docs/01's
// mandatory plan-approval gate needs: a routine drafted plan (outcome
// "proceed") must not clutter the Inbox's exceptions-only list, and a
// real escalation must not show up in the approvals queue. Two direct
// run_events inserts, no real Temporal execution needed — same hermetic
// pattern as TestListSurfacesOutcomeAndSummaryFromLatestEvent.
func TestListExcludesPendingApprovalsAndViceVersa(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()

	approvalRunID := "inbox-split-approval-" + time.Now().Format(time.RFC3339Nano)
	escalationRunID := "inbox-split-escalation-" + time.Now().Format(time.RFC3339Nano)
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM run_events WHERE run_id = $1 OR run_id = $2`, approvalRunID, escalationRunID)
	})

	insert := func(runID, outcome string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO run_events (run_id, workflow, from_step, to_step, occurred_at, outcome)
			VALUES ($1, 'issue-to-pr-claude-only', 'plan', 'REVIEW_PENDING', now(), $2)
		`, runID, outcome); err != nil {
			t.Fatalf("insert run_events for %q: %v", runID, err)
		}
	}
	insert(approvalRunID, "proceed")
	insert(escalationRunID, "escalate")

	contains := func(items []PendingRun, runID string) bool {
		for _, p := range items {
			if p.RunID == runID {
				return true
			}
		}
		return false
	}

	inboxItems, err := List(ctx, pool)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if contains(inboxItems, approvalRunID) {
		t.Errorf("List (Inbox) included the pending-approval run %q, want it excluded", approvalRunID)
	}
	if !contains(inboxItems, escalationRunID) {
		t.Errorf("List (Inbox) did not include the escalation run %q", escalationRunID)
	}

	approvalItems, err := ListPendingApprovals(ctx, pool)
	if err != nil {
		t.Fatalf("ListPendingApprovals: %v", err)
	}
	if !contains(approvalItems, approvalRunID) {
		t.Errorf("ListPendingApprovals did not include %q", approvalRunID)
	}
	if contains(approvalItems, escalationRunID) {
		t.Errorf("ListPendingApprovals included the escalation run %q, want it excluded", escalationRunID)
	}
}

func TestSignalCancelUnblocksARun(t *testing.T) {
	pool := requirePool(t)
	c := requireTemporal(t)
	taskQueue := "inbox-test-" + time.Now().Format("20060102T150405.000000000")
	startTestWorker(t, c, pool, taskQueue)

	runID := "inbox-cancel-test-" + time.Now().Format("20060102T150405.000000000")
	def := reviewPendingTestDefinition("inbox-cancel-test")
	run, err := c.ExecuteWorkflow(context.Background(), client.StartWorkflowOptions{ID: runID, TaskQueue: taskQueue},
		conductor.RunWorkflow, conductor.RunInput{Definition: def, FailVerifyUntilAttempt: 1})
	if err != nil {
		t.Fatalf("ExecuteWorkflow: %v", err)
	}
	waitForReviewPending(t, pool, runID)

	if err := SignalCancel(context.Background(), c, runID); err != nil {
		t.Fatalf("SignalCancel: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var result conductor.RunResult
	if err := run.Get(ctx, &result); err != nil {
		t.Fatalf("workflow execution error: %v", err)
	}
	if result.FinalState != "CANCELLED" {
		t.Errorf("FinalState = %q, want CANCELLED", result.FinalState)
	}
}

func TestSignalResumeUnblocksARunAtTheNamedStep(t *testing.T) {
	pool := requirePool(t)
	c := requireTemporal(t)
	taskQueue := "inbox-test-" + time.Now().Format("20060102T150405.000000000")
	startTestWorker(t, c, pool, taskQueue)

	runID := "inbox-resume-test-" + time.Now().Format("20060102T150405.000000000")
	def := reviewPendingTestDefinition("inbox-resume-test")
	// "verify" carries no budget, so its attempt number never advances
	// past 1 — with FailVerifyUntilAttempt: 1, every entry into it fails
	// and re-escalates to REVIEW_PENDING deterministically, including
	// after a resume. That's exactly what this test needs: proof
	// SignalResume genuinely re-enters the named step (a fresh
	// transition out of REVIEW_PENDING), not that the Run eventually
	// completes.
	run, err := c.ExecuteWorkflow(context.Background(), client.StartWorkflowOptions{ID: runID, TaskQueue: taskQueue},
		conductor.RunWorkflow, conductor.RunInput{Definition: def, FailVerifyUntilAttempt: 1})
	if err != nil {
		t.Fatalf("ExecuteWorkflow: %v", err)
	}
	waitForReviewPending(t, pool, runID)

	if err := SignalResume(context.Background(), c, runID, "verify", "try again"); err != nil {
		t.Fatalf("SignalResume: %v", err)
	}

	// Whatever happens next (pass or another fail-and-reescalate), the
	// Run must have left REVIEW_PENDING and recorded a fresh transition
	// out of it — that's what SignalResume is responsible for, not the
	// eventual terminal state.
	deadline := time.Now().Add(10 * time.Second)
	var sawResumeTransition bool
	for time.Now().Before(deadline) {
		var fromStep string
		err := pool.QueryRow(context.Background(), `
			SELECT from_step FROM run_events
			WHERE run_id = $1 AND from_step = 'REVIEW_PENDING'
			ORDER BY id DESC LIMIT 1
		`, runID).Scan(&fromStep)
		if err == nil && fromStep == "REVIEW_PENDING" {
			sawResumeTransition = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !sawResumeTransition {
		t.Fatalf("run %q never recorded a transition out of REVIEW_PENDING after resume", runID)
	}

	// Clean up regardless of how it's now looping.
	if err := SignalCancel(context.Background(), c, runID); err != nil {
		t.Logf("cleanup SignalCancel: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = run.Get(ctx, &conductor.RunResult{})
}

func TestSignalResumeRequiresResumeStepID(t *testing.T) {
	c := requireTemporal(t)
	if err := SignalResume(context.Background(), c, "does-not-matter", "", "hint"); err == nil {
		t.Fatalf("expected an error for an empty resumeStepID")
	}
}
