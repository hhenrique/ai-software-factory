-- Dedup guard for task.create (internal/backlog.CreateTask). The
-- Reviewer's context (workflows/issue-to-pr-claude-only.yaml's `review`
-- step: [scope_contract, diff]) carries no memory of findings raised in
-- earlier rounds of the same Run — docs/01's "out_of_scope items ... are
-- not replayed into subsequent rounds" describes conversation_open_items
-- pruning that was never actually implemented (internal/workflowdef/
-- producers.go declares the field name for validation only; nothing
-- computes its content). So a Reviewer that re-examines the same diff
-- can legitimately re-raise the same already-spawned finding on a later
-- round, and coder_response's out_of_scope routing had no way to tell
-- that apart from a genuinely new finding — task.create just inserted
-- another row every time.
--
-- This only ever constrains the review-finding path in practice: a
-- human-submitted Task (InsertHumanTask) always inserts with run_id NULL
-- (attached later by AttachRun), and Postgres treats every NULL in a
-- unique index as distinct from every other NULL, so two human Tasks
-- never collide here regardless of description text.
--
-- Exact-text dedup, not semantic dedup: a Reviewer that rephrases the
-- same finding between rounds still produces a second row. That's a real
-- limitation, not a bug in this constraint — catching the paraphrased
-- case needs the conversation_open_items pruning docs/01 already
-- describes, not a mechanical string match.
\connect factory_projection

CREATE UNIQUE INDEX backlog_tasks_run_id_description_idx ON backlog_tasks (run_id, description);
