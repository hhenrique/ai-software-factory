package harness

import "testing"

func TestExtractJSONBlockFenced(t *testing.T) {
	text := "Here's my answer:\n```json\n{\"verdict\": \"proceed\", \"n\": 1}\n```\nDone."
	got, ok := extractJSONBlock(text)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if got["verdict"] != "proceed" {
		t.Errorf("verdict = %v, want proceed", got["verdict"])
	}
}

func TestExtractJSONBlockBareJSON(t *testing.T) {
	got, ok := extractJSONBlock(`  {"verdict": "reject"}  `)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if got["verdict"] != "reject" {
		t.Errorf("verdict = %v, want reject", got["verdict"])
	}
}

func TestExtractJSONBlockInvalid(t *testing.T) {
	_, ok := extractJSONBlock("this is not json at all")
	if ok {
		t.Fatalf("expected ok=false for non-JSON text")
	}
}

// TestScalarStringAcceptsPlainString and
// TestScalarStringAcceptsSingleElementArray are regression tests for a
// real bug: gpt-5-mini (via the copilot CLI) returned
// {"verdict": ["approved"]} for an enum field instead of a bare string,
// routing a real Reviewer call to malformed_output.
func TestScalarStringAcceptsPlainString(t *testing.T) {
	s, ok := scalarString("approved")
	if !ok || s != "approved" {
		t.Errorf("scalarString(\"approved\") = (%q, %v), want (approved, true)", s, ok)
	}
}

func TestScalarStringAcceptsSingleElementArray(t *testing.T) {
	s, ok := scalarString([]any{"approved"})
	if !ok || s != "approved" {
		t.Errorf("scalarString([approved]) = (%q, %v), want (approved, true)", s, ok)
	}
}

func TestScalarStringRejectsMultiElementArray(t *testing.T) {
	// Tolerating the one-element case must not become "guess at anything
	// array-shaped" — a genuinely ambiguous multi-choice array is still
	// malformed, not silently collapsed to its first element.
	_, ok := scalarString([]any{"approved", "changes_required"})
	if ok {
		t.Errorf("scalarString([approved, changes_required]): expected ok=false")
	}
}

func TestScalarStringRejectsEmptyArrayAndOtherTypes(t *testing.T) {
	if _, ok := scalarString([]any{}); ok {
		t.Errorf("scalarString([]): expected ok=false")
	}
	if _, ok := scalarString(map[string]any{}); ok {
		t.Errorf("scalarString({}): expected ok=false")
	}
	if _, ok := scalarString(nil); ok {
		t.Errorf("scalarString(nil): expected ok=false")
	}
}
