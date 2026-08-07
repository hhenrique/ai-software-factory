// Package settings is the data-access layer for global, control-plane-
// editable configuration — a generic key-value store (table `settings`)
// rather than one column per setting, so adding the next one is a data
// change, not a schema migration. Plain functions over a *pgxpool.Pool,
// same shape and justification as internal/repositories: every caller
// (cmd/controlplane's HTTP handlers, internal/repoconfig.DBProvider)
// already runs in an ordinary Go process, no Temporal Activity
// indirection needed.
//
// First real key: "factory_root" — see internal/repoconfig.DBProvider,
// which replaced the FACTORY_ROOT env var this package exists to retire.
package settings

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Setting is one key's current value.
type Setting struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Get looks up one setting by key. ok is false if it's never been set —
// deliberately not an error: "unconfigured" is an expected, meaningful
// state a caller (internal/repoconfig.DBProvider) needs to distinguish
// from a real lookup failure.
func Get(ctx context.Context, pool *pgxpool.Pool, key string) (value string, ok bool, err error) {
	err = pool.QueryRow(ctx, `SELECT value FROM settings WHERE key = $1`, key).Scan(&value)
	if err == pgx.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("settings: get %q: %w", key, err)
	}
	return value, true, nil
}

// Set upserts a setting — "reassign this value" is the normal edit, not
// a create-then-delete, same convention internal/roleassignment.Set uses.
func Set(ctx context.Context, pool *pgxpool.Pool, key, value string) (Setting, error) {
	var s Setting
	err := pool.QueryRow(ctx, `
		INSERT INTO settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = $2, updated_at = now()
		RETURNING key, value, updated_at
	`, key, value).Scan(&s.Key, &s.Value, &s.UpdatedAt)
	if err != nil {
		return Setting{}, fmt.Errorf("settings: set %q: %w", key, err)
	}
	return s, nil
}

// List returns every configured setting — small, whole-table data, same
// "fetch it all, let the UI pick out what it needs" convention as
// GET /api/workflows and GET /api/workers.
func List(ctx context.Context, pool *pgxpool.Pool) ([]Setting, error) {
	rows, err := pool.Query(ctx, `SELECT key, value, updated_at FROM settings ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("settings: list: %w", err)
	}
	defer rows.Close()

	var out []Setting
	for rows.Next() {
		var s Setting
		if err := rows.Scan(&s.Key, &s.Value, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("settings: list: scan: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("settings: list: %w", err)
	}
	return out, nil
}
