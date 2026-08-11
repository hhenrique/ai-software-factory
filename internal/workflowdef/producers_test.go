package workflowdef

import "testing"

// TestProducedFieldsAgentStepWithSchemaAndWorktreeProducesBoth is a
// regression guard: adding change_summary to execute/revise_verify/
// revise_review's output_schema (docs/03) broke rule 5 validation of
// workflows/issue-to-pr.yaml, because producedFields used to treat
// "has an output_schema" and "produces diff" as mutually exclusive —
// true only by coincidence, since every schema-less agent step in the
// reference Workflows also happened to be the only ones with worktree
// access. harness.Invoke's actual runtime behavior always produces diff
// whenever a step has worktree_path in context, regardless of whether it
// also has an output_schema — producedFields must model both
// independently, not as alternatives.
func TestProducedFieldsAgentStepWithSchemaAndWorktreeProducesBoth(t *testing.T) {
	s := &Step{
		ID:           "execute",
		Type:         StepTypeAgent,
		Context:      []string{"scope_contract", "worktree_path"},
		OutputSchema: map[string]any{"change_summary": "string"},
	}
	fields := producedFields(s, &validationConfig{})

	got := map[string]bool{}
	for _, f := range fields {
		got[f] = true
	}
	if !got["change_summary"] {
		t.Errorf("producedFields(%+v) = %v, want change_summary from the output_schema", s, fields)
	}
	if !got["diff"] {
		t.Errorf("producedFields(%+v) = %v, want diff from worktree access, in addition to the schema field", s, fields)
	}
}

// TestProducedFieldsAgentStepWithoutWorktreeOmitsDiff covers the other
// side: a schema-bearing agent step with no worktree access (e.g.
// coder_response, reviewer's review) must not be credited with producing
// a diff it never computes.
func TestProducedFieldsAgentStepWithoutWorktreeOmitsDiff(t *testing.T) {
	s := &Step{
		ID:           "coder_response",
		Type:         StepTypeAgent,
		Context:      []string{"scope_contract", "findings"},
		OutputSchema: map[string]any{"verdict": []any{"address", "dispute"}},
	}
	fields := producedFields(s, &validationConfig{})
	for _, f := range fields {
		if f == "diff" {
			t.Errorf("producedFields(%+v) = %v, should not include diff without worktree_path in context", s, fields)
		}
	}
}
