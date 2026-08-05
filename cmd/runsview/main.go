// Command runsview is a throwaway visibility tool, not the control plane
// (docs/04) — it exists only so a human can see what Runs exist and what
// state they're in without reading Temporal's raw history or grepping
// worker logs. No API layer, no design investment: two server-rendered
// HTML pages, direct SQL against the projection store
// (internal/eventlog), nothing else. Delete or replace wholesale once the
// real control plane's Overview section gets built — nothing here is
// meant to be preserved or extended.
package main

import (
	"context"
	"html/template"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"factory/internal/eventlog"
)

func main() {
	ctx := context.Background()
	pool, err := eventlog.NewPool(ctx)
	if err != nil {
		log.Fatalf("runsview: %v", err)
	}
	defer pool.Close()

	addr := envOr("RUNSVIEW_ADDR", ":8081")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", listHandler(pool))
	mux.HandleFunc("GET /runs/{run_id}", detailHandler(pool))

	log.Printf("runsview: listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("runsview: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type runRow struct {
	RunID        string
	Workflow     string
	CurrentState string
	StartedAt    time.Time
	LastUpdate   time.Time
	Transitions  int
}

var listTemplate = template.Must(template.New("list").Parse(`<!doctype html>
<meta charset="utf-8">
<title>runsview</title>
<style>
body { font-family: monospace; margin: 2em; }
table { border-collapse: collapse; }
th, td { border: 1px solid #999; padding: 0.3em 0.6em; text-align: left; }
</style>
<h1>Runs</h1>
<p>Throwaway visibility tool — not the control plane. See cmd/runsview's doc comment.</p>
{{if not .}}<p>No runs recorded yet.</p>{{end}}
<table>
<tr><th>Run ID</th><th>Workflow</th><th>Current state</th><th>Started</th><th>Last update</th><th>Transitions</th></tr>
{{range .}}
<tr>
  <td><a href="/runs/{{.RunID}}">{{.RunID}}</a></td>
  <td>{{.Workflow}}</td>
  <td>{{.CurrentState}}</td>
  <td>{{.StartedAt.Format "2006-01-02 15:04:05"}}</td>
  <td>{{.LastUpdate.Format "2006-01-02 15:04:05"}}</td>
  <td>{{.Transitions}}</td>
</tr>
{{end}}
</table>
`))

func listHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := pool.Query(r.Context(), `
			SELECT run_id, workflow, to_step, started_at, last_update, transitions
			FROM (
				SELECT DISTINCT ON (run_id)
					run_id, workflow, to_step,
					min(occurred_at) OVER (PARTITION BY run_id) AS started_at,
					occurred_at AS last_update,
					count(*) OVER (PARTITION BY run_id) AS transitions
				FROM run_events
				ORDER BY run_id, occurred_at DESC, id DESC
			) latest
			ORDER BY last_update DESC
		`)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var runs []runRow
		for rows.Next() {
			var run runRow
			if err := rows.Scan(&run.RunID, &run.Workflow, &run.CurrentState, &run.StartedAt, &run.LastUpdate, &run.Transitions); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			runs = append(runs, run)
		}
		if err := rows.Err(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := listTemplate.Execute(w, runs); err != nil {
			log.Printf("runsview: render list: %v", err)
		}
	}
}

type transitionRow struct {
	FromStep      string
	ToStep        string
	StepID        string
	AttemptNumber int
	TokenDelta    int
	ActivityCalls int
	OccurredAt    time.Time
}

var detailTemplate = template.Must(template.New("detail").Parse(`<!doctype html>
<meta charset="utf-8">
<title>runsview: {{.RunID}}</title>
<style>
body { font-family: monospace; margin: 2em; }
table { border-collapse: collapse; }
th, td { border: 1px solid #999; padding: 0.3em 0.6em; text-align: left; }
</style>
<p><a href="/">&larr; all runs</a></p>
<h1>{{.RunID}}</h1>
{{if not .Events}}<p>No events found for this run.</p>{{end}}
<table>
<tr><th>Time</th><th>Step</th><th>From</th><th>To</th><th>Attempt</th><th>Token delta</th><th>Activity calls</th></tr>
{{range .Events}}
<tr>
  <td>{{.OccurredAt.Format "2006-01-02 15:04:05.000"}}</td>
  <td>{{.StepID}}</td>
  <td>{{.FromStep}}</td>
  <td>{{.ToStep}}</td>
  <td>{{.AttemptNumber}}</td>
  <td>{{.TokenDelta}}</td>
  <td>{{.ActivityCalls}}</td>
</tr>
{{end}}
</table>
`))

func detailHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("run_id")

		rows, err := pool.Query(r.Context(), `
			SELECT from_step, to_step, coalesce(step_id, ''), coalesce(attempt_number, 0),
			       token_delta, activity_calls, occurred_at
			FROM run_events
			WHERE run_id = $1
			ORDER BY occurred_at ASC, id ASC
		`, runID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var events []transitionRow
		for rows.Next() {
			var ev transitionRow
			if err := rows.Scan(&ev.FromStep, &ev.ToStep, &ev.StepID, &ev.AttemptNumber,
				&ev.TokenDelta, &ev.ActivityCalls, &ev.OccurredAt); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			events = append(events, ev)
		}
		if err := rows.Err(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		data := struct {
			RunID  string
			Events []transitionRow
		}{RunID: runID, Events: events}
		if err := detailTemplate.Execute(w, data); err != nil {
			log.Printf("runsview: render detail: %v", err)
		}
	}
}
