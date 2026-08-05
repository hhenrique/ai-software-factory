-- Minimal backlog store for task.create (docs/01: an out_of_scope review
-- finding spawns a new backlog Task, source-tagged
-- auto-generated:review-finding, QUEUED, unprioritized). This is a real
-- record, not a placeholder — but doc 04's full Task entity (priority,
-- assigned Workflow, a triage UI) isn't built yet, so this table only
-- carries what's needed to prove the finding wasn't silently discarded.
\connect factory_projection

CREATE TABLE backlog_tasks (
    id          BIGSERIAL PRIMARY KEY,
    task_id     TEXT NOT NULL UNIQUE,
    run_id      TEXT NOT NULL,
    source      TEXT NOT NULL,
    description TEXT,
    status      TEXT NOT NULL DEFAULT 'QUEUED',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX backlog_tasks_run_id_idx ON backlog_tasks (run_id);
