package main

import (
	"os"
	"testing"

	"factory/internal/repositories"
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

func TestResolveRepoConfigExplicitFlagsWinOverRegistered(t *testing.T) {
	repo := repositories.Repository{
		CloneURL: "https://github.com/registered/repo.git", TestCommand: "registered test", DefaultWorkflow: "registered.yaml",
	}
	cloneURL, testCommand, workflowFile := resolveRepoConfig(repo, "https://github.com/explicit/repo.git", "explicit test", "explicit.yaml")
	if cloneURL != "https://github.com/explicit/repo.git" || testCommand != "explicit test" || workflowFile != "explicit.yaml" {
		t.Errorf("resolveRepoConfig = (%q, %q, %q), want explicit values unchanged", cloneURL, testCommand, workflowFile)
	}
}

func TestResolveRepoConfigFallsBackToRegisteredWhenFlagsEmpty(t *testing.T) {
	repo := repositories.Repository{
		CloneURL: "https://github.com/registered/repo.git", TestCommand: "registered test", DefaultWorkflow: "registered.yaml",
	}
	cloneURL, testCommand, workflowFile := resolveRepoConfig(repo, "", "", "")
	if cloneURL != repo.CloneURL || testCommand != repo.TestCommand || workflowFile != repo.DefaultWorkflow {
		t.Errorf("resolveRepoConfig = (%q, %q, %q), want registered repo's values", cloneURL, testCommand, workflowFile)
	}
}

func TestResolveRepoConfigPartialOverride(t *testing.T) {
	repo := repositories.Repository{
		CloneURL: "https://github.com/registered/repo.git", TestCommand: "registered test", DefaultWorkflow: "registered.yaml",
	}
	cloneURL, testCommand, workflowFile := resolveRepoConfig(repo, "", "explicit test", "")
	if cloneURL != repo.CloneURL {
		t.Errorf("cloneURL = %q, want registered value %q", cloneURL, repo.CloneURL)
	}
	if testCommand != "explicit test" {
		t.Errorf("testCommand = %q, want explicit value", testCommand)
	}
	if workflowFile != repo.DefaultWorkflow {
		t.Errorf("workflowFile = %q, want registered value %q", workflowFile, repo.DefaultWorkflow)
	}
}
