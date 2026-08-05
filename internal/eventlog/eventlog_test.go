package eventlog

import (
	"context"
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
	steps := []string{"provision", "execute", "verify", "merge"}
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
	if lastToStep != "merge" {
		t.Errorf("most recent to_step = %q, want %q", lastToStep, "merge")
	}
}
