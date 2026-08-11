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

// TestEnforceReadOnlyWorktreeCleanTreeNoViolation is the common case: a
// Planner-role harness that actually stayed read-only.
func TestEnforceReadOnlyWorktreeCleanTreeNoViolation(t *testing.T) {
	requireGit(t)
	dir := newFixtureWorktree(t)

	violated, err := enforceReadOnlyWorktree(context.Background(), dir)
	if err != nil {
		t.Fatalf("enforceReadOnlyWorktree: %v", err)
	}
	if violated {
		t.Errorf("violated = true, want false for a clean tree")
	}
}

// TestEnforceReadOnlyWorktreeResetsModifiedTrackedFile is the doc03
// backstop in action: a harness's read-only flag isn't trusted alone —
// this is the deterministic check that actually catches and undoes a
// stray edit to a file already tracked by git.
func TestEnforceReadOnlyWorktreeResetsModifiedTrackedFile(t *testing.T) {
	requireGit(t)
	dir := newFixtureWorktree(t)

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("tampered\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}

	violated, err := enforceReadOnlyWorktree(context.Background(), dir)
	if err != nil {
		t.Fatalf("enforceReadOnlyWorktree: %v", err)
	}
	if !violated {
		t.Fatalf("violated = false, want true for a modified tracked file")
	}

	got, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatalf("read README after reset: %v", err)
	}
	if string(got) != "hi\n" {
		t.Errorf("README.md = %q after reset, want original content restored", got)
	}
}

// TestEnforceReadOnlyWorktreeRemovesUntrackedFile proves the reset also
// catches a brand-new file, not just a modified tracked one — git
// checkout -- . alone wouldn't remove it, which is why this pairs it
// with git clean -fd.
func TestEnforceReadOnlyWorktreeRemovesUntrackedFile(t *testing.T) {
	requireGit(t)
	dir := newFixtureWorktree(t)

	newFile := filepath.Join(dir, "NEW.md")
	if err := os.WriteFile(newFile, []byte("should not exist\n"), 0o644); err != nil {
		t.Fatalf("write NEW.md: %v", err)
	}

	violated, err := enforceReadOnlyWorktree(context.Background(), dir)
	if err != nil {
		t.Fatalf("enforceReadOnlyWorktree: %v", err)
	}
	if !violated {
		t.Fatalf("violated = false, want true for an untracked new file")
	}
	if _, err := os.Stat(newFile); !os.IsNotExist(err) {
		t.Errorf("NEW.md still exists after reset (stat err = %v), want it removed", err)
	}
}
