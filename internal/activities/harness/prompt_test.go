package harness

import (
	"strings"
	"testing"

	"factory/internal/conductor"
)

func TestBuildPromptIncludesTaskDescription(t *testing.T) {
	p := buildPrompt(conductor.ActivityInput{
		Context: map[string]any{"task_description": "Bump left-pad to 2.0.0"},
	})
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
	})
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
	})
	if !strings.Contains(p, "diff --git a/f.txt b/f.txt") {
		t.Errorf("prompt %q missing the diff context field — Reviewer has nothing to review without it", p)
	}
}

func TestBuildPromptRequestsJSONBlockWhenOutputSchemaPresent(t *testing.T) {
	p := buildPrompt(conductor.ActivityInput{
		Context:      map[string]any{"task_description": "plan this"},
		OutputSchema: map[string]any{"verdict": []any{"proceed", "reject"}},
	})
	if !strings.Contains(p, "```json") {
		t.Errorf("prompt %q should ask for a fenced json block when OutputSchema is set", p)
	}
}

func TestBuildPromptNoJSONInstructionWhenSchemaLess(t *testing.T) {
	p := buildPrompt(conductor.ActivityInput{
		Context: map[string]any{"task_description": "just edit files"},
	})
	if strings.Contains(p, "```json") {
		t.Errorf("prompt %q should not ask for JSON when there's no OutputSchema", p)
	}
}
