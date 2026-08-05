package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"factory/internal/eventlog"
	"factory/internal/repositories"
)

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

func TestParseGitHubIdentity(t *testing.T) {
	name, cloneURL, err := parseGitHubIdentity("github.com/hhenrique/toy-repo")
	if err != nil {
		t.Fatalf("parseGitHubIdentity: %v", err)
	}
	if name != "github.com/hhenrique/toy-repo" {
		t.Errorf("name = %q", name)
	}
	if cloneURL != "https://github.com/hhenrique/toy-repo.git" {
		t.Errorf("cloneURL = %q", cloneURL)
	}

	for _, bad := range []string{"", "gitlab.com/a/b", "github.com/onlyowner", "github.com/"} {
		if _, _, err := parseGitHubIdentity(bad); err == nil {
			t.Errorf("parseGitHubIdentity(%q): expected an error", bad)
		}
	}
}

func TestParseGitHubIdentityTolerantOfPastedURLs(t *testing.T) {
	cases := []string{
		"https://github.com/hhenrique/toy-repo",
		"https://github.com/hhenrique/toy-repo.git",
		"http://github.com/hhenrique/toy-repo",
		"github.com/hhenrique/toy-repo/",
		"  github.com/hhenrique/toy-repo  ",
	}
	for _, in := range cases {
		name, cloneURL, err := parseGitHubIdentity(in)
		if err != nil {
			t.Errorf("parseGitHubIdentity(%q): %v", in, err)
			continue
		}
		if name != "github.com/hhenrique/toy-repo" {
			t.Errorf("parseGitHubIdentity(%q): name = %q, want github.com/hhenrique/toy-repo", in, name)
		}
		if cloneURL != "https://github.com/hhenrique/toy-repo.git" {
			t.Errorf("parseGitHubIdentity(%q): cloneURL = %q", in, cloneURL)
		}
	}
}

func TestListWorkflowFiles(t *testing.T) {
	files, err := listWorkflowFiles("../../workflows")
	if err != nil {
		t.Fatalf("listWorkflowFiles: %v", err)
	}
	found := false
	for _, f := range files {
		if f == "../../workflows/issue-to-pr-claude-only.yaml" {
			found = true
		}
	}
	if !found {
		t.Errorf("listWorkflowFiles = %v, want it to include issue-to-pr-claude-only.yaml", files)
	}
}

func TestListWorkflowFilesMissingDirErrors(t *testing.T) {
	if _, err := listWorkflowFiles("../../does-not-exist"); err == nil {
		t.Fatalf("expected an error for a missing directory")
	}
}

func TestCreateListEnableDisableRepositoryHandlers(t *testing.T) {
	pool := requirePool(t)
	name := "test-controlplane-" + time.Now().Format("20060102T150405.000000000")
	identity := "github.com/hhenrique/" + name

	createBody := `{"identity":"` + identity + `","test_command":"node --check script.js","default_workflow":"workflows/issue-to-pr-claude-only.yaml"}`
	req := httptest.NewRequest(http.MethodPost, "/api/repositories", strings.NewReader(createBody))
	rec := httptest.NewRecorder()
	createRepositoryHandler(pool)(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var created repositories.Repository
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("create: decode response: %v", err)
	}
	if created.Name != identity || !created.Enabled {
		t.Fatalf("create: unexpected response %+v", created)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/repositories", nil)
	rec = httptest.NewRecorder()
	listRepositoriesHandler(pool)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status = %d", rec.Code)
	}
	var listed []repositories.Repository
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("list: decode response: %v", err)
	}
	found := false
	for _, r := range listed {
		if r.Name == identity {
			found = true
		}
	}
	if !found {
		t.Fatalf("list: did not include %q", identity)
	}

	disableBody := `{"name":"` + identity + `"}`
	req = httptest.NewRequest(http.MethodPost, "/api/repositories/disable", strings.NewReader(disableBody))
	rec = httptest.NewRecorder()
	setEnabledHandler(pool, false)(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("disable: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	got, err := repositories.Get(context.Background(), pool, identity)
	if err != nil {
		t.Fatalf("Get after disable: %v", err)
	}
	if got.Enabled {
		t.Errorf("Enabled = true after disable")
	}

	req = httptest.NewRequest(http.MethodPost, "/api/repositories/enable", strings.NewReader(disableBody))
	rec = httptest.NewRecorder()
	setEnabledHandler(pool, true)(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("enable: status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestSetEnabledHandlerUnknownNameReturns404(t *testing.T) {
	pool := requirePool(t)
	body := `{"name":"does-not-exist-` + time.Now().Format(time.RFC3339Nano) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/repositories/enable", strings.NewReader(body))
	rec := httptest.NewRecorder()
	setEnabledHandler(pool, true)(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestCreateRepositoryHandlerMalformedBody(t *testing.T) {
	pool := requirePool(t)
	req := httptest.NewRequest(http.MethodPost, "/api/repositories", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	createRepositoryHandler(pool)(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
