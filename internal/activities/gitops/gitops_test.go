package gitops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"factory/internal/conductor"
	"factory/internal/repoconfig"
)

// testProvider is a trivial repoconfig.Provider rooted at a temp dir, so
// each test gets an isolated clone/worktree tree.
type testProvider struct{ root string }

func (p testProvider) Paths(ctx context.Context, repo, runID string) (repoconfig.Paths, error) {
	return repoconfig.Paths{
		CloneDir:    filepath.Join(p.root, "repos", repo+".git"),
		WorktreeDir: filepath.Join(p.root, "worktrees", repo, runID),
	}, nil
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}
}

// newFixtureRepo creates a throwaway git repo with one commit on "main"
// and returns its filesystem path — usable directly as a clone URL.
func newFixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func TestWorktreeCreateClonesAndAddsWorktree(t *testing.T) {
	requireGit(t)
	fixture := newFixtureRepo(t)
	activities := &Activities{Paths: testProvider{root: t.TempDir()}}

	out, err := activities.WorktreeCreate(context.Background(), conductor.ActivityInput{
		RunID: "run-1",
		Repo:  conductor.Repo{Name: "acme", CloneURL: fixture},
	})
	if err != nil {
		t.Fatalf("WorktreeCreate: %v", err)
	}

	worktreePath, _ := out.Produced["worktree_path"].(string)
	if worktreePath == "" {
		t.Fatalf("expected worktree_path in Produced, got %+v", out.Produced)
	}
	if _, err := os.Stat(filepath.Join(worktreePath, "README.md")); err != nil {
		t.Errorf("expected checked-out README.md in worktree: %v", err)
	}

	if branch, _ := out.Produced["branch"].(string); branch != "factory/run-1" {
		t.Errorf("branch = %q, want factory/run-1", branch)
	}

	cloneDir, _ := out.Produced["clone_dir"].(string)
	if _, err := os.Stat(cloneDir); err != nil {
		t.Errorf("expected clone_dir %q to exist: %v", cloneDir, err)
	}
}

func TestWorktreeCreateSecondRunGetsIsolatedWorktreeSharedClone(t *testing.T) {
	requireGit(t)
	fixture := newFixtureRepo(t)
	activities := &Activities{Paths: testProvider{root: t.TempDir()}}

	out1, err := activities.WorktreeCreate(context.Background(), conductor.ActivityInput{
		RunID: "run-1",
		Repo:  conductor.Repo{Name: "acme", CloneURL: fixture},
	})
	if err != nil {
		t.Fatalf("WorktreeCreate run-1: %v", err)
	}
	out2, err := activities.WorktreeCreate(context.Background(), conductor.ActivityInput{
		RunID: "run-2",
		Repo:  conductor.Repo{Name: "acme", CloneURL: fixture},
	})
	if err != nil {
		t.Fatalf("WorktreeCreate run-2: %v", err)
	}

	if out1.Produced["worktree_path"] == out2.Produced["worktree_path"] {
		t.Errorf("expected distinct worktree paths per run, both were %v", out1.Produced["worktree_path"])
	}
	if out1.Produced["clone_dir"] != out2.Produced["clone_dir"] {
		t.Errorf("expected the shared clone dir to be reused across runs of the same repo: %v vs %v",
			out1.Produced["clone_dir"], out2.Produced["clone_dir"])
	}
}

func TestWorktreeCreateSecondRunFetchesUpstreamChanges(t *testing.T) {
	requireGit(t)
	fixture := newFixtureRepo(t)
	activities := &Activities{Paths: testProvider{root: t.TempDir()}}

	if _, err := activities.WorktreeCreate(context.Background(), conductor.ActivityInput{
		RunID: "run-1",
		Repo:  conductor.Repo{Name: "acme", CloneURL: fixture},
	}); err != nil {
		t.Fatalf("WorktreeCreate run-1: %v", err)
	}

	// New commit lands upstream after the first Run's worktree was created.
	if err := os.WriteFile(filepath.Join(fixture, "NEW.md"), []byte("new file\n"), 0o644); err != nil {
		t.Fatalf("write new upstream file: %v", err)
	}
	runGit(t, fixture, "add", "NEW.md")
	runGit(t, fixture, "commit", "-m", "second commit")

	out2, err := activities.WorktreeCreate(context.Background(), conductor.ActivityInput{
		RunID: "run-2",
		Repo:  conductor.Repo{Name: "acme", CloneURL: fixture},
	})
	if err != nil {
		t.Fatalf("WorktreeCreate run-2: %v", err)
	}

	worktreePath, _ := out2.Produced["worktree_path"].(string)
	if _, err := os.Stat(filepath.Join(worktreePath, "NEW.md")); err != nil {
		t.Errorf("expected run-2's worktree to include the upstream commit made after run-1 (shared clone must actually fetch, not just clone-once): %v", err)
	}
}

