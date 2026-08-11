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

// TestBuildPromptPlannerRoleGetsForwardLookingAssessmentGuidance is a
// regression guard: a real Planner call's "assessment" read like a
// completion report ("Moved controls...Checks passed.") because nothing
// told the model this runs before any change or check happens. A human
// now reads this directly to approve or reject (01's mandatory
// plan-approval gate), so it has to actually read as a plan.
func TestBuildPromptPlannerRoleGetsForwardLookingAssessmentGuidance(t *testing.T) {
	p := buildPrompt(conductor.ActivityInput{
		Role:    "planner",
		Context: map[string]any{"task_description": "plan this", "worktree_path": "/tmp/x"},
	}, true)
	if !strings.Contains(p, "You have not changed any files and no build/test/lint checks have run") {
		t.Errorf("prompt %q missing the Planner-specific not-yet-executed guidance", p)
	}
}

func TestBuildPromptNonPlannerRoleOmitsAssessmentGuidance(t *testing.T) {
	p := buildPrompt(conductor.ActivityInput{
		Role:    "coder",
		Context: map[string]any{"task_description": "do this", "worktree_path": "/tmp/x"},
	}, true)
	if strings.Contains(p, "You have not changed any files and no build/test/lint checks have run") {
		t.Errorf("prompt %q should not carry Planner-specific guidance for a Coder-role call", p)
	}
}

// TestBuildPromptCoderWithWorktreeGetsChangeSummaryGuidance covers
// execute/revise_verify/revise_review's actual shape: Coder role, real
// worktree access.
func TestBuildPromptCoderWithWorktreeGetsChangeSummaryGuidance(t *testing.T) {
	p := buildPrompt(conductor.ActivityInput{
		Role:    "coder",
		Context: map[string]any{"task_description": "do this", "worktree_path": "/tmp/x"},
	}, true)
	if !strings.Contains(p, "change_summary") {
		t.Errorf("prompt %q missing the change_summary guidance for a Coder call with worktree access", p)
	}
}

// TestBuildPromptCoderResponseOmitsChangeSummaryGuidance covers
// coder_response's actual shape: Coder role, but no worktree access (it
// judges findings against an already-produced diff, same as Reviewer
// never touches the worktree) — the guidance only makes sense for a call
// that can actually run `git diff` itself.
func TestBuildPromptCoderResponseOmitsChangeSummaryGuidance(t *testing.T) {
	p := buildPrompt(conductor.ActivityInput{
		Role:    "coder",
		Context: map[string]any{"findings": []any{"x"}},
	}, false)
	if strings.Contains(p, "change_summary") {
		t.Errorf("prompt %q should not carry change_summary guidance for a worktree-less Coder call", p)
	}
}

// TestBuildPromptSchemaExplanationCoversNestedObjectsAndLists is a
// regression guard for the other half of the same gap: scope_contract's
// output_schema used to be a bare `object` placeholder, giving the model
// no indication it needed acceptance_criteria/in_scope_paths/non_goals
// keys specifically — it came back empty on a real call. The schema
// explanation must now cover a nested field template and a typed list.
func TestBuildPromptSchemaExplanationCoversNestedObjectsAndLists(t *testing.T) {
	p := buildPrompt(conductor.ActivityInput{
		Context: map[string]any{"task_description": "plan this"},
		OutputSchema: map[string]any{
			"verdict": []any{"proceed", "reject"},
			"scope_contract": map[string]any{
				"acceptance_criteria": []any{"string"},
			},
		},
	}, false)
	if !strings.Contains(p, "FIELD TEMPLATE") {
		t.Errorf("prompt %q missing the FIELD TEMPLATE rule for a nested object schema value", p)
	}
	if !strings.Contains(p, "LIST") {
		t.Errorf("prompt %q missing the LIST rule for an array-of-type-placeholder schema value", p)
	}
}
