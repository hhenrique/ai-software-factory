// Package eventlog writes structured Run state-transition events (docs/01)
// into the control plane's projection store — a separate database within
// the shared Postgres instance (docs/05), never Temporal's own execution
// history. This is the foundation the control plane's Overview reads are
// meant to be built on (docs/04/05: "do not make the UI query Temporal's
// raw workflow history directly for every Overview metric — project into
// the projection store").
package eventlog

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"factory/internal/conductor"
)

// RunEvent is the projection-store representation used by the Control
// Plane's run detail view. It deliberately carries the rendered summary
// rather than exposing the UI to the conductor's Produced map shape.
type RunEvent struct {
	ID            int64     `json:"id"`
	FromStep      string    `json:"from_step"`
	ToStep        string    `json:"to_step"`
	StepID        string    `json:"step_id,omitempty"`
	AttemptNumber int       `json:"attempt_number,omitempty"`
	TokenDelta    int       `json:"token_delta,omitempty"`
	ActivityCalls int       `json:"activity_calls,omitempty"`
	OccurredAt    time.Time `json:"occurred_at"`
	FailureReason string    `json:"failure_reason,omitempty"`
	Outcome       string    `json:"outcome,omitempty"`
	Summary       string    `json:"summary,omitempty"`
}

// ListRunEvents returns the append-only projection for one Run in display
// order. The UI uses this instead of making a human open Temporal just to
// understand the last few transitions.
func ListRunEvents(ctx context.Context, pool *pgxpool.Pool, runID string) ([]RunEvent, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, from_step, to_step, coalesce(step_id, ''), coalesce(attempt_number, 0),
		       token_delta, activity_calls, occurred_at, coalesce(failure_reason, ''),
		       coalesce(outcome, ''), produced
		FROM run_events
		WHERE run_id = $1
		ORDER BY occurred_at ASC, id ASC
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("eventlog: list run events: %w", err)
	}
	defer rows.Close()

	var out []RunEvent
	for rows.Next() {
		var ev RunEvent
		var producedJSON []byte
		if err := rows.Scan(&ev.ID, &ev.FromStep, &ev.ToStep, &ev.StepID, &ev.AttemptNumber,
			&ev.TokenDelta, &ev.ActivityCalls, &ev.OccurredAt, &ev.FailureReason,
			&ev.Outcome, &producedJSON); err != nil {
			return nil, fmt.Errorf("eventlog: list run events: scan: %w", err)
		}
		if len(producedJSON) > 0 {
			var produced map[string]any
			if err := json.Unmarshal(producedJSON, &produced); err != nil {
				return nil, fmt.Errorf("eventlog: list run events: unmarshal produced: %w", err)
			}
			ev.Summary = conductor.FormatEventContent(produced)
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("eventlog: list run events: %w", err)
	}
	return out, nil
}

// Activities holds the projection-store connection pool.
type Activities struct {
	Pool *pgxpool.Pool
}

// Registrations maps RecordEventActivityName to this struct's method —
// called directly by RunWorkflow (conductor/workflow.go), not dispatched
// via a Workflow Definition step the way tool/agent Activities are.
func (a *Activities) Registrations() map[string]any {
	return map[string]any{
		conductor.RecordEventActivityName: a.RecordEvent,
	}
}

// RecordEvent inserts one row per call — append-only, never updated. A
// Run's current state is "the to_step of its most recent event by
// occurred_at," not a separately maintained mutable row, so there's
// nothing to reconcile if a retry double-records (an extra row with
// identical from_step/to_step doesn't change what "most recent" resolves
// to for a caller reading run_events, since it's still ordered last).
//
// ev.Produced is marshaled to JSON before insert — pgx sends a []byte
// value for a jsonb column as-is, no separate pgtype wrapper needed. nil
// (an empty/absent map) stays NULL rather than the literal "null" or "{}",
// consistent with outcome/failure_reason's NULLIF-on-empty-string
// convention for "nothing to show" below.
func (a *Activities) RecordEvent(ctx context.Context, ev conductor.TransitionEvent) error {
	var producedJSON []byte
	if len(ev.Produced) > 0 {
		b, err := json.Marshal(ev.Produced)
		if err != nil {
			return fmt.Errorf("eventlog: record event: marshal produced: %w", err)
		}
		producedJSON = b
	}

	_, err := a.Pool.Exec(ctx, `
		INSERT INTO run_events
			(run_id, workflow, from_step, to_step, step_id, attempt_number, token_delta, activity_calls, failure_reason, outcome, produced)
		VALUES
			($1, $2, $3, $4, NULLIF($5, ''), $6, $7, $8, NULLIF($9, ''), NULLIF($10, ''), $11)
	`, ev.RunID, ev.Workflow, ev.FromStep, ev.ToStep, ev.StepID, ev.AttemptNumber, ev.TokenDelta, ev.ActivityCalls, ev.FailureReason, ev.Outcome, producedJSON)
	if err != nil {
		return fmt.Errorf("eventlog: record event: %w", err)
	}
	return nil
}
