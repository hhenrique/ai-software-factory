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
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"factory/internal/eventlog"
	"factory/internal/repositories"
	"factory/internal/workflowdef"
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
	mux.HandleFunc("POST /api/repositories/update", updateRepositoryHandler(pool))
	mux.HandleFunc("POST /api/repositories/delete", deleteRepositoryHandler(pool))
	mux.HandleFunc("GET /api/workflows", listWorkflowsHandler(envOr("CONTROLPLANE_WORKFLOWS_DIR", "workflows")))

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

// WorkflowInfo is docs/04's Workflows section, in full now: a Workflow
// Definition is already checked-in YAML (workflows/), so unlike
// Repositories this needs no persistence — every field here is derived
// fresh from disk on each request. Also backs the Repositories form's
// "Default workflow" combobox (the .Path field), which is why this
// existed before the rest of the section did.
type WorkflowInfo struct {
	Path       string     `json:"path"`
	Workflow   string     `json:"workflow"`
	Version    int        `json:"version"`
	StepCount  int        `json:"step_count"`
	Roles      []RoleInfo `json:"roles"`
	HasTrigger bool       `json:"has_trigger"`
	Valid      bool       `json:"valid"`
	Errors     []string   `json:"errors,omitempty"`
}

// RoleInfo is one entry of a Workflow Definition's roles: block —
// docs/04's Workers (Roles) section reads directly off this once that
// slice lands, rather than its own registry (roles live inline in each
// Workflow Definition, not as standalone data).
type RoleInfo struct {
	Name    string `json:"name"`
	Harness string `json:"harness"`
	Model   string `json:"model"`
}

// listWorkflowsHandler scans dir for *.yaml/*.yml files and parses +
// validates each one — docs/04's Workflows section: "Validation status
// surfaced before a Workflow can be made active." A file that fails to
// parse or validate is still listed, Valid: false with Errors set, rather
// than silently dropped — the whole point is surfacing the problem, not
// hiding it.
func listWorkflowsHandler(dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		infos, err := listWorkflowInfo(dir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, infos)
	}
}

// listWorkflowFiles returns every *.yaml/*.yml file directly under dir,
// sorted. Not recursive — workflows/ has no subdirectories today, and
// there's no reason yet to walk one that might appear.
func listWorkflowFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("list workflow files in %s: %w", dir, err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := filepath.Ext(e.Name())
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		files = append(files, filepath.Join(dir, e.Name()))
	}
	sort.Strings(files)
	return files, nil
}

// listWorkflowInfo parses+validates every workflow file found by
// listWorkflowFiles. A per-file parse/validate failure becomes an invalid
// WorkflowInfo entry, not a request-level error — one broken YAML file
// shouldn't hide every other one from the list.
func listWorkflowInfo(dir string) ([]WorkflowInfo, error) {
	files, err := listWorkflowFiles(dir)
	if err != nil {
		return nil, err
	}
	infos := make([]WorkflowInfo, len(files))
	for i, path := range files {
		infos[i] = loadWorkflowInfo(path)
	}
	return infos, nil
}

func loadWorkflowInfo(path string) WorkflowInfo {
	info := WorkflowInfo{Path: path}

	data, err := os.ReadFile(path)
	if err != nil {
		info.Errors = []string{err.Error()}
		return info
	}
	def, err := workflowdef.Parse(data)
	if err != nil {
		info.Errors = []string{"parse: " + err.Error()}
		return info
	}

	info.Workflow = def.Workflow
	info.Version = def.Version
	info.StepCount = len(def.Steps)
	info.HasTrigger = def.Trigger != nil
	for name, role := range def.Roles {
		info.Roles = append(info.Roles, RoleInfo{Name: name, Harness: role.Harness, Model: role.Model})
	}
	sort.Slice(info.Roles, func(i, j int) bool { return info.Roles[i].Name < info.Roles[j].Name })

	if errs := workflowdef.Validate(def); len(errs) != 0 {
		for _, e := range errs {
			info.Errors = append(info.Errors, e.Error())
		}
		return info
	}
	info.Valid = true
	return info
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

// nameRequest is the body for every /api/repositories/{action} endpoint
// keyed by name only — not a path segment, since a canonical identity
// contains slashes ("github.com/owner/repo").
type nameRequest struct {
	Name string `json:"name"`
}

func setEnabledHandler(pool *pgxpool.Pool, enabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req nameRequest
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

// updateRepositoryRequest is /api/repositories/update's body — name plus
// exactly the fields repositories.Update allows changing (not clone_url:
// see that function's doc comment for why a rename is a delete+re-add).
type updateRepositoryRequest struct {
	Name            string `json:"name"`
	TestCommand     string `json:"test_command"`
	DefaultWorkflow string `json:"default_workflow"`
}

func updateRepositoryHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req updateRepositoryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "malformed JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}
		repo, err := repositories.Update(r.Context(), pool, req.Name, req.TestCommand, req.DefaultWorkflow)
		if errors.Is(err, repositories.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, repo)
	}
}

func deleteRepositoryHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req nameRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "malformed JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}
		err := repositories.Delete(r.Context(), pool, req.Name)
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

// parseGitHubIdentity validates and splits a GitHub repo reference into a
// unique repository name (the canonical "github.com/owner/repo" form,
// verbatim) and an https clone URL — the same URL shape gitops.GitHubSlug
// expects back out of it, so a repository registered here is immediately
// usable by pr.create_and_link and cmd/submittask's gh issue lookups.
//
// Tolerates pasting a full clone URL, not just the bare identity the
// field's placeholder suggests — https://github.com/owner/repo,
// https://github.com/owner/repo.git, and http:// are all normalized down
// to the same canonical form, since a human copying a URL out of a
// browser or `git remote -v` shouldn't have to hand-edit it first.
func parseGitHubIdentity(identity string) (name, cloneURL string, err error) {
	identity = strings.TrimSpace(identity)
	identity = strings.TrimPrefix(identity, "https://")
	identity = strings.TrimPrefix(identity, "http://")
	identity = strings.TrimSuffix(identity, ".git")
	identity = strings.TrimSuffix(identity, "/")

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
