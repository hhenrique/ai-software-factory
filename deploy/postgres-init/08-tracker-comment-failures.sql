-- Queryable record of a failed tracker comment post
-- (docs/08-tracking-integration.md's "best-effort must not mean silent" —
-- a comment-post failure never fails the Run, but it must still leave a
-- fact something else can query, not just a Temporal worker log line
-- nobody reads). Deliberately its own table, not another row shape in
-- run_events: that table's (from_step, to_step) pair is what
-- internal/backlog.List derives a Task's current Status from (most recent
-- row wins), and a tracker-comment failure isn't a real state transition —
-- inserting one there would risk becoming the "latest" row and reporting
-- a stale/wrong Status.
\connect factory_projection

CREATE TABLE tracker_comment_failures (
    id          BIGSERIAL PRIMARY KEY,
    run_id      TEXT NOT NULL,
    target_kind TEXT NOT NULL,
    target_ref  TEXT NOT NULL,
    error       TEXT NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX tracker_comment_failures_run_id_idx ON tracker_comment_failures (run_id);
