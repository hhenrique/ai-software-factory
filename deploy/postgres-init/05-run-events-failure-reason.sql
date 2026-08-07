-- Adds the failure reason to a run_events row — internal/conductor's
-- recordFailure (see 04-workers.sql-era fix in internal/conductor/
-- workflow.go) records a FAILED transition for every hard failure in
-- RunWorkflow's main loop, but until now had nowhere to put the actual
-- error text, only that a failure happened. The Work view needs the text
-- to show a human why a Run failed, not just that it did.
\connect factory_projection

ALTER TABLE run_events ADD COLUMN failure_reason TEXT;
