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
//
// List excludes outcome "proceed" — a routine drafted plan awaiting
// approval (docs/01's mandatory plan-approval gate) is a different kind
// of queue from this one's exceptions (escalations, malformed output,
// budget/harness-limit exhaustion, disputed findings): see
// ListPendingApprovals and 04-control-plane-mvp-scope.md's "Pending
// Approvals" section for why they're split rather than merged.
package inbox

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/client"

	"factory/internal/conductor"
)

// PendingRun is one Run currently parked at REVIEW_PENDING, with just
// enough context (which step escalated, on what attempt, how long ago,
// and — see Outcome/Summary — why) for a human to decide what to do
// without leaving this view.
type PendingRun struct {
	RunID         string    `json:"run_id"`
	Workflow      string    `json:"workflow"`
	FromStep      string    `json:"from_step"`
	StepID        string    `json:"step_id,omitempty"`
	AttemptNumber int       `json:"attempt_number,omitempty"`
	OccurredAt    time.Time `json:"occurred_at"`

	// Outcome is the routing signal that landed this Run here — an
	// agent's verdict ("escalate", "changes_required"), or a synthetic
	// label ("malformed_output", "budget_exhausted",
	// "harness_limit_exceeded") for the escalation kinds that aren't a
	// verdict at all. "" for the rare case a resume immediately
	// re-escalated with no Activity call in between.
	Outcome string `json:"outcome,omitempty"`

	// Summary renders the escalating transition's Produced content
	// (verdict, scope_contract, findings, a diff summary) into a compact
	// human-readable block — the same v1 content doc08's tracker mirror
	// already posts externally (conductor.FormatEventContent), now
	// visible here too instead of requiring a read of Temporal's raw
	// history to find out why a Run escalated.
	Summary string `json:"summary,omitempty"`
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
	return queryPendingRuns(ctx, pool, "latest.outcome IS DISTINCT FROM 'proceed'")
}

// ListPendingApprovals is List's mirror image — every Run parked at
// REVIEW_PENDING specifically because a drafted plan is awaiting human
// approval (outcome "proceed"), docs/01's mandatory plan-approval gate.
// Same PendingRun shape: FromStep is the planning step that produced the
// plan, Summary is the same rendered assessment/scope_contract text a
// human reviews before approving or requesting changes.
func ListPendingApprovals(ctx context.Context, pool *pgxpool.Pool) ([]PendingRun, error) {
	return queryPendingRuns(ctx, pool, "latest.outcome = 'proceed'")
}

// queryPendingRuns is List/ListPendingApprovals' shared query, split only
// by which side of the outcome = 'proceed' line they want — everything
// else (latest-transition-per-run, REVIEW_PENDING filter, ordering,
// Summary rendering) is identical.
func queryPendingRuns(ctx context.Context, pool *pgxpool.Pool, outcomeFilter string) ([]PendingRun, error) {
	rows, err := pool.Query(ctx, `
		SELECT run_id, workflow, from_step, coalesce(step_id, ''), coalesce(attempt_number, 0), occurred_at,
		       coalesce(outcome, ''), produced
		FROM (
			SELECT DISTINCT ON (run_id)
				run_id, workflow, from_step, to_step, step_id, attempt_number, occurred_at, outcome, produced
			FROM run_events
			ORDER BY run_id, occurred_at DESC, id DESC
		) latest
		WHERE latest.to_step = 'REVIEW_PENDING' AND `+outcomeFilter+`
		ORDER BY occurred_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("inbox: list: %w", err)
	}
	defer rows.Close()

	var out []PendingRun
	for rows.Next() {
		var p PendingRun
		var producedJSON []byte
		if err := rows.Scan(&p.RunID, &p.Workflow, &p.FromStep, &p.StepID, &p.AttemptNumber, &p.OccurredAt,
			&p.Outcome, &producedJSON); err != nil {
			return nil, fmt.Errorf("inbox: list: scan: %w", err)
		}
		if len(producedJSON) > 0 {
			var produced map[string]any
			if err := json.Unmarshal(producedJSON, &produced); err != nil {
				return nil, fmt.Errorf("inbox: list: unmarshal produced for run %q: %w", p.RunID, err)
			}
			p.Summary = conductor.FormatEventContent(produced)
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
