package conductor

import "factory/internal/workflowdef"

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
	// non-convergence detected (e.g. a non-shrinking failing-test-set)
	// and the loop should fail fast rather than continue to the attempt
	// cap. Still deferred — see RealBudgetGate's doc comment.
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
// CheckOscillation is deliberately still a no-op — a real implementation
// needs to compare structured content across attempts (failing-test-sets,
// dispute reasoning) and is a distinct piece of work from token-limit
// enforcement, tracked separately.
type RealBudgetGate struct{}

func (RealBudgetGate) CheckTokenBudget(spentTokens int, budget workflowdef.Budget) bool {
	if budget.MaxTokens <= 0 {
		return true // unbounded on this dimension
	}
	return spentTokens <= budget.MaxTokens
}

func (RealBudgetGate) CheckOscillation(string, []ActivityOutput) bool {
	return true // TODO: next piece of work, not this one
}
