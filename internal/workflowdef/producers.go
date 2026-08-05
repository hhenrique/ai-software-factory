package workflowdef

// defaultToolActionProducedFields maps a tool step's action identifier to
// the context fields it makes available to later steps. Doc 02 has no
// field for declaring a tool action's outputs, so this table is the
// mechanical stand-in rule 5 needs — override per-Validate-call via
// WithToolActionProducedFields for actions this package doesn't know about.
var defaultToolActionProducedFields = map[string][]string{
	"run.tests_lint_build": {"failing_tests_diff"},
}

// defaultAlwaysAvailableFields are fields the conductor computes itself,
// not tied to any single step's output — e.g. doc 01's pruned "open items"
// view of the review conversation, which is a conductor-maintained
// projection over all findings raised so far, not one step's output.
var defaultAlwaysAvailableFields = []string{
	"conversation_open_items",
}

// producedFields returns the context field names step s makes available to
// its successors.
func producedFields(s *Step, cfg *validationConfig) []string {
	switch s.Type {
	case StepTypeTool:
		return cfg.toolActionProducedFields[s.Action]
	case StepTypeAgent:
		if len(s.OutputSchema) > 0 {
			fields := make([]string, 0, len(s.OutputSchema))
			for k := range s.OutputSchema {
				fields = append(fields, k)
			}
			return fields
		}
		// doc 03's implicit-patch convention: a schema-less agent step
		// (e.g. Coder's initial EXECUTING pass) still produces a diff.
		return []string{"diff"}
	default:
		return nil
	}
}
