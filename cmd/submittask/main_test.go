package main

import (
	"os"
	"testing"

	"factory/internal/workflowdef"
)

// TestDefaultWorkflowFileParsesAndValidates is a regression guard, same
// spirit as cmd/smoketest's equivalent check for the embedded reference
// definitions: if a future edit to workflows/issue-to-pr-claude-only.yaml
// breaks it, this fails loud in `go test` rather than only at submittask
// runtime.
func TestDefaultWorkflowFileParsesAndValidates(t *testing.T) {
	data, err := os.ReadFile("../../workflows/issue-to-pr-claude-only.yaml")
	if err != nil {
		t.Fatalf("read workflows/issue-to-pr-claude-only.yaml: %v", err)
	}
	def, err := workflowdef.Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if errs := workflowdef.Validate(def); len(errs) != 0 {
		t.Fatalf("validate:\n%s", errs.Error())
	}
	for _, role := range def.Roles {
		if role.Harness != "claude-code" {
			t.Errorf("role harness = %q, want claude-code (this file exists specifically to avoid needing Codex/Copilot credits)", role.Harness)
		}
	}
}

func TestGenerateRunIDIncludesIssueNumber(t *testing.T) {
	got := generateRunID("toy-repo", 3)
	want := "toy-repo-issue-3-"
	if len(got) <= len(want) || got[:len(want)] != want {
		t.Errorf("generateRunID(toy-repo, 3) = %q, want prefix %q", got, want)
	}
}

func TestGenerateRunIDWithoutIssueNumber(t *testing.T) {
	got := generateRunID("toy-repo", 0)
	want := "toy-repo-task-"
	if len(got) <= len(want) || got[:len(want)] != want {
		t.Errorf("generateRunID(toy-repo, 0) = %q, want prefix %q", got, want)
	}
}
