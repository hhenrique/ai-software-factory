-- Adds outcome + produced to a run_events row — the same content
-- doc08-tracking-integration.md's tracker mirror already formats for
-- external posting (verdict, scope_contract, findings, a diff summary),
-- now also persisted here so the control plane's Inbox/Work views can
-- show a human *why* a Run is stuck without needing Temporal's raw
-- history to find it (doc04: "full trace/replay per Run... non-negotiable
-- even for a minimal build"). Same shape as 05's failure_reason addition
-- — both are "why," just for different transition kinds: failure_reason
-- for a hard Run failure, these two for an ordinary routing decision
-- (an agent's verdict, a tool step's pass/fail, a synthetic label like
-- "budget_exhausted"/"malformed_output").
\connect factory_projection

ALTER TABLE run_events ADD COLUMN outcome TEXT;
ALTER TABLE run_events ADD COLUMN produced JSONB;
