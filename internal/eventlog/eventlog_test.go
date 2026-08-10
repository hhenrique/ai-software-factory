package eventlog

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"factory/internal/conductor"
)

// requirePool connects to the projection store, skipping the test if it's
// not reachable — same spirit as gitops/pr/verify's requireGit: this
// package's real behavior needs a real Postgres (deploy/docker-compose.yaml),
// not something worth mocking with a fake SQL layer for one insert query.
func requirePool(t *testing.T) *Activities {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pool, err := NewPool(ctx)
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

func TestRecordEventInsertsRow(t *testing.T) {
	a := requirePool(t)
	ctx := context.Background()

	runID := "test-run-" + time.Now().Format(time.RFC3339Nano)
	err := a.RecordEvent(ctx, conductor.TransitionEvent{
		RunID:         runID,
		Workflow:      "dependency-bump-minimal",
		FromStep:      "",
		ToStep:        "provision",
		AttemptNumber: 0,
		TokenDelta:    0,
		ActivityCalls: 0,
	})
	if err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}

	var count int
	if err := a.Pool.QueryRow(ctx, `SELECT count(*) FROM run_events WHERE run_id = $1`, runID).Scan(&count); err != nil {
		t.Fatalf("query back: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
}

func TestRecordEventMultipleTransitionsOrderByOccurredAt(t *testing.T) {
	a := requirePool(t)
	ctx := context.Background()

	runID := "test-run-multi-" + time.Now().Format(time.RFC3339Nano)
	steps := []string{"provision", "execute", "verify", "create_pr"}
	from := ""
	for _, to := range steps {
		if err := a.RecordEvent(ctx, conductor.TransitionEvent{
			RunID: runID, Workflow: "dependency-bump-minimal", FromStep: from, ToStep: to,
		}); err != nil {
			t.Fatalf("RecordEvent(%s -> %s): %v", from, to, err)
		}
		from = to
	}

	var lastToStep string
	err := a.Pool.QueryRow(ctx,
		`SELECT to_step FROM run_events WHERE run_id = $1 ORDER BY occurred_at DESC, id DESC LIMIT 1`, runID,
	).Scan(&lastToStep)
	if err != nil {
		t.Fatalf("query back: %v", err)
	}
	if lastToStep != "create_pr" {
		t.Errorf("most recent to_step = %q, want %q", lastToStep, "create_pr")
	}
}

// TestRecordEventPersistsOutcomeAndProduced is a regression guard for the
// control-plane visibility gap this closes: without these two columns,
// the only way to see *why* a Run escalated (a Planner's verdict, its
// scope_contract) was reading Temporal's raw Activity history by hand —
// Inbox/Work need to read it back from here instead.
func TestRecordEventPersistsOutcomeAndProduced(t *testing.T) {
	a := requirePool(t)
	ctx := context.Background()

	runID := "test-run-outcome-" + time.Now().Format(time.RFC3339Nano)
	// A ToStep of REVIEW_PENDING here isn't just a routing detail worth
	// asserting on — internal/inbox.List treats every run_events row
	// shaped this way as a real pending item in the control plane's live
	// Inbox view, so this row must be cleaned up like any other
	// user-visible row this session has been fixing, not left as
	// "it's only an append-only log" (true for an ordinary transition,
	// not for one a human-facing view actually queries by to_step).
	t.Cleanup(func() {
		a.Pool.Exec(context.Background(), `DELETE FROM run_events WHERE run_id = $1`, runID)
	})
	produced := map[string]any{
		"verdict":        "escalate",
		"scope_contract": map[string]any{"acceptance_criteria": []any{"a", "b"}},
	}
	err := a.RecordEvent(ctx, conductor.TransitionEvent{
		RunID: runID, Workflow: "issue-to-pr-claude-only", FromStep: "plan", ToStep: "REVIEW_PENDING",
		Outcome: "escalate", Produced: produced,
	})
	if err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}

	var gotOutcome string
	var gotProducedJSON []byte
	if err := a.Pool.QueryRow(ctx,
		`SELECT outcome, produced FROM run_events WHERE run_id = $1`, runID,
	).Scan(&gotOutcome, &gotProducedJSON); err != nil {
		t.Fatalf("query back: %v", err)
	}
	if gotOutcome != "escalate" {
		t.Errorf("outcome = %q, want escalate", gotOutcome)
	}
	var gotProduced map[string]any
	if err := json.Unmarshal(gotProducedJSON, &gotProduced); err != nil {
		t.Fatalf("unmarshal produced: %v", err)
	}
	if gotProduced["verdict"] != "escalate" {
		t.Errorf("produced[verdict] = %v, want escalate", gotProduced["verdict"])
	}
}

// TestRecordEventLeavesProducedNullWhenEmpty guards the NULLIF-style
// convention every other "nothing to show" column already follows here —
// an empty Produced map must not become a stored "{}" that a reader would
// have to special-case away from real NULL.
func TestRecordEventLeavesProducedNullWhenEmpty(t *testing.T) {
	a := requirePool(t)
	ctx := context.Background()

	runID := "test-run-no-outcome-" + time.Now().Format(time.RFC3339Nano)
	if err := a.RecordEvent(ctx, conductor.TransitionEvent{
		RunID: runID, Workflow: "dependency-bump-minimal", FromStep: "", ToStep: "provision",
	}); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}

	var gotOutcome *string
	var gotProducedJSON []byte
	if err := a.Pool.QueryRow(ctx,
		`SELECT outcome, produced FROM run_events WHERE run_id = $1`, runID,
	).Scan(&gotOutcome, &gotProducedJSON); err != nil {
		t.Fatalf("query back: %v", err)
	}
	if gotOutcome != nil {
		t.Errorf("outcome = %v, want NULL", *gotOutcome)
	}
	if gotProducedJSON != nil {
		t.Errorf("produced = %s, want NULL", gotProducedJSON)
	}
}
