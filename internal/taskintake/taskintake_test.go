package taskintake

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"factory/internal/conductor"
	"factory/internal/eventlog"
)

func TestDefaultWorkflowFileParsesAndValidates(t *testing.T) {
	// Doubles as a regression guard, same spirit as cmd/smoketest's
	// equivalent check for the embedded reference definitions: if a
	// future edit to DefaultWorkflowFile breaks it, this fails loud in
	// `go test` rather than only at Submit runtime. DefaultWorkflowFile
	// itself is relative to the repo root (correct for runtime use from
	// cmd/submittask or cmd/controlplane); "../../" gets there from this
	// package's own directory.
	//
	// This used to also assert every declared role's harness was
	// claude-code (the reason this file exists instead of just using
	// issue-to-pr-standard — avoiding needing Codex/Copilot credits). That
	// assertion doesn't apply anymore: harness/model is no longer part of
	// the Workflow Definition (see internal/roleassignment) — it's
	// whichever Worker is currently assigned per role in the database,
	// runtime state this parse-only test has no access to.
	if _, err := parseAndValidateWorkflowFile("../../" + DefaultWorkflowFile); err != nil {
		t.Fatalf("parse/validate %s: %v", DefaultWorkflowFile, err)
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

func TestSubmitRequiresExactlyOneOfDescriptionOrGitHubIssue(t *testing.T) {
	repo := conductor.Repo{Name: "x", CloneURL: "https://github.com/a/b.git", TestCommand: "true"}

	_, err := Submit(context.Background(), Deps{}, Params{Repo: repo})
	if err == nil {
		t.Errorf("Submit with neither Description nor GitHubIssue: expected an error")
	}

	_, err = Submit(context.Background(), Deps{}, Params{Repo: repo, Description: "x", GitHubIssue: 1})
	if err == nil {
		t.Errorf("Submit with both Description and GitHubIssue: expected an error")
	}
}

func TestSubmitRequiresTestCommand(t *testing.T) {
	repo := conductor.Repo{Name: "x", CloneURL: "https://github.com/a/b.git"}
	_, err := Submit(context.Background(), Deps{}, Params{Repo: repo, Description: "do the thing"})
	if err == nil {
		t.Errorf("Submit with no TestCommand: expected an error")
	}
}

// requirePool connects to the projection store, skipping if it's not
// reachable — same spirit as internal/backlog's requirePool.
func requirePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pool, err := eventlog.NewPool(ctx)
	if err != nil {
		t.Skip("projection store not configured:", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skip("projection store not reachable (is `docker compose up` running?):", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestSubmitInvalidWorkflowFileErrorsBeforeInsertingATask(t *testing.T) {
	pool := requirePool(t)
	repo := conductor.Repo{Name: "x", CloneURL: "https://github.com/a/b.git", TestCommand: "true"}

	_, err := Submit(context.Background(), Deps{Pool: pool}, Params{
		Repo:         repo,
		Description:  "do the thing",
		WorkflowFile: "does-not-exist.yaml",
	})
	if err == nil {
		t.Fatalf("expected an error for a missing workflow file")
	}

	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM backlog_tasks WHERE description = 'do the thing'`,
	).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 0 {
		t.Errorf("a Task was inserted despite Submit failing on workflow validation")
	}
}
