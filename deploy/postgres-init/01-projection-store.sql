-- Control plane projection store: a separate database within the shared
-- Postgres instance (docs/05: Temporal's own persistence and the
-- projection store each get their own database, not a shared schema).
-- Runs once, on first container init (docker-entrypoint-initdb.d), same
-- as temporalio/auto-setup creating its own databases.
CREATE DATABASE factory_projection;

\connect factory_projection

-- One row per Run state transition (docs/01: "Every state transition
-- emits a structured event... including Runs that fail before any model
-- call"). "State transition" here is the conductor's step transition —
-- the generic step-walker doesn't have doc 01's named states baked in
-- structurally, only step ids that conventionally correspond to them
-- (see internal/conductor/workflow.go).
CREATE TABLE run_events (
    id             BIGSERIAL PRIMARY KEY,
    run_id         TEXT NOT NULL,
    task_id        TEXT,             -- always NULL for now; no persisted Task entity yet (doc 04's Work section)
    workflow       TEXT NOT NULL,
    from_step      TEXT NOT NULL,    -- '' for the initial "Run started" event
    to_step        TEXT NOT NULL,
    step_id        TEXT,             -- the step whose Activity call produced this transition; NULL for the initial event
    attempt_number INT,
    token_delta    INT NOT NULL DEFAULT 0,
    activity_calls INT NOT NULL DEFAULT 0,
    occurred_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX run_events_run_id_idx ON run_events (run_id, occurred_at);
