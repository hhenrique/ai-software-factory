package conductor

import "factory/internal/workflowdef"

// BudgetGate is the hook point for doc 01's token-budget and
// oscillation/deadlock detection. Deliberately not implemented this slice:
// a real implementation needs external I/O (cumulative spend queries,
// prior failing-test-set history) and belongs in its own Activity invoked
// from RunWorkflow like any other side-effecting call — never plain
// in-workflow logic, since Temporal workflow code must stay deterministic.
// Both methods return true for "OK to proceed" so a Noop implementation
// can be an always-allow no-op rather than needing inverted booleans.
type BudgetGate interface {
	// CheckTokenBudget reports whether spentTokens remains within
	// budget.MaxTokens (true if unbounded or within budget).
	CheckTokenBudget(spentTokens int, budget workflowdef.Budget) bool

	// CheckOscillation reports whether it's OK to continue looping on
	// stepID given its accumulated Activity outputs so far — false means
	// non-convergence detected (e.g. a non-shrinking failing-test-set)
	// and the loop should fail fast rather than continue to the attempt
	// cap.
	CheckOscillation(stepID string, history []ActivityOutput) bool
}

// NoopBudgetGate always allows — the explicit stand-in until token-budget
// and oscillation/deadlock checks are implemented as a real Activity.
type NoopBudgetGate struct{}

func (NoopBudgetGate) CheckTokenBudget(int, workflowdef.Budget) bool  { return true }
func (NoopBudgetGate) CheckOscillation(string, []ActivityOutput) bool { return true }
