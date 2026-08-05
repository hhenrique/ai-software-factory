package workflowdef

import "testing"

func TestParseReferenceDefinitions(t *testing.T) {
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
			if def.Workflow != tc.name {
				t.Errorf("Workflow = %q, want %q", def.Workflow, tc.name)
			}
			if len(def.Steps) == 0 {
				t.Errorf("Steps is empty")
			}
		})
	}
}

func TestParseUnknownFieldFailsLoudly(t *testing.T) {
	data := []byte(`
workflow: bad
version: 1
roles: {}
steps:
  - id: a
    type: tool
    action: worktree.create
    typo_field: oops
`)
	if _, err := Parse(data); err == nil {
		t.Fatalf("Parse: expected error for unknown field, got nil")
	}
}

func TestParseInvalidYAMLSyntax(t *testing.T) {
	data := []byte("workflow: [unterminated")
	if _, err := Parse(data); err == nil {
		t.Fatalf("Parse: expected error for invalid YAML syntax, got nil")
	}
}
