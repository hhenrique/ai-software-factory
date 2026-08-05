// Package harnesslimits resolves per-(harness, model, effort) token
// limits, decoupled from Role — two steps with different roles that
// happen to use the same (harness, model, effort) combination share the
// same limit, rather than each getting an independent one.
//
// This is deliberately env-config for now (same pattern as
// internal/repoconfig), not a database — the numbers here are for
// testing/tuning, not a decided operational policy yet, and adding a
// real config store + UI for this is exactly the kind of cost doc 04's
// "Worktree storage" section already argues to defer until enough of the
// control plane exists to justify it. See doc 04 for the accumulating
// tally of env-configured surfaces this adds to.
//
// ParseEnv is called by whatever starts a Run (never by
// conductor.RunWorkflow itself, which must stay a deterministic function
// of its input — reading an env var inside workflow code would make
// replay depend on the worker process's current environment, which can
// change between the original execution and a later replay). The
// resolved map becomes part of RunInput, not something looked up fresh
// on every replay.
package harnesslimits

import (
	"encoding/json"
	"fmt"
	"os"
)

// EnvVar is the JSON-object env var this package reads: keys are
// "harness/model/effort" (see Key), values are max-token integers. A
// combination absent from the map has no limit — this is opt-in, not a
// blanket cap that would break every unconfigured combination by default.
const EnvVar = "FACTORY_HARNESS_TOKEN_LIMITS"

// Key builds the canonical lookup key for a (harness, model, effort)
// combination — the same format on both the config side (this package)
// and the tracking side (conductor's Run-local cumulative-spend map), so
// they agree without either side needing to know the other's internals.
func Key(harness, model, effort string) string {
	return harness + "/" + model + "/" + effort
}

// ParseEnv reads EnvVar, returning an empty map (not an error) if unset —
// absent config means no limits configured, not a misconfiguration. A
// set-but-invalid value is a real misconfiguration and returns an error,
// matching the "fail loud on a mistake" convention used elsewhere
// (workflowdef's KnownFields(true), for example) rather than silently
// ignoring it.
func ParseEnv() (map[string]int, error) {
	raw := os.Getenv(EnvVar)
	if raw == "" {
		return map[string]int{}, nil
	}
	var limits map[string]int
	if err := json.Unmarshal([]byte(raw), &limits); err != nil {
		return nil, fmt.Errorf("harnesslimits: parse %s: %w", EnvVar, err)
	}
	return limits, nil
}
