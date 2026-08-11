package backlog

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"factory/internal/conductor"
	"factory/internal/eventlog"
)

// requirePool connects to the projection store, skipping if it's not
// reachable — same spirit as eventlog's requirePool: this package's real
// behavior needs a real Postgres (deploy/docker-compose.yaml).
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

// cleanupTask registers a best-effort delete of a backlog_tasks row once
// the test ends — internal/backlog has no exported Delete (this is
// test-only cleanup via direct SQL), so every test in this file that
// inserts a real row must call this, so the control plane's Work view
// never accumulates rows from routine `go test` runs.
func cleanupTask(t *testing.T, pool *pgxpool.Pool, taskID string) {
	t.Helper()
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM backlog_tasks WHERE task_id = $1`, taskID)
	})
}

func TestCreateTaskInsertsRowAndReturnsID(t *testing.T) {
	a := requirePool(t)
	ctx := context.Background()

	runID := "test-run-" + time.Now().Format(time.RFC3339Nano)
	out, err := a.CreateTask(ctx, conductor.ActivityInput{
		RunID: runID,
		Context: map[string]any{
			"source":           "review-finding",
			"task_description": "extract this into a method",
		},
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	taskID, _ := out.Produced["spawned_task_id"].(string)
	if taskID == "" {
		t.Fatalf("expected spawned_task_id in Produced, got %+v", out.Produced)
	}
	cleanupTask(t, a.Pool, taskID)

	var gotRunID, gotSource, gotStatus string
	err = a.Pool.QueryRow(ctx,
		`SELECT run_id, source, status FROM backlog_tasks WHERE task_id = $1`, taskID,
	).Scan(&gotRunID, &gotSource, &gotStatus)
	if err != nil {
		t.Fatalf("query back: %v", err)
	}
	if gotRunID != runID {
		t.Errorf("run_id = %q, want %q", gotRunID, runID)
	}
	if gotSource != "review-finding" {
		t.Errorf("source = %q, want review-finding", gotSource)
	}
	if gotStatus != "QUEUED" {
		t.Errorf("status = %q, want QUEUED", gotStatus)
	}
}

// TestCreateTaskDedupesSameRunAndDescription is a regression guard for
// the duplicate-Tasks bug: the Reviewer has no memory of findings raised
// in earlier rounds of the same Run (see CreateTask's doc comment), so
// coder_response can dispatch task.create more than once for what's
// really the same out-of-scope finding. A second call with the same
// (run_id, description) must return the first call's task_id, not insert
// a second row.
func TestCreateTaskDedupesSameRunAndDescription(t *testing.T) {
	a := requirePool(t)
	ctx := context.Background()

	runID := "test-run-dedup-" + time.Now().Format(time.RFC3339Nano)
	in := conductor.ActivityInput{
		RunID: runID,
		Context: map[string]any{
			"source":           "review-finding",
			"task_description": "extract this into a method",
		},
	}

	first, err := a.CreateTask(ctx, in)
	if err != nil {
		t.Fatalf("first CreateTask: %v", err)
	}
	firstID, _ := first.Produced["spawned_task_id"].(string)
	if firstID == "" {
		t.Fatalf("expected spawned_task_id on first call, got %+v", first.Produced)
	}
	cleanupTask(t, a.Pool, firstID)

	second, err := a.CreateTask(ctx, in)
	if err != nil {
		t.Fatalf("second CreateTask: %v", err)
	}
	secondID, _ := second.Produced["spawned_task_id"].(string)
	if secondID != firstID {
		t.Errorf("second call spawned_task_id = %q, want the same %q as the first call (dedup should reuse it)", secondID, firstID)
	}

	var count int
	if err := a.Pool.QueryRow(ctx,
		`SELECT count(*) FROM backlog_tasks WHERE run_id = $1`, runID,
	).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 1 {
		t.Errorf("backlog_tasks rows for run %q = %d, want 1", runID, count)
	}
}

// TestCreateTaskAllowsDifferentDescriptionsSameRun is the flip side of
// the dedup guard: two genuinely different findings raised in the same
// Run must still both get their own Task, not collapse into one.
func TestCreateTaskAllowsDifferentDescriptionsSameRun(t *testing.T) {
	a := requirePool(t)
	ctx := context.Background()

	runID := "test-run-distinct-" + time.Now().Format(time.RFC3339Nano)
	first, err := a.CreateTask(ctx, conductor.ActivityInput{
		RunID: runID,
		Context: map[string]any{
			"source":           "review-finding",
			"task_description": "finding A",
		},
	})
	if err != nil {
		t.Fatalf("first CreateTask: %v", err)
	}
	firstID, _ := first.Produced["spawned_task_id"].(string)
	cleanupTask(t, a.Pool, firstID)

	second, err := a.CreateTask(ctx, conductor.ActivityInput{
		RunID: runID,
		Context: map[string]any{
			"source":           "review-finding",
			"task_description": "finding B",
		},
	})
	if err != nil {
		t.Fatalf("second CreateTask: %v", err)
	}
	secondID, _ := second.Produced["spawned_task_id"].(string)
	cleanupTask(t, a.Pool, secondID)

	if secondID == "" || secondID == firstID {
		t.Errorf("second call spawned_task_id = %q, want a new id distinct from %q", secondID, firstID)
	}
}

func TestCreateTaskDefaultsSourceWhenMissing(t *testing.T) {
	a := requirePool(t)
	ctx := context.Background()

	out, err := a.CreateTask(ctx, conductor.ActivityInput{
		RunID:   "test-run-no-source-" + time.Now().Format(time.RFC3339Nano),
		Context: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	taskID, _ := out.Produced["spawned_task_id"].(string)
	cleanupTask(t, a.Pool, taskID)

	var gotSource string
	if err := a.Pool.QueryRow(ctx, `SELECT source FROM backlog_tasks WHERE task_id = $1`, taskID).Scan(&gotSource); err != nil {
		t.Fatalf("query back: %v", err)
	}
	if gotSource != "unknown" {
		t.Errorf("source = %q, want unknown", gotSource)
	}
}

func TestInsertHumanTaskThenAttachRun(t *testing.T) {
	a := requirePool(t)
	ctx := context.Background()

	wantSourceRef := SourceRef{Kind: "github_issue", Ref: "https://github.com/hhenrique/toy-repo/issues/3"}
	taskID, err := InsertHumanTask(ctx, a.Pool, "hhenrique/toy-repo", "issue-to-pr", "fix the thing", wantSourceRef)
	if err != nil {
		t.Fatalf("InsertHumanTask: %v", err)
	}
	if taskID == "" {
		t.Fatalf("expected a non-empty task id")
	}
	cleanupTask(t, a.Pool, taskID)

	var gotRunID *string
	var gotRepo, gotWorkflow, gotSource, gotStatus, gotSourceRefKind, gotSourceRefRef string
	err = a.Pool.QueryRow(ctx,
		`SELECT run_id, target_repo, workflow, source, status, source_ref_kind, source_ref_ref FROM backlog_tasks WHERE task_id = $1`, taskID,
	).Scan(&gotRunID, &gotRepo, &gotWorkflow, &gotSource, &gotStatus, &gotSourceRefKind, &gotSourceRefRef)
	if err != nil {
		t.Fatalf("query back: %v", err)
	}
	if gotRunID != nil {
		t.Errorf("run_id = %v, want NULL before AttachRun", *gotRunID)
	}
	if gotRepo != "hhenrique/toy-repo" {
		t.Errorf("target_repo = %q, want hhenrique/toy-repo", gotRepo)
	}
	if gotWorkflow != "issue-to-pr" {
		t.Errorf("workflow = %q, want issue-to-pr", gotWorkflow)
	}
	if gotSource != "human" {
		t.Errorf("source = %q, want human", gotSource)
	}
	if gotStatus != "QUEUED" {
		t.Errorf("status = %q, want QUEUED", gotStatus)
	}
	if gotSourceRefKind != wantSourceRef.Kind || gotSourceRefRef != wantSourceRef.Ref {
		t.Errorf("source_ref = {%q, %q}, want %+v", gotSourceRefKind, gotSourceRefRef, wantSourceRef)
	}

	runID := "test-run-" + time.Now().Format(time.RFC3339Nano)
	if err := AttachRun(ctx, a.Pool, taskID, runID); err != nil {
		t.Fatalf("AttachRun: %v", err)
	}

	err = a.Pool.QueryRow(ctx,
		`SELECT run_id, status FROM backlog_tasks WHERE task_id = $1`, taskID,
	).Scan(&gotRunID, &gotStatus)
	if err != nil {
		t.Fatalf("query back after AttachRun: %v", err)
	}
	if gotRunID == nil || *gotRunID != runID {
		t.Errorf("run_id after AttachRun = %v, want %q", gotRunID, runID)
	}
	if gotStatus != "RUNNING" {
		t.Errorf("status after AttachRun = %q, want RUNNING", gotStatus)
	}
}

func TestAttachRunUnknownTaskIDErrors(t *testing.T) {
	a := requirePool(t)
	ctx := context.Background()

	err := AttachRun(ctx, a.Pool, "does-not-exist-"+time.Now().Format(time.RFC3339Nano), "some-run")
	if err == nil {
		t.Fatalf("expected an error for an unknown task_id")
	}
}

func TestListIncludesInsertedHumanTaskWithEmptyRunIDBeforeAttach(t *testing.T) {
	a := requirePool(t)
	ctx := context.Background()

	wantSourceRef := SourceRef{Kind: "github_issue", Ref: "https://github.com/hhenrique/toy-repo/issues/7"}
	taskID, err := InsertHumanTask(ctx, a.Pool, "hhenrique/toy-repo", "issue-to-pr", "list test "+time.Now().Format(time.RFC3339Nano), wantSourceRef)
	if err != nil {
		t.Fatalf("InsertHumanTask: %v", err)
	}
	cleanupTask(t, a.Pool, taskID)

	tasks, err := List(ctx, a.Pool)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var found *Task
	for i := range tasks {
		if tasks[i].TaskID == taskID {
			found = &tasks[i]
		}
	}
	if found == nil {
		t.Fatalf("List did not include inserted task %q", taskID)
	}
	if found.RunID != "" {
		t.Errorf("RunID = %q, want empty before AttachRun", found.RunID)
	}
	if found.TargetRepo != "hhenrique/toy-repo" || found.Workflow != "issue-to-pr" {
		t.Errorf("unexpected fields: %+v", found)
	}
	if found.Status != "QUEUED" {
		t.Errorf("Status = %q, want QUEUED", found.Status)
	}
	if found.SourceRef != wantSourceRef {
		t.Errorf("SourceRef = %+v, want %+v", found.SourceRef, wantSourceRef)
	}
}

// TestListSurfacesZeroValueSourceRefForFreeTextTask is a regression guard
// for the "no known source" case (doc 08: Kind "" means a free-text
// Task) — List must report the zero value, not e.g. a NULL-scan error,
// when InsertHumanTask was called with an empty SourceRef.
func TestListSurfacesZeroValueSourceRefForFreeTextTask(t *testing.T) {
	a := requirePool(t)
	ctx := context.Background()

	taskID, err := InsertHumanTask(ctx, a.Pool, "hhenrique/toy-repo", "issue-to-pr", "free text task "+time.Now().Format(time.RFC3339Nano), SourceRef{})
	if err != nil {
		t.Fatalf("InsertHumanTask: %v", err)
	}
	cleanupTask(t, a.Pool, taskID)

	tasks, err := List(ctx, a.Pool)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var found *Task
	for i := range tasks {
		if tasks[i].TaskID == taskID {
			found = &tasks[i]
		}
	}
	if found == nil {
		t.Fatalf("List did not include inserted task %q", taskID)
	}
	if found.SourceRef != (SourceRef{}) {
		t.Errorf("SourceRef = %+v, want zero value", found.SourceRef)
	}
}

// TestListDerivesStatusFromRunEventsNotStaleColumn is a regression test:
// AttachRun sets backlog_tasks.status = 'RUNNING' once and nothing ever
// updates it again, so a Task whose Run has actually finished used to
// show RUNNING forever in the Work view. List must now report whatever
// run_events' latest transition for that run_id actually says.
func TestListDerivesStatusFromRunEventsNotStaleColumn(t *testing.T) {
	a := requirePool(t)
	ctx := context.Background()

	taskID, err := InsertHumanTask(ctx, a.Pool, "hhenrique/toy-repo", "issue-to-pr", "derive status test "+time.Now().Format(time.RFC3339Nano), SourceRef{})
	if err != nil {
		t.Fatalf("InsertHumanTask: %v", err)
	}
	cleanupTask(t, a.Pool, taskID)
	runID := "test-run-derive-status-" + time.Now().Format(time.RFC3339Nano)
	t.Cleanup(func() {
		a.Pool.Exec(context.Background(), `DELETE FROM run_events WHERE run_id = $1`, runID)
	})
	if err := AttachRun(ctx, a.Pool, taskID, runID); err != nil {
		t.Fatalf("AttachRun: %v", err)
	}

	statusOf := func() string {
		tasks, err := List(ctx, a.Pool)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, task := range tasks {
			if task.TaskID == taskID {
				return task.Status
			}
		}
		t.Fatalf("List did not include task %q", taskID)
		return ""
	}

	// backlog_tasks.status is still 'RUNNING' from AttachRun and no
	// run_events row exists yet — List falls back to the stored column.
	if got := statusOf(); got != "RUNNING" {
		t.Errorf("Status before any run_events row = %q, want RUNNING (fallback)", got)
	}

	// A real mid-DAG transition: still in progress, not one of the
	// terminal/REVIEW_PENDING states, so List reports RUNNING either way
	// (same value here, but derived from run_events now, not the column).
	if _, err := a.Pool.Exec(ctx, `
		INSERT INTO run_events (run_id, workflow, from_step, to_step, occurred_at)
		VALUES ($1, 'issue-to-pr', 'provision', 'plan', now())
	`, runID); err != nil {
		t.Fatalf("insert run_events (plan): %v", err)
	}
	if got := statusOf(); got != "RUNNING" {
		t.Errorf("Status mid-DAG = %q, want RUNNING", got)
	}

	// The Run actually fails (internal/conductor.recordFailure's terminal
	// event) — backlog_tasks.status is still the untouched 'RUNNING' from
	// AttachRun, but List must now report FAILED.
	wantReason := "no on: mapping for outcome \"\""
	if _, err := a.Pool.Exec(ctx, `
		INSERT INTO run_events (run_id, workflow, from_step, to_step, occurred_at, failure_reason)
		VALUES ($1, 'issue-to-pr', 'plan', 'FAILED', now(), $2)
	`, runID, wantReason); err != nil {
		t.Fatalf("insert run_events (FAILED): %v", err)
	}
	if got := statusOf(); got != "FAILED" {
		t.Errorf("Status after a FAILED run_events row = %q, want FAILED", got)
	}

	tasks, err := List(ctx, a.Pool)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var gotReason string
	for _, task := range tasks {
		if task.TaskID == taskID {
			gotReason = task.FailureReason
		}
	}
	if gotReason != wantReason {
		t.Errorf("FailureReason = %q, want %q", gotReason, wantReason)
	}

	var stillStoredAsRunning string
	if err := a.Pool.QueryRow(ctx, `SELECT status FROM backlog_tasks WHERE task_id = $1`, taskID).Scan(&stillStoredAsRunning); err != nil {
		t.Fatalf("query back backlog_tasks.status: %v", err)
	}
	if stillStoredAsRunning != "RUNNING" {
		t.Errorf("backlog_tasks.status = %q, want it to still literally say RUNNING (proving List derives, doesn't rely on this column being updated)", stillStoredAsRunning)
	}
}

// TestListSurfacesOutcomeAndSummaryFromLatestEvent is a regression guard
// for the Work view's own version of the same gap internal/inbox closes:
// a human looking at a stuck Task used to see only its status, nothing
// about what the last step actually produced. List must now surface it.
func TestListSurfacesOutcomeAndSummaryFromLatestEvent(t *testing.T) {
	a := requirePool(t)
	ctx := context.Background()

	taskID, err := InsertHumanTask(ctx, a.Pool, "hhenrique/toy-repo", "issue-to-pr", "summary test "+time.Now().Format(time.RFC3339Nano), SourceRef{})
	if err != nil {
		t.Fatalf("InsertHumanTask: %v", err)
	}
	cleanupTask(t, a.Pool, taskID)
	runID := "test-run-summary-" + time.Now().Format(time.RFC3339Nano)
	t.Cleanup(func() {
		a.Pool.Exec(context.Background(), `DELETE FROM run_events WHERE run_id = $1`, runID)
	})
	if err := AttachRun(ctx, a.Pool, taskID, runID); err != nil {
		t.Fatalf("AttachRun: %v", err)
	}

	produced := map[string]any{"verdict": "changes_required", "findings": []any{
		map[string]any{"severity": "advisory", "scope_classification": "in_scope", "description": "missing test", "location": "foo.go:1"},
	}}
	producedJSON, err := json.Marshal(produced)
	if err != nil {
		t.Fatalf("marshal produced: %v", err)
	}
	if _, err := a.Pool.Exec(ctx, `
		INSERT INTO run_events (run_id, workflow, from_step, to_step, occurred_at, outcome, produced)
		VALUES ($1, 'issue-to-pr', 'review', 'coder_response', now(), 'changes_required', $2)
	`, runID, producedJSON); err != nil {
		t.Fatalf("insert run_events: %v", err)
	}

	tasks, err := List(ctx, a.Pool)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var found *Task
	for i := range tasks {
		if tasks[i].TaskID == taskID {
			found = &tasks[i]
		}
	}
	if found == nil {
		t.Fatalf("List did not include task %q", taskID)
	}
	if found.Outcome != "changes_required" {
		t.Errorf("Outcome = %q, want changes_required", found.Outcome)
	}
	if !strings.Contains(found.Summary, "Findings (1):") {
		t.Errorf("Summary = %q, want it to contain the finding", found.Summary)
	}
}
