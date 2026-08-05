package workflowdef

import "embed"

//go:embed fixtures/*.yaml
var fixturesFS embed.FS

// IssueToPRStandardYAML and DependencyBumpMinimalYAML are the two reference
// Workflow Definitions from doc 02, copied verbatim. They're embedded here
// as the single source of truth shared by the validator's own tests, the
// Temporal worker's registered definitions, and the smoke test — so a
// future edit to either YAML can't drift between those three consumers.
var (
	IssueToPRStandardYAML     = mustReadFixture("fixtures/issue-to-pr-standard.yaml")
	DependencyBumpMinimalYAML = mustReadFixture("fixtures/dependency-bump-minimal.yaml")
)

// brokenFixture names the five fixtures that each violate exactly one of
// doc 02's validation rules 1-5 (rule 0's structural prerequisites are
// covered directly by parse/structure tests instead).
type brokenFixture struct {
	Name string
	Rule Rule
	YAML []byte
}

func brokenFixtures() []brokenFixture {
	return []brokenFixture{
		{"broken-rule1-unbudgeted-cycle", RuleAcyclicOrBudgeted, mustReadFixture("fixtures/broken-rule1-unbudgeted-cycle.yaml")},
		{"broken-rule2-unknown-role", RuleAgentRoleExists, mustReadFixture("fixtures/broken-rule2-unknown-role.yaml")},
		{"broken-rule3-missing-malformed-handler", RuleMalformedOutputHandled, mustReadFixture("fixtures/broken-rule3-missing-malformed-handler.yaml")},
		{"broken-rule4-unmapped-verdict", RuleOutcomesMapped, mustReadFixture("fixtures/broken-rule4-unmapped-verdict.yaml")},
		{"broken-rule5-unproducible-context", RuleContextProducible, mustReadFixture("fixtures/broken-rule5-unproducible-context.yaml")},
	}
}

func mustReadFixture(path string) []byte {
	data, err := fixturesFS.ReadFile(path)
	if err != nil {
		panic("workflowdef: embedded fixture missing: " + path + ": " + err.Error())
	}
	return data
}
