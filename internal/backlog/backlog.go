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
	"time"

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

// InsertHumanTask records a manually-submitted Task (source: "human")
// before its Run starts — called directly by whatever intake surface
// accepts one (cmd/submittask), not via an Activity: unlike CreateTask
// above (dispatched from inside a running Temporal workflow, which cannot
// do direct I/O — see conductor.dispatchAction), this runs in an ordinary
// Go process with a plain Postgres connection. run_id starts NULL: there's
// no decoupled scheduler (doc 00's "bespoke scheduling engine" is
// explicitly deferred), so a submitted Task's Run is started immediately
// after this call, not queued for later pickup — see AttachRun.
func InsertHumanTask(ctx context.Context, pool *pgxpool.Pool, targetRepo, workflow, description string) (taskID string, err error) {
	err = pool.QueryRow(ctx, `
		INSERT INTO backlog_tasks (task_id, source, target_repo, workflow, description, status)
		VALUES (gen_random_uuid()::text, 'human', $1, $2, $3, 'QUEUED')
		RETURNING task_id
	`, targetRepo, workflow, description).Scan(&taskID)
	if err != nil {
		return "", fmt.Errorf("backlog: insert human task: %w", err)
	}
	return taskID, nil
}

// Task is one backlog_tasks row — docs/04's seed of the real Task entity
// (still no priority, no assigned-Workflow-as-a-first-class-field beyond
// the plain string here, no triage UI beyond this read). RunID/TargetRepo/
// Workflow/Description are "" rather than a Go nil/pointer for NULL: every
// consumer so far (cmd/controlplane's JSON API) wants a plain string, and
// NULL only ever means "not set yet," not a meaningful third state to
// distinguish from empty.
type Task struct {
	TaskID      string    `json:"task_id"`
	RunID       string    `json:"run_id,omitempty"`
	TargetRepo  string    `json:"target_repo,omitempty"`
	Workflow    string    `json:"workflow,omitempty"`
	Source      string    `json:"source"`
	Description string    `json:"description,omitempty"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

// List returns every backlog Task, most recently created first — backs
// cmd/controlplane's Work section.
func List(ctx context.Context, pool *pgxpool.Pool) ([]Task, error) {
	rows, err := pool.Query(ctx, `
		SELECT task_id, coalesce(run_id, ''), coalesce(target_repo, ''), coalesce(workflow, ''),
		       source, coalesce(description, ''), status, created_at
		FROM backlog_tasks ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("backlog: list: %w", err)
	}
	defer rows.Close()

	var out []Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.TaskID, &t.RunID, &t.TargetRepo, &t.Workflow, &t.Source, &t.Description, &t.Status, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("backlog: list: scan: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("backlog: list: %w", err)
	}
	return out, nil
}

// AttachRun records which Run id was started for a Task and moves its
// status to RUNNING — called once the Run has actually been submitted to
// Temporal (see InsertHumanTask's doc comment for why this is a separate
// step rather than set at insert time).
func AttachRun(ctx context.Context, pool *pgxpool.Pool, taskID, runID string) error {
	tag, err := pool.Exec(ctx, `UPDATE backlog_tasks SET run_id = $1, status = 'RUNNING' WHERE task_id = $2`, runID, taskID)
	if err != nil {
		return fmt.Errorf("backlog: attach run: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("backlog: attach run: no backlog_tasks row for task_id %q", taskID)
	}
	return nil
}
