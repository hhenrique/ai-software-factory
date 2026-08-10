-- Task.SourceRef (docs/08-tracking-integration.md): a structured pointer
-- back to where a Task originated, set once at creation and never mutated
-- afterward (same immutable-provenance principle as a Run's Workflow
-- provenance hash). Replaces folding a GitHub issue URL into free-text
-- description (taskintake.fetchGitHubIssue) — good for a human/agent to
-- read, useless for a tool step (the future source-side Tracker adapter)
-- to reliably re-target a comment at. Empty string means "no known
-- source" (a free-text Task), same convention repositories.test_command
-- and .default_workflow already use.
\connect factory_projection

ALTER TABLE backlog_tasks ADD COLUMN source_ref_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE backlog_tasks ADD COLUMN source_ref_ref  TEXT NOT NULL DEFAULT '';
