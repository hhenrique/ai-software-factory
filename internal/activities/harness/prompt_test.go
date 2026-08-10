package harness

import (
	"strings"
	"testing"

	"factory/internal/conductor"
)

func TestBuildPromptIncludesTaskDescription(t *testing.T) {
	p := buildPrompt(conductor.ActivityInput{
		Context: map[string]any{"task_description": "Bump left-pad to 2.0.0"},
	}, false)
	if !strings.Contains(p, "Bump left-pad to 2.0.0") {
		t.Errorf("prompt %q missing task_description", p)
	}
}

func TestBuildPromptIncludesFailingTestsDiffOnRetry(t *testing.T) {
	p := buildPrompt(conductor.ActivityInput{
		Context: map[string]any{
			"task_description":   "Bump left-pad",
			"failing_tests_diff": "FAIL: TestFoo",
		},
	}, true)
	if !strings.Contains(p, "FAIL: TestFoo") {
		t.Errorf("prompt %q missing failing_tests_diff", p)
	}
}

func TestBuildPromptIncludesDiffForReviewer(t *testing.T) {
	p := buildPrompt(conductor.ActivityInput{
		Context: map[string]any{
			"scope_contract": map[string]any{"acceptance_criteria": []string{"x"}},
			"diff":           "diff --git a/f.txt b/f.txt\n+hello",
		},
		OutputSchema: map[string]any{"findings": "array", "verdict": []any{"approved", "changes_required"}},
	}, false)
	if !strings.Contains(p, "diff --git a/f.txt b/f.txt") {
		t.Errorf("prompt %q missing the diff context field — Reviewer has nothing to review without it", p)
	}
}

func TestBuildPromptRequestsJSONBlockWhenOutputSchemaPresent(t *testing.T) {
	p := buildPrompt(conductor.ActivityInput{
		Context:      map[string]any{"task_description": "plan this"},
		OutputSchema: map[string]any{"verdict": []any{"proceed", "reject"}},
	}, false)
	if !strings.Contains(p, "```json") {
		t.Errorf("prompt %q should ask for a fenced json block when OutputSchema is set", p)
	}
}

func TestBuildPromptNoJSONInstructionWhenSchemaLess(t *testing.T) {
	p := buildPrompt(conductor.ActivityInput{
		Context: map[string]any{"task_description": "just edit files"},
	}, true)
	if strings.Contains(p, "```json") {
		t.Errorf("prompt %q should not ask for JSON when there's no OutputSchema", p)
	}
}

// TestBuildPromptNoWorktreeNote is Planner/Reviewer's actual shape: no
// worktree_path in context. The note must tell the harness plainly that
// this is expected, or an agentic CLI tends to go looking for a repo
// anyway and escalate the resulting confusion (see conversation that
// prompted this: a Planner run against an empty temp dir reported the
// task itself as blocked).
func TestBuildPromptNoWorktreeNote(t *testing.T) {
	p := buildPrompt(conductor.ActivityInput{
		Context: map[string]any{"task_description": "plan this"},
	}, false)
	if !strings.Contains(p, "do not have access to this repository's files") {
		t.Errorf("prompt %q missing the no-file-access note for a worktree-less step", p)
	}
}

func TestBuildPromptNoWorktreeNoteOmittedWhenWorktreePresent(t *testing.T) {
	p := buildPrompt(conductor.ActivityInput{
		Context: map[string]any{"task_description": "edit files", "worktree_path": "/tmp/x"},
	}, true)
	if strings.Contains(p, "do not have access to this repository's files") {
		t.Errorf("prompt %q should not carry the no-file-access note when the step has a real worktree", p)
	}
}
