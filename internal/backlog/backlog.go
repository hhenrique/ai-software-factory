// Package backlog implements task.create — the side-effecting Activity
// coder_response's out_of_scope compound target dispatches (doc 01: an
// out-of-scope review finding spawns a new backlog Task rather than being
// discarded). Writes into the same projection-store Postgres instance
// internal/eventlog uses (deploy/postgres-init/02-backlog.sql), not a
// separate store.
package backlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"factory/internal/conductor"
	"factory/internal/workflowdef"
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

// SourceRef is a structured pointer back to where a Task originated (doc
// 08: "a real structured field ... set once at Task creation and never
// mutated afterward"). Kind "" means no known source — a free-text Task.
type SourceRef struct {
	Kind string `json:"kind,omitempty"` // "github_issue" | "aha_idea" | ""
	Ref  string `json:"ref,omitempty"`  // issue URL, Aha! idea id/reference
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
func InsertHumanTask(ctx context.Context, pool *pgxpool.Pool, targetRepo, workflow, description string, sourceRef SourceRef) (taskID string, err error) {
	err = pool.QueryRow(ctx, `
		INSERT INTO backlog_tasks (task_id, source, target_repo, workflow, description, status, source_ref_kind, source_ref_ref)
		VALUES (gen_random_uuid()::text, 'human', $1, $2, $3, 'QUEUED', $4, $5)
		RETURNING task_id
	`, targetRepo, workflow, description, sourceRef.Kind, sourceRef.Ref).Scan(&taskID)
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
	SourceRef   SourceRef `json:"source_ref"`

	// FailureReason is the error text from the run_events row that set
	// Status to FAILED (conductor.recordFailure) — "" whenever Status
	// isn't FAILED, or for a FAILED Task predating that fix (an already-
	// recorded FAILED transition with no reason attached).
	FailureReason string `json:"failure_reason,omitempty"`

	// Outcome/Summary mirror internal/inbox.PendingRun's fields of the
	// same name: the latest transition's routing outcome and a rendered
	// verdict/scope_contract/findings/diff summary — so the Work view can
	// show *why* a Task's Run is sitting in REVIEW_PENDING (or what its
	// last step actually did) without a human needing to separately open
	// the Inbox or read Temporal's raw history (doc04's "full trace/
	// replay per Run" is non-negotiable even for a minimal build).
	Outcome string `json:"outcome,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// List returns every backlog Task, most recently created first — backs
// cmd/controlplane's Work section.
//
// Status is derived from run_events' latest transition for the Task's
// run_id, not read from backlog_tasks.status directly: that column is
// only ever written once, by AttachRun when a Run starts (status
// 'RUNNING'), and nothing updates it again when the Run actually finishes
// — a stale-forever value once a Run completes/fails/is cancelled.
// run_events is doc01's actual source of truth for a Run's transitions
// (and, since internal/conductor.recordFailure, reliably includes the
// terminal one even for a hard Activity error), so deriving from it here
// means Status can't drift the way a second, hand-maintained copy of the
// same fact already has. The stored column is still the fallback for a
// Task with no run_id yet (before its Run starts, i.e. 'QUEUED') or the
// rare case a Run's first event hasn't landed yet.
func List(ctx context.Context, pool *pgxpool.Pool) ([]Task, error) {
	rows, err := pool.Query(ctx, `
		SELECT bt.task_id, coalesce(bt.run_id, ''), coalesce(bt.target_repo, ''), coalesce(bt.workflow, ''),
		       bt.source, coalesce(bt.description, ''), bt.status, bt.created_at,
		       bt.source_ref_kind, bt.source_ref_ref,
		       latest.to_step, latest.failure_reason, latest.outcome, latest.produced
		FROM backlog_tasks bt
		LEFT JOIN LATERAL (
			SELECT to_step, failure_reason, outcome, produced FROM run_events
			WHERE run_events.run_id = bt.run_id
			ORDER BY occurred_at DESC, id DESC LIMIT 1
		) latest ON true
		ORDER BY bt.created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("backlog: list: %w", err)
	}
	defer rows.Close()

	var out []Task
	for rows.Next() {
		var t Task
		var latestToStep, failureReason, outcome sql.NullString
		var producedJSON []byte
		if err := rows.Scan(&t.TaskID, &t.RunID, &t.TargetRepo, &t.Workflow, &t.Source, &t.Description, &t.Status, &t.CreatedAt,
			&t.SourceRef.Kind, &t.SourceRef.Ref,
			&latestToStep, &failureReason, &outcome, &producedJSON); err != nil {
			return nil, fmt.Errorf("backlog: list: scan: %w", err)
		}
		if latestToStep.Valid && latestToStep.String != "" {
			if workflowdef.IsTerminalState(latestToStep.String) {
				// COMPLETED, FAILED, CANCELLED, or REVIEW_PENDING — all
				// meaningful to show as-is, not collapsed into "RUNNING".
				t.Status = latestToStep.String
			} else {
				t.Status = "RUNNING"
			}
		}
		if t.Status == "FAILED" {
			t.FailureReason = failureReason.String
		}
		t.Outcome = outcome.String
		if len(producedJSON) > 0 {
			var produced map[string]any
			if err := json.Unmarshal(producedJSON, &produced); err != nil {
				return nil, fmt.Errorf("backlog: list: unmarshal produced for task %q: %w", t.TaskID, err)
			}
			t.Summary = conductor.FormatEventContent(produced)
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
