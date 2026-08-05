package conductor_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"factory/internal/conductor"
)

func TestRealBudgetGateCheckOscillationAllowsFewerThanTwoAttempts(t *testing.T) {
	var gate conductor.RealBudgetGate

	require.True(t, gate.CheckOscillation("verify", nil))
	require.True(t, gate.CheckOscillation("verify", []conductor.ActivityOutput{
		{Produced: map[string]any{"failing_tests_diff": "FAIL a\nFAIL b"}},
	}))
}

func TestRealBudgetGateCheckOscillationFailingTestsNotShrinking(t *testing.T) {
	var gate conductor.RealBudgetGate

	history := []conductor.ActivityOutput{
		{Produced: map[string]any{"failing_tests_diff": "FAIL a\nFAIL b"}},
		{Produced: map[string]any{"failing_tests_diff": "FAIL a\nFAIL b\nFAIL c"}},
	}
	require.False(t, gate.CheckOscillation("verify", history), "latest is a strict superset of prev — non-convergent")

	history = []conductor.ActivityOutput{
		{Produced: map[string]any{"failing_tests_diff": "FAIL a\nFAIL b"}},
		{Produced: map[string]any{"failing_tests_diff": "FAIL b\nFAIL a"}},
	}
	require.False(t, gate.CheckOscillation("verify", history), "same set, different order — equal counts as non-convergent")
}

func TestRealBudgetGateCheckOscillationFailingTestsShrinking(t *testing.T) {
	var gate conductor.RealBudgetGate

	history := []conductor.ActivityOutput{
		{Produced: map[string]any{"failing_tests_diff": "FAIL a\nFAIL b\nFAIL c"}},
		{Produced: map[string]any{"failing_tests_diff": "FAIL a"}},
	}
	require.True(t, gate.CheckOscillation("verify", history), "latest is a proper subset of prev — real progress")
}

func TestRealBudgetGateCheckOscillationFailingTestsDisjointNotFlaggedAsNonConvergent(t *testing.T) {
	var gate conductor.RealBudgetGate

	// Neither a superset nor shrinking — different failures each time.
	// Not the pattern doc 01 names (non-shrinking), so it isn't flagged
	// here; it still eventually hits the attempt cap on its own.
	history := []conductor.ActivityOutput{
		{Produced: map[string]any{"failing_tests_diff": "FAIL a"}},
		{Produced: map[string]any{"failing_tests_diff": "FAIL b"}},
	}
	require.True(t, gate.CheckOscillation("verify", history))
}

func TestRealBudgetGateCheckOscillationRepeatedIdenticalFindingsIsDeadlock(t *testing.T) {
	var gate conductor.RealBudgetGate

	finding := map[string]any{"file": "main.go", "line": float64(42), "summary": "unchecked error"}
	history := []conductor.ActivityOutput{
		{Produced: map[string]any{"findings": []any{finding}}},
		{Produced: map[string]any{"findings": []any{finding}}},
	}
	require.False(t, gate.CheckOscillation("review", history), "identical finding raised twice in a row — deadlock")
}

func TestRealBudgetGateCheckOscillationNewFindingsAreNotADeadlock(t *testing.T) {
	var gate conductor.RealBudgetGate

	history := []conductor.ActivityOutput{
		{Produced: map[string]any{"findings": []any{map[string]any{"summary": "issue A"}}}},
		{Produced: map[string]any{"findings": []any{map[string]any{"summary": "issue B"}}}},
	}
	require.True(t, gate.CheckOscillation("review", history))

	history = []conductor.ActivityOutput{
		{Produced: map[string]any{"findings": []any{}}},
		{Produced: map[string]any{"findings": []any{}}},
	}
	require.True(t, gate.CheckOscillation("review", history), "no findings at all carries no comparable signal")
}

func TestRealBudgetGateCheckOscillationNoComparableFieldAllows(t *testing.T) {
	var gate conductor.RealBudgetGate

	history := []conductor.ActivityOutput{
		{Produced: map[string]any{"diff": "some diff v1"}},
		{Produced: map[string]any{"diff": "some diff v2"}},
	}
	require.True(t, gate.CheckOscillation("execute", history))
}
