package main

import (
	"testing"

	"factory/internal/repositories"
)

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
