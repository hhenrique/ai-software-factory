package workflowdef

// defaultToolActionProducedFields maps a tool step's action identifier to
// the context fields it makes available to later steps. Doc 02 has no
// field for declaring a tool action's outputs, so this table is the
// mechanical stand-in rule 5 needs — override per-Validate-call via
// WithToolActionProducedFields for actions this package doesn't know about.
var defaultToolActionProducedFields = map[string][]string{
	"run.tests_lint_build": {"failing_tests_diff"},
	"worktree.create":      {"worktree_path", "branch", "clone_dir"},
	"task.create":          {"spawned_task_id"},
}

// defaultAlwaysAvailableFields are fields available from the start of a
// Run without being any step's Produced output — either conductor-computed
// (e.g. doc 01's pruned "open items" view of the review conversation, a
// projection over all findings raised so far; or human_hint, merged into
// context by RunWorkflow when a human resumes a Run from REVIEW_PENDING —
// see conductor.HumanDecision) or a Run-level input (doc 02 rule 5:
// "either part of the immutable Run context set in an earlier step's
// output, or a Run-level input"), supplied via RunInput.InitialContext by
// whatever starts the Run (e.g. task_description — there's no persisted
// Task entity yet, so this is supplied directly for now).
var defaultAlwaysAvailableFields = []string{
	"conversation_open_items",
	"task_description",
	"human_hint",
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
