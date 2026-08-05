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
