package workflowdef

import (
	"fmt"
	"strings"
)

// Rule identifies which of doc 02's validation rules an error violates.
// RuleStructural is a rule-0 prerequisite the docs don't number explicitly,
// but the conductor needs it satisfied before rules 1-5 mean anything.
type Rule int

const (
	RuleStructural Rule = iota
	RuleAcyclicOrBudgeted
	RuleAgentRoleExists
	RuleMalformedOutputHandled
	RuleOutcomesMapped
	RuleContextProducible
)

func (r Rule) String() string {
	switch r {
	case RuleStructural:
		return "rule0-structural"
	case RuleAcyclicOrBudgeted:
		return "rule1-acyclic-or-budgeted"
	case RuleAgentRoleExists:
		return "rule2-agent-role-exists"
	case RuleMalformedOutputHandled:
		return "rule3-malformed-output-handled"
	case RuleOutcomesMapped:
		return "rule4-outcomes-mapped"
	case RuleContextProducible:
		return "rule5-context-producible"
	default:
		return "unknown-rule"
	}
}

// ValidationError is a single violation, tagged with the rule that caught
// it so callers (tests, the conductor, a future control-plane UI) can tell
// failure modes apart rather than pattern-matching error strings.
type ValidationError struct {
	Rule    Rule
	StepID  string // empty for definition-level errors not tied to one step
	Message string
}

func (e *ValidationError) Error() string {
	if e.StepID != "" {
		return fmt.Sprintf("[%s] step %q: %s", e.Rule, e.StepID, e.Message)
	}
	return fmt.Sprintf("[%s] %s", e.Rule, e.Message)
}

// ValidationErrors collects every violation found by Validate — it does not
// fail fast, so a human iterating on YAML sees every problem in one pass.
type ValidationErrors []*ValidationError

func (e ValidationErrors) Error() string {
	lines := make([]string, len(e))
	for i, err := range e {
		lines[i] = err.Error()
	}
	return strings.Join(lines, "\n")
}

// HasRule reports whether any error in the collection was raised by rule r.
func (e ValidationErrors) HasRule(r Rule) bool {
	for _, err := range e {
		if err.Rule == r {
			return true
		}
	}
	return false
}
