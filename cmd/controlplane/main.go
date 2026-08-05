// Command controlplane is the real control plane (docs/04) — built in
// vertical slices, one section at a time. This slice: Repositories only.
// Unlike cmd/runsview (a throwaway server-rendered visibility tool, see
// its own doc comment), this is meant to be preserved and extended: a
// small SPA (one HTML shell + vanilla JS hitting a JSON API, no
// framework/build step) with a collapsible left-side nav that grows one
// section at a time as each slice lands. Static assets are embedded so
// the binary stays self-contained, same spirit as every other cmd/* here.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"factory/internal/eventlog"
	"factory/internal/repositories"
)

//go:embed static
var staticFS embed.FS

func main() {
	ctx := context.Background()
	pool, err := eventlog.NewPool(ctx)
	if err != nil {
		log.Fatalf("controlplane: %v", err)
	}
	defer pool.Close()

	addr := envOr("CONTROLPLANE_ADDR", ":8082")

	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("controlplane: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.FS(static)))
	mux.HandleFunc("GET /api/repositories", listRepositoriesHandler(pool))
	mux.HandleFunc("POST /api/repositories", createRepositoryHandler(pool))
	mux.HandleFunc("POST /api/repositories/enable", setEnabledHandler(pool, true))
	mux.HandleFunc("POST /api/repositories/disable", setEnabledHandler(pool, false))

	log.Printf("controlplane: listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("controlplane: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func listRepositoriesHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		repos, err := repositories.List(r.Context(), pool)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, repos)
	}
}

// createRepositoryRequest mirrors the UI's "Canonical identity" field
// (e.g. "github.com/hhenrique/toy-repo") rather than asking for a raw
// clone URL directly — GitHub is the only provider this slice manages, so
// the identity alone is enough to derive one.
type createRepositoryRequest struct {
	Identity        string `json:"identity"`
	TestCommand     string `json:"test_command"`
	DefaultWorkflow string `json:"default_workflow"`
}

func createRepositoryHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createRepositoryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "malformed JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}

		name, cloneURL, err := parseGitHubIdentity(req.Identity)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		repo, err := repositories.Insert(r.Context(), pool, name, cloneURL, req.TestCommand, req.DefaultWorkflow)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusCreated, repo)
	}
}

// setEnabledRequest is the body for /api/repositories/{enable,disable} —
// name only, not a path segment, since a canonical identity contains
// slashes ("github.com/owner/repo").
type setEnabledRequest struct {
	Name string `json:"name"`
}

func setEnabledHandler(pool *pgxpool.Pool, enabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req setEnabledRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "malformed JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}
		err := repositories.SetEnabled(r.Context(), pool, req.Name, enabled)
		if errors.Is(err, repositories.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// parseGitHubIdentity validates and splits a "github.com/owner/repo"
// identity into a unique repository name (the identity itself, verbatim)
// and an https clone URL — the same URL shape gitops.GitHubSlug expects
// back out of it, so a repository registered here is immediately usable
// by pr.create_and_link and cmd/submittask's gh issue lookups.
func parseGitHubIdentity(identity string) (name, cloneURL string, err error) {
	identity = strings.TrimSuffix(strings.TrimSpace(identity), "/")
	const prefix = "github.com/"
	if !strings.HasPrefix(identity, prefix) {
		return "", "", errors.New(`identity must look like "github.com/<owner>/<repo>" — GitHub is the only managed provider in this release`)
	}
	ownerRepo := strings.TrimPrefix(identity, prefix)
	parts := strings.Split(ownerRepo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", errors.New(`identity must look like "github.com/<owner>/<repo>"`)
	}
	return identity, "https://" + identity + ".git", nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("controlplane: encode response: %v", err)
	}
}
