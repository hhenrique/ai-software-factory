package tracker

import (
	"context"
	"strings"
	"testing"
	"time"

	"factory/internal/conductor"
	"factory/internal/eventlog"
)

// requirePool connects to the projection store, skipping if it's not
// reachable — same spirit as internal/backlog's requirePool.
func requirePool(t *testing.T) *Activities {
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
	return &Activities{Pool: pool}
}

// TestGitHubTrackerSatisfiesTrackerInterface is a compile-time-flavored
// check: assigning to the Tracker var only compiles if GitHubTracker
// actually implements PostComment with the interface's exact signature.
func TestGitHubTrackerSatisfiesTrackerInterface(t *testing.T) {
	var _ Tracker = GitHubTracker{}
}

// PostComment against a real github_pr/github_issue target fundamentally
// requires a real GitHub-hosted PR/issue (gh validates against the live
// API) — verified separately via a live, manual check, not in this
// automated suite, same as internal/activities/pr's pr-creation half.
func TestGitHubTrackerPostCommentRejectsUnsupportedKind(t *testing.T) {
	err := GitHubTracker{}.PostComment(context.Background(), Ref{Kind: "aha_idea", Ref: "IDEA-1"}, "hello")
	if err == nil {
		t.Fatalf("expected an error for an unsupported target kind")
	}
	if !strings.Contains(err.Error(), "aha_idea") {
		t.Errorf("error = %q, want it to mention the unsupported kind", err.Error())
	}
}

// TestActivitiesPostCommentRecordsQueryableFailure is doc08's "best-effort
// must not mean silent": a failed post must leave a fact something else
// can query (tracker_comment_failures), not just a Temporal worker log
// line, while still returning the original error to the caller (which
// recordTransition never lets fail the Run — see internal/conductor).
func TestActivitiesPostCommentRecordsQueryableFailure(t *testing.T) {
	a := requirePool(t)
	ctx := context.Background()

	runID := "test-run-tracker-" + time.Now().Format(time.RFC3339Nano)
	in := conductor.TrackerCommentInput{
		RunID: runID, TargetKind: "aha_idea", TargetRef: "IDEA-1", Body: "hello",
	}

	err := a.PostComment(ctx, in)
	if err == nil {
		t.Fatalf("expected an error for an unsupported target kind")
	}
	if !strings.Contains(err.Error(), "aha_idea") {
		t.Errorf("error = %q, want it to mention the unsupported kind", err.Error())
	}
	t.Cleanup(func() {
		a.Pool.Exec(context.Background(), `DELETE FROM tracker_comment_failures WHERE run_id = $1`, runID)
	})

	var gotTargetKind, gotTargetRef, gotError string
	queryErr := a.Pool.QueryRow(ctx,
		`SELECT target_kind, target_ref, error FROM tracker_comment_failures WHERE run_id = $1`, runID,
	).Scan(&gotTargetKind, &gotTargetRef, &gotError)
	if queryErr != nil {
		t.Fatalf("query back tracker_comment_failures: %v", queryErr)
	}
	if gotTargetKind != "aha_idea" || gotTargetRef != "IDEA-1" {
		t.Errorf("recorded target = (%q, %q), want (aha_idea, IDEA-1)", gotTargetKind, gotTargetRef)
	}
	if !strings.Contains(gotError, "aha_idea") {
		t.Errorf("recorded error = %q, want it to mention the unsupported kind", gotError)
	}
}
