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
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"factory/internal/conductor"
)

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
func (a *Activities) RecordEvent(ctx context.Context, ev conductor.TransitionEvent) error {
	_, err := a.Pool.Exec(ctx, `
		INSERT INTO run_events
			(run_id, workflow, from_step, to_step, step_id, attempt_number, token_delta, activity_calls, failure_reason)
		VALUES
			($1, $2, $3, $4, NULLIF($5, ''), $6, $7, $8, NULLIF($9, ''))
	`, ev.RunID, ev.Workflow, ev.FromStep, ev.ToStep, ev.StepID, ev.AttemptNumber, ev.TokenDelta, ev.ActivityCalls, ev.FailureReason)
	if err != nil {
		return fmt.Errorf("eventlog: record event: %w", err)
	}
	return nil
}
