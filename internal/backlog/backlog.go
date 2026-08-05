// Package backlog implements task.create — the side-effecting Activity
// coder_response's out_of_scope compound target dispatches (doc 01: an
// out-of-scope review finding spawns a new backlog Task rather than being
// discarded). Writes into the same projection-store Postgres instance
// internal/eventlog uses (deploy/postgres-init/02-backlog.sql), not a
// separate store.
package backlog

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

// Registrations maps the task.create Activity name to this struct's
// method. Named to match the action identifier in a compound `on:`
// target's `action:` field verbatim (doc 02's reference example:
// `task.create(source=review-finding)`) — conductor's dispatchAction
// strips the "(...)" params before calling, so the registered name is
// just "task.create".
func (a *Activities) Registrations() map[string]any {
	return map[string]any{
		"task.create": a.CreateTask,
	}
}

// CreateTask records a new backlog Task and reports its id back into the
// Run's context as spawned_task_id — doc 01: "The current Run's finding
// status is recorded as rejected(out_of_scope, spawned=<new task id>)."
func (a *Activities) CreateTask(ctx context.Context, in conductor.ActivityInput) (conductor.ActivityOutput, error) {
	source, _ := in.Context["source"].(string)
	if source == "" {
		source = "unknown"
	}
	description, _ := in.Context["task_description"].(string)

	var taskID string
	err := a.Pool.QueryRow(ctx, `
		INSERT INTO backlog_tasks (task_id, run_id, source, description)
		VALUES (gen_random_uuid()::text, $1, $2, $3)
		RETURNING task_id
	`, in.RunID, source, description).Scan(&taskID)
	if err != nil {
		return conductor.ActivityOutput{}, fmt.Errorf("backlog: create task: %w", err)
	}

	return conductor.ActivityOutput{
		Produced: map[string]any{"spawned_task_id": taskID},
	}, nil
}
