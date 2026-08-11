package workflowdef

import "testing"

func TestValidateReferenceDefinitionsAreClean(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"issue-to-pr-standard", IssueToPRStandardYAML},
		{"dependency-bump-minimal", DependencyBumpMinimalYAML},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def, err := Parse(tc.data)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if errs := Validate(def); len(errs) != 0 {
				t.Fatalf("Validate: expected no errors, got:\n%s", errs.Error())
			}
		})
	}
}

// TestValidateReviewPendingIsAPassThroughForContext is a regression guard
// for the plan-approval gate (docs/01): a step reachable only via a
// human's REVIEW_PENDING resume — never a direct on:/next: edge — must
// still see whatever an earlier step produced before reaching
// REVIEW_PENDING. buildEdges drops REVIEW_PENDING as a terminal sink
// (correct for cycle detection), which would otherwise make this look
// permanently unproducible to rule 5's reaching-definitions analysis
// even though runContext genuinely carries it forward at runtime.
func TestValidateReviewPendingIsAPassThroughForContext(t *testing.T) {
	yaml := []byte(`
workflow: gate-test
version: 1
roles: [planner, coder]
steps:
  - id: plan
    type: agent
    role: planner
    output_schema: { verdict: [proceed, reject, escalate], scope_contract: object }
    on:
      proceed:  REVIEW_PENDING
      reject:   FAILED
      escalate: REVIEW_PENDING
    on_malformed_output: REVIEW_PENDING

  - id: execute
    type: agent
    role: coder
    context: [scope_contract]
    next: COMPLETED
`)
	def, err := Parse(yaml)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if errs := Validate(def); len(errs) != 0 {
		t.Fatalf("Validate: expected no errors, got:\n%s", errs.Error())
	}
}

// TestValidateContextStillCatchesAGenuinelyUnproducedField proves the
// REVIEW_PENDING pass-through above doesn't make rule 5 toothless — a
// field nothing ever produces must still fail, REVIEW_PENDING or not.
func TestValidateContextStillCatchesAGenuinelyUnproducedField(t *testing.T) {
	yaml := []byte(`
workflow: gate-test-negative
version: 1
roles: [planner, coder]
steps:
  - id: plan
    type: agent
    role: planner
    output_schema: { verdict: [proceed, reject, escalate], scope_contract: object }
    on:
      proceed:  REVIEW_PENDING
      reject:   FAILED
      escalate: REVIEW_PENDING
    on_malformed_output: REVIEW_PENDING

  - id: execute
    type: agent
    role: coder
    context: [nonexistent_field]
    next: COMPLETED
`)
	def, err := Parse(yaml)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	errs := Validate(def)
	if !errs.HasRule(RuleContextProducible) {
		t.Fatalf("Validate: expected a %s error, got:\n%s", RuleContextProducible, errs.Error())
	}
}

func TestValidateBrokenFixturesFailWithExpectedRule(t *testing.T) {
	for _, bf := range brokenFixtures() {
		t.Run(bf.Name, func(t *testing.T) {
			def, err := Parse(bf.YAML)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			errs := Validate(def)
			if len(errs) == 0 {
				t.Fatalf("Validate: expected at least one error for %s, got none", bf.Name)
			}
			if !errs.HasRule(bf.Rule) {
				t.Fatalf("Validate: expected an error tagged %s for %s, got:\n%s", bf.Rule, bf.Name, errs.Error())
			}
		})
	}
}
