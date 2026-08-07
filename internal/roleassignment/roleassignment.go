// Package roleassignment is the data-access layer for "which Worker
// currently plays which Role, for a given Workflow" — the join between
// internal/workflowdef's fixed Role names and internal/workers' persisted
// (harness, model, params) triads. Plain functions over a *pgxpool.Pool,
// same shape as internal/repositories/internal/workers.
//
// Resolve is the one function called from inside the Run-submission path
// (internal/taskintake.Submit): it must run *before* RunWorkflow starts,
// never from inside it — RunWorkflow must stay a deterministic function
// of its RunInput (docs/05), so the resolved Worker triads get baked into
// RunInput as plain data, the same pattern internal/harnesslimits already
// established for token limits.
package roleassignment

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"factory/internal/workflowdef"
)

// foreignKeyViolationCode is Postgres's SQLSTATE for a foreign-key
// constraint violation — used by Set to tell "worker_id doesn't exist"
// apart from any other failure.
const foreignKeyViolationCode = "23503"

// ErrUnknownWorker is returned by Set when workerID doesn't name a real
// worker (role_assignments.worker_id's foreign key would otherwise just
// surface as an opaque Postgres error).
var ErrUnknownWorker = errors.New("roleassignment: unknown worker id")

// ErrUnknownRole is returned by Set when role isn't a member of
// workflowdef.KnownRoles. Set validates this itself (not just trusting
// callers, e.g. cmd/controlplane's HTTP handler, to check first) so the
// invariant holds regardless of caller.
var ErrUnknownRole = errors.New("roleassignment: role is not a known role")

// Assignment is one (workflow, role) -> worker binding.
type Assignment struct {
	Workflow  string    `json:"workflow"`
	Role      string    `json:"role"`
	WorkerID  int64     `json:"worker_id"`
	UpdatedAt time.Time `json:"updated_at"`
}

// List returns every current assignment across every workflow — small,
// whole-table data (one row per (workflow, role) pair that's ever been
// assigned), fetched in full and grouped/filtered client-side, same
// convention as GET /api/workflows and GET /api/workers.
func List(ctx context.Context, pool *pgxpool.Pool) ([]Assignment, error) {
	rows, err := pool.Query(ctx, `
		SELECT workflow, role, worker_id, updated_at FROM role_assignments
		ORDER BY workflow, role
	`)
	if err != nil {
		return nil, fmt.Errorf("roleassignment: list: %w", err)
	}
	defer rows.Close()

	var out []Assignment
	for rows.Next() {
		var a Assignment
		if err := rows.Scan(&a.Workflow, &a.Role, &a.WorkerID, &a.UpdatedAt); err != nil {
			return nil, fmt.Errorf("roleassignment: list: scan: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("roleassignment: list: %w", err)
	}
	return out, nil
}

// Set assigns workerID to play role for workflow — an upsert, since
// "reassign this role" is the normal edit, not a create-then-delete.
// Does not validate that workflow names an actual Workflow Definition
// (this package has no notion of a workflows directory — that's
// cmd/controlplane's job, which already scans it for other purposes).
func Set(ctx context.Context, pool *pgxpool.Pool, workflow, role string, workerID int64) (Assignment, error) {
	if !workflowdef.IsKnownRole(role) {
		return Assignment{}, fmt.Errorf("%w: %q", ErrUnknownRole, role)
	}

	var a Assignment
	err := pool.QueryRow(ctx, `
		INSERT INTO role_assignments (workflow, role, worker_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (workflow, role) DO UPDATE SET worker_id = $3, updated_at = now()
		RETURNING workflow, role, worker_id, updated_at
	`, workflow, role, workerID).Scan(&a.Workflow, &a.Role, &a.WorkerID, &a.UpdatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolationCode {
			return Assignment{}, fmt.Errorf("%w: %d", ErrUnknownWorker, workerID)
		}
		return Assignment{}, fmt.Errorf("roleassignment: set %q/%q: %w", workflow, role, err)
	}
	return a, nil
}

// ErrNotFound is returned by Delete when no assignment exists for the
// given (workflow, role).
var ErrNotFound = errors.New("roleassignment: not found")

// Delete removes the assignment for (workflow, role), leaving that role
// unassigned — a legitimate state to be in (e.g. mid-setup), just one
// Resolve will reject at Run-submission time.
func Delete(ctx context.Context, pool *pgxpool.Pool, workflow, role string) error {
	tag, err := pool.Exec(ctx, `DELETE FROM role_assignments WHERE workflow = $1 AND role = $2`, workflow, role)
	if err != nil {
		return fmt.Errorf("roleassignment: delete %q/%q: %w", workflow, role, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Resolve looks up the currently-assigned Worker for every name in
// roleNames (a Workflow Definition's parsed roles: list) under workflow,
// returning each as a workflowdef.Role (harness/model/params) keyed by
// role name — exactly the shape internal/conductor.RunInput.RoleAssignments
// expects. Returns one error naming every role with no current
// assignment (not just the first — same "surface every problem in one
// pass" convention workflowdef.ValidationErrors already uses), since a
// human fixing this wants the whole list, not one-at-a-time discovery.
func Resolve(ctx context.Context, pool *pgxpool.Pool, workflow string, roleNames []string) (map[string]workflowdef.Role, error) {
	if len(roleNames) == 0 {
		return map[string]workflowdef.Role{}, nil
	}

	rows, err := pool.Query(ctx, `
		SELECT ra.role, w.harness, w.model, w.params
		FROM role_assignments ra
		JOIN workers w ON w.id = ra.worker_id
		WHERE ra.workflow = $1 AND ra.role = ANY($2)
	`, workflow, roleNames)
	if err != nil {
		return nil, fmt.Errorf("roleassignment: resolve %q: %w", workflow, err)
	}
	defer rows.Close()

	resolved := make(map[string]workflowdef.Role, len(roleNames))
	for rows.Next() {
		var role string
		var r workflowdef.Role
		if err := rows.Scan(&role, &r.Harness, &r.Model, &r.Params); err != nil {
			return nil, fmt.Errorf("roleassignment: resolve %q: scan: %w", workflow, err)
		}
		resolved[role] = r
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("roleassignment: resolve %q: %w", workflow, err)
	}

	var missing []string
	for _, name := range roleNames {
		if _, ok := resolved[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("roleassignment: workflow %q has no Worker assigned for role(s) %v — set one in the Workflows view before submitting", workflow, missing)
	}
	return resolved, nil
}
