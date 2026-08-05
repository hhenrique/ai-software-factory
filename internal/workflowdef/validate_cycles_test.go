package workflowdef

import (
	"regexp"
	"testing"
)

// wordPresent reports whether id appears in msg as a whole word — plain
// substring matching is unsafe here since single-letter step ids like "a"
// or "c" occur as substrings of unrelated words in the error text (e.g.
// "acyclic", "budgeted").
func wordPresent(msg, id string) bool {
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(id) + `\b`).MatchString(msg)
}

func step(id, next, budget string) Step {
	return Step{ID: id, Type: StepTypeAgent, Next: next, Budget: budget}
}

func TestValidateCyclesBudgetedSelfLoopOK(t *testing.T) {
	def := &Definition{
		Budgets: map[string]Budget{"b1": {MaxAttempts: 3}},
		Steps:   []Step{step("a", "a", "b1")},
	}
	if errs := validateCycles(def); len(errs) != 0 {
		t.Fatalf("expected no errors for budgeted self-loop, got:\n%s", errs.Error())
	}
}

func TestValidateCyclesUnbudgetedSelfLoopErrors(t *testing.T) {
	def := &Definition{
		Steps: []Step{step("a", "a", "")},
	}
	errs := validateCycles(def)
	if len(errs) != 1 {
		t.Fatalf("expected exactly 1 error for unbudgeted self-loop, got %d:\n%s", len(errs), errs.Error())
	}
	if errs[0].Rule != RuleAcyclicOrBudgeted {
		t.Errorf("Rule = %s, want %s", errs[0].Rule, RuleAcyclicOrBudgeted)
	}
}

func TestValidateCyclesTwoDisjointCyclesOnlyUnbudgetedErrors(t *testing.T) {
	// a<->b unbudgeted, c<->d budgeted (on c).
	def := &Definition{
		Budgets: map[string]Budget{"b1": {MaxAttempts: 2}},
		Steps: []Step{
			step("a", "b", ""),
			step("b", "a", ""),
			step("c", "d", "b1"),
			step("d", "c", ""),
		},
	}
	errs := validateCycles(def)
	if len(errs) != 1 {
		t.Fatalf("expected exactly 1 error, got %d:\n%s", len(errs), errs.Error())
	}
	msg := errs[0].Error()
	if !wordPresent(msg, "a") || !wordPresent(msg, "b") {
		t.Errorf("error message %q should name the unbudgeted cycle's members a,b", msg)
	}
	if wordPresent(msg, "c") || wordPresent(msg, "d") {
		t.Errorf("error message %q should not name the budgeted cycle's members c,d", msg)
	}
}

func TestValidateCyclesDiamondMergeIsSingleSCC(t *testing.T) {
	// a->b, a->c, b->d, c->d, d->a: two parallel paths from a to d, closed
	// back into a cycle by d->a. Must be recognized as one SCC {a,b,c,d},
	// not two separate cycles or four isolated nodes.
	base := []Step{
		{ID: "a", Type: StepTypeAgent, On: map[string]Target{"x": {StepOrState: "b"}, "y": {StepOrState: "c"}}},
		step("b", "d", ""),
		step("c", "d", ""),
		step("d", "a", ""),
	}

	t.Run("unbudgeted", func(t *testing.T) {
		def := &Definition{Steps: append([]Step(nil), base...)}
		errs := validateCycles(def)
		if len(errs) != 1 {
			t.Fatalf("expected exactly 1 error for the merged SCC, got %d:\n%s", len(errs), errs.Error())
		}
		msg := errs[0].Error()
		for _, id := range []string{"a", "b", "c", "d"} {
			if !wordPresent(msg, id) {
				t.Errorf("error message %q should name every SCC member, missing %q", msg, id)
			}
		}
	})

	t.Run("budgeted on one member", func(t *testing.T) {
		steps := append([]Step(nil), base...)
		steps[1].Budget = "b1" // step "b"
		def := &Definition{
			Budgets: map[string]Budget{"b1": {MaxAttempts: 3}},
			Steps:   steps,
		}
		if errs := validateCycles(def); len(errs) != 0 {
			t.Fatalf("expected no errors once one SCC member is budgeted, got:\n%s", errs.Error())
		}
	})
}

func TestValidateCyclesDiamondWithoutClosingEdgeIsNotACycle(t *testing.T) {
	// a->b, a->c, b->d, c->d, no edge back to a: a DAG, not a cycle.
	def := &Definition{
		Steps: []Step{
			{ID: "a", Type: StepTypeAgent, On: map[string]Target{"x": {StepOrState: "b"}, "y": {StepOrState: "c"}}},
			step("b", "d", ""),
			step("c", "d", ""),
			{ID: "d", Type: StepTypeAgent},
		},
	}
	if errs := validateCycles(def); len(errs) != 0 {
		t.Fatalf("expected no errors for a non-cyclic diamond, got:\n%s", errs.Error())
	}
}
