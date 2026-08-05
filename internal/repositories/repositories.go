// Package repositories is the data-access layer for docs/04's
// Repositories section — the first control-plane section backed by real
// persistence rather than CLI flags/env vars. Plain functions over a
// *pgxpool.Pool, not a Temporal Activity: every caller (cmd/controlplane's
// HTTP handlers, cmd/submittask's -repo lookup) already runs in an
// ordinary Go process, so there's no determinism constraint requiring an
// Activity indirection here (contrast internal/backlog.CreateTask, which
// is dispatched from inside a running Temporal workflow).
package repositories

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned by Get when no repository is registered under
// the given name.
var ErrNotFound = errors.New("repositories: not found")

// Repository is docs/04's Repository entity, trimmed to exactly what
// cmd/submittask needs to start a real Run today (conductor.Repo plus a
// default Workflow file path) — not the full field set that doc lists
// (in_scope_paths defaults, branching policy), which stay unbuilt until a
// caller actually needs them, same as backlog_tasks' incremental growth.
type Repository struct {
	Name            string    `json:"name"`
	CloneURL        string    `json:"clone_url"`
	TestCommand     string    `json:"test_command"`
	DefaultWorkflow string    `json:"default_workflow"`
	Enabled         bool      `json:"enabled"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// List returns every registered repository, most recently created first.
func List(ctx context.Context, pool *pgxpool.Pool) ([]Repository, error) {
	rows, err := pool.Query(ctx, `
		SELECT name, clone_url, test_command, default_workflow, enabled, created_at, updated_at
		FROM repositories ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("repositories: list: %w", err)
	}
	defer rows.Close()

	var out []Repository
	for rows.Next() {
		var r Repository
		if err := rows.Scan(&r.Name, &r.CloneURL, &r.TestCommand, &r.DefaultWorkflow, &r.Enabled, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("repositories: list: scan: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("repositories: list: %w", err)
	}
	return out, nil
}

// Get looks up one repository by name, ErrNotFound if none is registered
// under it — used by cmd/submittask's -repo shorthand.
func Get(ctx context.Context, pool *pgxpool.Pool, name string) (Repository, error) {
	var r Repository
	err := pool.QueryRow(ctx, `
		SELECT name, clone_url, test_command, default_workflow, enabled, created_at, updated_at
		FROM repositories WHERE name = $1
	`, name).Scan(&r.Name, &r.CloneURL, &r.TestCommand, &r.DefaultWorkflow, &r.Enabled, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Repository{}, ErrNotFound
	}
	if err != nil {
		return Repository{}, fmt.Errorf("repositories: get %q: %w", name, err)
	}
	return r, nil
}

// Insert registers a new repository, enabled by default. name must be
// unique (docs/04: "Repo identifier"); a duplicate is a real conflict,
// reported as-is rather than silently upserting — registering under a
// name someone else already claimed is a mistake worth surfacing, not
// something to paper over.
func Insert(ctx context.Context, pool *pgxpool.Pool, name, cloneURL, testCommand, defaultWorkflow string) (Repository, error) {
	var r Repository
	err := pool.QueryRow(ctx, `
		INSERT INTO repositories (name, clone_url, test_command, default_workflow)
		VALUES ($1, $2, $3, $4)
		RETURNING name, clone_url, test_command, default_workflow, enabled, created_at, updated_at
	`, name, cloneURL, testCommand, defaultWorkflow).Scan(
		&r.Name, &r.CloneURL, &r.TestCommand, &r.DefaultWorkflow, &r.Enabled, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return Repository{}, fmt.Errorf("repositories: insert %q: %w", name, err)
	}
	return r, nil
}

// SetEnabled flips a repository's enabled flag — the control-plane UI's
// per-row Enable/Disable action. ErrNotFound if name isn't registered.
func SetEnabled(ctx context.Context, pool *pgxpool.Pool, name string, enabled bool) error {
	tag, err := pool.Exec(ctx, `
		UPDATE repositories SET enabled = $1, updated_at = now() WHERE name = $2
	`, enabled, name)
	if err != nil {
		return fmt.Errorf("repositories: set enabled %q: %w", name, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
