// Package inbox is docs/01's "a human resolves a Run parked at
// REVIEW_PENDING" made queryable and actionable from outside the Run
// itself: List finds every Run currently sitting there, SignalResume/
// SignalCancel send the same conductor.HumanDecision signal a human's
// resume/cancel action was already defined to accept (see
// internal/conductor/types.go) — this package doesn't invent a new
// resume mechanism, it's the first real caller of the one that already
// existed, from outside the process that started the Run.
//
// Deliberately narrow: this is not a general Runs browser (that's
// cmd/runsview's job, staying as-is) — just the one thing genuinely
// blocked on a human, which is what "Inbox" means here.
package inbox

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/client"

	"factory/internal/conductor"
)

// PendingRun is one Run currently parked at REVIEW_PENDING, with just
// enough context (which step escalated, on what attempt, how long ago)
// for a human to decide what to do without leaving this view.
type PendingRun struct {
	RunID         string    `json:"run_id"`
	Workflow      string    `json:"workflow"`
	FromStep      string    `json:"from_step"`
	StepID        string    `json:"step_id,omitempty"`
	AttemptNumber int       `json:"attempt_number,omitempty"`
	OccurredAt    time.Time `json:"occurred_at"`
}

// List returns every Run whose most recent transition landed on
// REVIEW_PENDING, oldest first (classic inbox/triage ordering — the
// longest-waiting item surfaces first). "Most recent transition" is
// computed fresh each call the same way cmd/runsview's list does
// (DISTINCT ON run_id, latest by occurred_at/id) — a Run resumed and
// later re-escalated shows up again naturally, and one that moved past
// REVIEW_PENDING (or was cancelled) drops out, with no separate status
// field to keep in sync.
func List(ctx context.Context, pool *pgxpool.Pool) ([]PendingRun, error) {
	rows, err := pool.Query(ctx, `
		SELECT run_id, workflow, from_step, coalesce(step_id, ''), coalesce(attempt_number, 0), occurred_at
		FROM (
			SELECT DISTINCT ON (run_id)
				run_id, workflow, from_step, to_step, step_id, attempt_number, occurred_at
			FROM run_events
			ORDER BY run_id, occurred_at DESC, id DESC
		) latest
		WHERE to_step = 'REVIEW_PENDING'
		ORDER BY occurred_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("inbox: list: %w", err)
	}
	defer rows.Close()

	var out []PendingRun
	for rows.Next() {
		var p PendingRun
		if err := rows.Scan(&p.RunID, &p.Workflow, &p.FromStep, &p.StepID, &p.AttemptNumber, &p.OccurredAt); err != nil {
			return nil, fmt.Errorf("inbox: list: scan: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inbox: list: %w", err)
	}
	return out, nil
}

// SignalResume sends the same conductor.HumanDecision{Action: "resume"}
// signal doc 01 always expected a human to send — resumeStepID is
// required (RunWorkflow itself rejects a resume with no step id; failing
// here is just doing that check without a round trip to Temporal first).
// Passing "" for Temporal's own runID parameter targets the current/only
// execution of workflowID, which is what this system always has (see
// client.Client.SignalWorkflow's doc comment).
func SignalResume(ctx context.Context, c client.Client, runID, resumeStepID, hint string) error {
	if resumeStepID == "" {
		return fmt.Errorf("inbox: resume requires a resume_step_id")
	}
	err := c.SignalWorkflow(ctx, runID, "", conductor.HumanDecisionSignalName, conductor.HumanDecision{
		Action:       "resume",
		ResumeStepID: resumeStepID,
		Hint:         hint,
	})
	if err != nil {
		return fmt.Errorf("inbox: signal resume %q: %w", runID, err)
	}
	return nil
}

// SignalCancel sends conductor.HumanDecision{Action: "cancel"}.
func SignalCancel(ctx context.Context, c client.Client, runID string) error {
	err := c.SignalWorkflow(ctx, runID, "", conductor.HumanDecisionSignalName, conductor.HumanDecision{Action: "cancel"})
	if err != nil {
		return fmt.Errorf("inbox: signal cancel %q: %w", runID, err)
	}
	return nil
}
