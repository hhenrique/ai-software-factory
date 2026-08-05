package conductor

import (
	"encoding/json"
	"strings"

	"factory/internal/workflowdef"
)

// BudgetGate is the hook point for doc 01's token-budget and
// oscillation/deadlock detection. Both methods return true for "OK to
// proceed" so a Noop implementation can be an always-allow no-op rather
// than needing inverted booleans.
type BudgetGate interface {
	// CheckTokenBudget reports whether spentTokens remains within
	// budget.MaxTokens (true if unbounded or within budget). Real as of
	// RealBudgetGate — see its doc comment for what "real" means here
	// given these harnesses only report usage after a call completes,
	// never in-flight.
	CheckTokenBudget(spentTokens int, budget workflowdef.Budget) bool

	// CheckOscillation reports whether it's OK to continue looping on
	// stepID given its accumulated Activity outputs so far — false means
	// non-convergence detected (e.g. a non-shrinking failing-test-set),
	// and the loop should fail fast rather than continue to the attempt
	// cap. Real as of RealBudgetGate — see its doc comment for what
	// non-convergence means precisely for each loop it applies to.
	CheckOscillation(stepID string, history []ActivityOutput) bool
}

// NoopBudgetGate always allows. Kept as an explicit, named "no
// enforcement at all" option (distinct from RealBudgetGate with empty
// limits, which is also always-allow but for a different reason —
// nothing configured, not enforcement disabled) — useful for tests that
// want to isolate other behavior from budget enforcement entirely.
type NoopBudgetGate struct{}

func (NoopBudgetGate) CheckTokenBudget(int, workflowdef.Budget) bool  { return true }
func (NoopBudgetGate) CheckOscillation(string, []ActivityOutput) bool { return true }

// RealBudgetGate implements token-budget enforcement (docs 01/05).
//
// What "real" means here, precisely: none of these harnesses (Claude
// Code, Codex, Copilot CLI) report token usage in-flight, only after a
// call completes — there's no way for the conductor to interrupt a
// single call mid-flight for going over. So this can only ever be a
// check-before-the-next-call gate, never a hard per-call ceiling — doc
// 05 already frames it this way ("before allowing a retry, check...
// cumulative tokens spent"), and this is a direct implementation of
// exactly that, not a weaker version of some stronger guarantee. The
// bound this actually provides: once cumulative spend already exceeds
// the limit, no further call starts — worst case is bounded by (limit +
// one call's overshoot), not by the limit exactly.
//
// CheckOscillation compares the two most recent completed attempts in
// history (nothing to compare before that — allow). It recognizes two
// signal shapes by which Produced field is present, matching the two loops
// doc 01 names explicitly:
//
//   - VERIFYING (run.tests_lint_build's "failing_tests_diff"): doc 01 says
//     "if the failing-test-set is a superset of or equal to a previous
//     attempt's failing set (not shrinking), treat this as non-convergence."
//     There's no structured test-name list here — repo build/test/lint
//     commands are arbitrary shell (doc 04), and parsing every test
//     runner's output format into real test identities isn't a "one
//     correct mechanical answer" (CLAUDE.md rule 2). The decided mechanical
//     proxy: treat each non-blank line of the combined stdout+stderr as one
//     "failing" element. Noisier than real test identities (a
//     timestamp-bearing line looks "new" every attempt) but directionally
//     right, and a false negative here just falls through to the attempt
//     cap rather than looping forever.
//   - REVIEWING (review's "findings"): doc 01 says a repeated identical
//     finding is a deadlock, "same logic as the oscillation check in the
//     verify loop" — so the same superset-or-equal-of-the-previous-set
//     comparison applies here too, canonicalizing each finding via JSON so
//     structurally identical findings compare equal regardless of map key
//     order. This doesn't also require matching Coder dispute reasoning
//     (doc 01's fuller description): coder_response carries no budget of
//     its own, so its output never lands in this history — a repeated
//     identical finding set alone is already real signal that a round
//     produced no progress, which is what actually matters for failing
//     fast.
//
// A step whose Produced carries neither field (e.g. a schema-less Coder
// pass producing only "diff") has no comparable signal here — allow.
type RealBudgetGate struct{}

func (RealBudgetGate) CheckTokenBudget(spentTokens int, budget workflowdef.Budget) bool {
	if budget.MaxTokens <= 0 {
		return true // unbounded on this dimension
	}
	return spentTokens <= budget.MaxTokens
}

func (RealBudgetGate) CheckOscillation(_ string, history []ActivityOutput) bool {
	if len(history) < 2 {
		return true
	}
	prev, latest := history[len(history)-2], history[len(history)-1]

	if diff, ok := latest.Produced["failing_tests_diff"].(string); ok {
		prevDiff, _ := prev.Produced["failing_tests_diff"].(string)
		return !isSupersetOrEqual(lineSet(diff), lineSet(prevDiff))
	}

	if findings, ok := latest.Produced["findings"]; ok {
		return !isSupersetOrEqual(canonicalSet(findings), canonicalSet(prev.Produced["findings"]))
	}

	return true
}

// lineSet splits text into a set of its non-blank trimmed lines — the
// mechanical proxy for "which tests are failing" described on
// RealBudgetGate.CheckOscillation.
func lineSet(text string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			set[line] = struct{}{}
		}
	}
	return set
}

// canonicalSet turns a []any of findings into a set of their canonical JSON
// encodings, so two structurally-identical findings compare equal
// regardless of map key order. A non-array or unmarshalable value yields an
// empty set (no comparable signal, not a crash).
func canonicalSet(v any) map[string]struct{} {
	arr, ok := v.([]any)
	if !ok {
		return map[string]struct{}{}
	}
	set := make(map[string]struct{}, len(arr))
	for _, f := range arr {
		if b, err := json.Marshal(f); err == nil {
			set[string(b)] = struct{}{}
		}
	}
	return set
}

// isSupersetOrEqual reports whether a contains every element of b (and at
// least as many) — doc 01's "superset of or equal to" non-convergence test.
func isSupersetOrEqual(a, b map[string]struct{}) bool {
	if len(b) == 0 {
		return false
	}
	if len(a) < len(b) {
		return false
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			return false
		}
	}
	return true
}