func TestWorktreeCreateDoesNotPruneAnEarlierRunsBranch(t *testing.T) {
	requireGit(t)
	fixture := newFixtureRepo(t)
	activities := &Activities{Paths: testProvider{root: t.TempDir()}}

	out1, err := activities.WorktreeCreate(context.Background(), conductor.ActivityInput{
		RunID: "run-1",
		Repo:  conductor.Repo{Name: "acme", CloneURL: fixture},
	})
	if err != nil {
		t.Fatalf("WorktreeCreate run-1: %v", err)
	}

	// run-1's branch exists only in the shared clone, never on the actual
	// "upstream" (the fixture repo) — a second Run's `fetch --prune` must
	// not treat it as stale and delete it, even though it looks exactly
	// like a deleted-upstream branch would.
	if _, err := activities.WorktreeCreate(context.Background(), conductor.ActivityInput{
		RunID: "run-2",
		Repo:  conductor.Repo{Name: "acme", CloneURL: fixture},
	}); err != nil {
		t.Fatalf("WorktreeCreate run-2: %v", err)
	}

	cloneDir, _ := out1.Produced["clone_dir"].(string)
	cmd := exec.Command("git", "--git-dir=.", "rev-parse", "--verify", "refs/heads/factory/run-1")
	cmd.Dir = cloneDir
	if err := cmd.Run(); err != nil {
		t.Errorf("expected refs/heads/factory/run-1 to survive run-2's fetch --prune, but it's gone: %v", err)
	}

	// run-1's worktree itself must still be usable too.
	worktreePath, _ := out1.Produced["worktree_path"].(string)
	statusCmd := exec.Command("git", "status")
	statusCmd.Dir = worktreePath
	if out, err := statusCmd.CombinedOutput(); err != nil {
		t.Errorf("run-1's worktree is no longer usable after run-2: %v: %s", err, out)
	}
}

func TestWorktreeCreateRetryOfSameRunIDIsIdempotent(t *testing.T) {
	requireGit(t)
	fixture := newFixtureRepo(t)
	activities := &Activities{Paths: testProvider{root: t.TempDir()}}

	in := conductor.ActivityInput{
		RunID: "run-1",
		Repo:  conductor.Repo{Name: "acme", CloneURL: fixture},
	}

	if _, err := activities.WorktreeCreate(context.Background(), in); err != nil {
		t.Fatalf("first WorktreeCreate: %v", err)
	}
	// Simulate an at-least-once Activity redelivery for the same Run.
	out2, err := activities.WorktreeCreate(context.Background(), in)
	if err != nil {
		t.Fatalf("second WorktreeCreate (retry): %v", err)
	}
	worktreePath, _ := out2.Produced["worktree_path"].(string)
	if _, err := os.Stat(filepath.Join(worktreePath, "README.md")); err != nil {
		t.Errorf("expected checked-out README.md in worktree after retry: %v", err)
	}
}

func TestWorktreeCreateMissingCloneURL(t *testing.T) {
	activities := &Activities{Paths: testProvider{root: t.TempDir()}}
	_, err := activities.WorktreeCreate(context.Background(), conductor.ActivityInput{RunID: "run-1"})
	if err == nil {
		t.Fatalf("expected error for missing CloneURL")
	}
}

func TestWorktreeCreateMissingRunID(t *testing.T) {
	activities := &Activities{Paths: testProvider{root: t.TempDir()}}
	_, err := activities.WorktreeCreate(context.Background(), conductor.ActivityInput{
		Repo: conductor.Repo{Name: "acme", CloneURL: "/does/not/matter"},
	})
	if err == nil {
		t.Fatalf("expected error for missing RunID")
	}
}

func TestBranchName(t *testing.T) {
	if got := BranchName("run-123"); got != "factory/run-123" {
		t.Errorf("BranchName = %q, want factory/run-123", got)
	}
}

func TestGitHubSlug(t *testing.T) {
	cases := []struct {
		cloneURL string
		want     string
		wantErr  bool
	}{
		{cloneURL: "https://github.com/hhenrique/toy-repo.git", want: "hhenrique/toy-repo"},
		{cloneURL: "https://github.com/hhenrique/toy-repo", want: "hhenrique/toy-repo"},
		{cloneURL: "git@github.com:hhenrique/toy-repo.git", wantErr: true},
		{cloneURL: "https://github.com/", wantErr: true},
	}
	for _, tc := range cases {
		got, err := GitHubSlug(tc.cloneURL)
		if tc.wantErr {
			if err == nil {
				t.Errorf("GitHubSlug(%q): expected error", tc.cloneURL)
			}
			continue
		}
		if err != nil {
			t.Errorf("GitHubSlug(%q): unexpected error: %v", tc.cloneURL, err)
			continue
		}
		if got != tc.want {
			t.Errorf("GitHubSlug(%q) = %q, want %q", tc.cloneURL, got, tc.want)
		}
	}
}
