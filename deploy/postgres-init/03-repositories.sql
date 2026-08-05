-- Persisted Repository config (docs/04's Repositories section), first
-- real slice of the control plane beyond the disposable runsview tool.
-- Backs cmd/controlplane's Repositories page and cmd/submittask's
-- -repo lookup — a registered repo is real config other code paths
-- depend on, not a decorative form. Doc 04 lists more fields eventually
-- (in_scope_paths defaults, branching policy) than are captured here;
-- this table has exactly what cmd/submittask already needs to start a
-- real Run (conductor.Repo{Name, CloneURL, TestCommand} plus a default
-- Workflow file path) and no more.
\connect factory_projection

CREATE TABLE repositories (
    id               BIGSERIAL PRIMARY KEY,
    name             TEXT NOT NULL UNIQUE,   -- canonical identity, e.g. "github.com/hhenrique/toy-repo"
    clone_url        TEXT NOT NULL,
    test_command     TEXT NOT NULL DEFAULT '',
    default_workflow TEXT NOT NULL DEFAULT '',
    enabled          BOOLEAN NOT NULL DEFAULT true,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
