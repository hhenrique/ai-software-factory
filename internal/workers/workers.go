// Package workers is the data-access layer for a Worker — the persisted
// (harness, model, params) triad docs/03 calls a Role's backing config,
// now decoupled from the Workflow Definition YAML that used to embed it
// inline in a roles: block. Plain functions over a *pgxpool.Pool, same
// shape and same justification as internal/repositories: every caller
// (cmd/controlplane's HTTP handlers, internal/roleassignment.Resolve via
// internal/taskintake) already runs in an ordinary Go process, so there's
// no determinism constraint requiring a Temporal Activity indirection
// here.
package workers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// foreignKeyViolationCode is Postgres's SQLSTATE for a foreign-key
// constraint violation — used to tell "worker still in use" apart from
// any other Delete failure.
const foreignKeyViolationCode = "23503"

// ErrNotFound is returned by Get when no worker is registered under the
// given id.
var ErrNotFound = errors.New("workers: not found")

// Worker is one configured (harness, model, params) triad — see
// docs/03-roles-and-harness-contract.md. Name is a free-text label (e.g.
// "Sonnet — high effort"), the identity a human picks it by in the
// control plane; harness/model/params is the actual config a
// harness adapter receives.
type Worker struct {
	ID        int64             `json:"id"`
	Name      string            `json:"name"`
	Harness   string            `json:"harness"`
	Model     string            `json:"model"`
	Params    map[string]string `json:"params"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// List returns every configured worker, most recently created first.
func List(ctx context.Context, pool *pgxpool.Pool) ([]Worker, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, name, harness, model, params, created_at, updated_at
		FROM workers ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("workers: list: %w", err)
	}
	defer rows.Close()

	var out []Worker
	for rows.Next() {
		var w Worker
		if err := rows.Scan(&w.ID, &w.Name, &w.Harness, &w.Model, &w.Params, &w.CreatedAt, &w.UpdatedAt); err != nil {
			return nil, fmt.Errorf("workers: list: scan: %w", err)
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("workers: list: %w", err)
	}
	return out, nil
}

// Get looks up one worker by id, ErrNotFound if none is registered under
// it.
func Get(ctx context.Context, pool *pgxpool.Pool, id int64) (Worker, error) {
	var w Worker
	err := pool.QueryRow(ctx, `
		SELECT id, name, harness, model, params, created_at, updated_at
		FROM workers WHERE id = $1
	`, id).Scan(&w.ID, &w.Name, &w.Harness, &w.Model, &w.Params, &w.CreatedAt, &w.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Worker{}, ErrNotFound
	}
	if err != nil {
		return Worker{}, fmt.Errorf("workers: get %d: %w", id, err)
	}
	return w, nil
}

// Create registers a new worker. name must be unique — a duplicate is a
// real conflict, reported as-is rather than silently upserting, same
// convention as internal/repositories.Insert.
func Create(ctx context.Context, pool *pgxpool.Pool, name, harness, model string, params map[string]string) (Worker, error) {
	if params == nil {
		params = map[string]string{}
	}
	var w Worker
	err := pool.QueryRow(ctx, `
		INSERT INTO workers (name, harness, model, params)
		VALUES ($1, $2, $3, $4)
		RETURNING id, name, harness, model, params, created_at, updated_at
	`, name, harness, model, params).Scan(
		&w.ID, &w.Name, &w.Harness, &w.Model, &w.Params, &w.CreatedAt, &w.UpdatedAt)
	if err != nil {
		return Worker{}, fmt.Errorf("workers: create %q: %w", name, err)
	}
	return w, nil
}

// Update changes a worker's name/harness/model/params in place — the
// same row (and id) every role_assignments.worker_id reference keeps
// pointing at, so editing a Worker's backing config here immediately
// takes effect for every (workflow, role) currently assigned to it,
// without touching a single role_assignments row. ErrNotFound if id
// isn't registered.
func Update(ctx context.Context, pool *pgxpool.Pool, id int64, name, harness, model string, params map[string]string) (Worker, error) {
	if params == nil {
		params = map[string]string{}
	}
	var w Worker
	err := pool.QueryRow(ctx, `
		UPDATE workers SET name = $1, harness = $2, model = $3, params = $4, updated_at = now()
		WHERE id = $5
		RETURNING id, name, harness, model, params, created_at, updated_at
	`, name, harness, model, params, id).Scan(
		&w.ID, &w.Name, &w.Harness, &w.Model, &w.Params, &w.CreatedAt, &w.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Worker{}, ErrNotFound
	}
	if err != nil {
		return Worker{}, fmt.Errorf("workers: update %d: %w", id, err)
	}
	return w, nil
}

// Delete removes a worker. ErrNotFound if id isn't registered. Fails
// with ErrInUse (wrapping the underlying foreign-key violation) if any
// role_assignments row still points at it — role_assignments.worker_id
// has no ON DELETE CASCADE precisely so this fails loudly instead of
// silently breaking a workflow's role resolution.
var ErrInUse = errors.New("workers: still assigned to at least one role")

func Delete(ctx context.Context, pool *pgxpool.Pool, id int64) error {
	tag, err := pool.Exec(ctx, `DELETE FROM workers WHERE id = $1`, id)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolationCode {
			return ErrInUse
		}
		return fmt.Errorf("workers: delete %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
