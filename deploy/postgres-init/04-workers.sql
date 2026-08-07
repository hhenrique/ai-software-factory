-- Worker (harness/model/params triad, docs/03) as a real persisted,
-- control-plane-CRUD'd entity, decoupled from the Workflow Definition
-- YAML that used to embed it inline in a roles: block. role_assignments
-- is the other half: which Worker currently plays which Role (a fixed
-- name — internal/workflowdef.KnownRoles — not itself a row here) for a
-- given Workflow (by logical name, e.g. "issue-to-pr-claude-only" —
-- same convention as backlog_tasks.workflow/run_events.workflow, not a
-- file path). Resolved once per Run at submission time
-- (internal/roleassignment.Resolve, called from internal/taskintake)
-- and baked into RunInput as plain data — never looked up inside
-- RunWorkflow itself, which must stay a deterministic function of its
-- input (same reason internal/harnesslimits resolves outside the
-- workflow too).
\connect factory_projection

CREATE TABLE workers (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,          -- free-text label, e.g. "Sonnet — high effort"
    harness    TEXT NOT NULL,
    model      TEXT NOT NULL,
    params     JSONB NOT NULL DEFAULT '{}',   -- e.g. {"effort": "high"}
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- role/workflow validity (role is one of workflowdef.KnownRoles; workflow
-- names an actual Workflow Definition) is enforced in the Go handler
-- layer, not here — matches how internal/workflowdef.Validate, not a DB
-- constraint, is this codebase's source of truth for schema correctness
-- (see internal/repositories for the same division of responsibility).
-- worker_id has no ON DELETE CASCADE: deleting a Worker still assigned to
-- a role must fail loudly (a foreign-key violation), not silently orphan
-- a workflow's role resolution.
CREATE TABLE role_assignments (
    workflow   TEXT NOT NULL,
    role       TEXT NOT NULL,
    worker_id  BIGINT NOT NULL REFERENCES workers(id),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workflow, role)
);

CREATE INDEX role_assignments_worker_id_idx ON role_assignments (worker_id);
