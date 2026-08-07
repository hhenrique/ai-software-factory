-- Global settings (internal/settings) — a generic key-value store, not a
-- single-purpose table: adding the next setting later is an INSERT, not
-- an ALTER TABLE, matching doc04's "one shared mechanism... rather than
-- each surface growing its own bespoke migration path independently."
-- First real key: factory_root, replacing the FACTORY_ROOT env var
-- (internal/repoconfig.EnvProvider) that caused real Run failures — a
-- silently-wrong default nobody had to confront until deep inside a
-- mkdir. The new DBProvider fails loud instead when unconfigured; see
-- internal/repoconfig.
\connect factory_projection

CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Per-repo override, same "empty string means unset, inherit the
-- default" convention test_command/default_workflow already use — not a
-- separate table, since it's a 1:1 relationship (a repo has at most one
-- worktree root override), same shape as those two columns.
ALTER TABLE repositories ADD COLUMN worktree_root TEXT NOT NULL DEFAULT '';
