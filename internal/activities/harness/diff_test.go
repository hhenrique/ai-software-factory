package harness

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}
}

func newFixtureWorktree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
	} {
		runGitT(t, dir, args...)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGitT(t, dir, "add", "README.md")
	runGitT(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func runGitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func TestCommitWorktreeChangesReturnsDiffAndCommits(t *testing.T) {
	requireGit(t)
	dir := newFixtureWorktree(t)

	if err := os.WriteFile(filepath.Join(dir, "NEW.md"), []byte("new content\n"), 0o644); err != nil {
		t.Fatalf("write NEW.md: %v", err)
	}

	diff, err := commitWorktreeChanges(context.Background(), dir, "execute", 1)
	if err != nil {
		t.Fatalf("commitWorktreeChanges: %v", err)
	}
	if !strings.Contains(diff, "new content") {
		t.Errorf("diff %q missing new content", diff)
	}

	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = dir
	status, err := statusCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v: %s", err, status)
	}
	if strings.TrimSpace(string(status)) != "" {
		t.Errorf("expected a clean tree after commit, got:\n%s", status)
	}
}

func TestCommitWorktreeChangesNoChangesReturnsEmpty(t *testing.T) {
	requireGit(t)
	dir := newFixtureWorktree(t)

	diff, err := commitWorktreeChanges(context.Background(), dir, "execute", 1)
	if err != nil {
		t.Fatalf("commitWorktreeChanges: %v", err)
	}
	if diff != "" {
		t.Errorf("diff = %q, want empty when nothing changed", diff)
	}
}
