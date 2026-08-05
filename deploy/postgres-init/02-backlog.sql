-- Backlog store for backlog_tasks, doc 04's seed of the real Task entity
-- (not the entity itself: still no priority, no triage UI). Two write
-- paths land here:
--   - task.create (docs/01: an out_of_scope review finding), source
--     'review-finding', run_id set immediately (the id of the Run that
--     raised the finding), target_repo/workflow NULL (the finding doesn't
--     carry forward which repo/workflow the new backlog Task should use —
--     still unbuilt, same doc 04 gap as priority/triage).
--   - a human-submitted Task (cmd/submittask), source 'human', run_id NULL
--     at insert time (there's no decoupled scheduler yet — see doc 00's
--     "bespoke scheduling engine" deferral — so submitting a Task starts
--     its Run immediately after, and run_id is attached right after that
--     Run is started, not before).
\connect factory_projection

CREATE TABLE backlog_tasks (
    id          BIGSERIAL PRIMARY KEY,
    task_id     TEXT NOT NULL UNIQUE,
    run_id      TEXT,
    target_repo TEXT,
    workflow    TEXT,
    source      TEXT NOT NULL,
    description TEXT,
    status      TEXT NOT NULL DEFAULT 'QUEUED',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX backlog_tasks_run_id_idx ON backlog_tasks (run_id);
