package pr

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"factory/internal/conductor"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}
}

func TestCreateAndLinkMissingWorktreePath(t *testing.T) {
	a := &Activities{}
	_, err := a.CreateAndLink(context.Background(), conductor.ActivityInput{
		Context: map[string]any{"branch": "factory/run-1"},
	})
	if err == nil {
		t.Fatalf("expected error for missing worktree_path")
	}
}

func TestCreateAndLinkMissingBranch(t *testing.T) {
	a := &Activities{}
	_, err := a.CreateAndLink(context.Background(), conductor.ActivityInput{
		Context: map[string]any{"worktree_path": t.TempDir()},
	})
	if err == nil {
		t.Fatalf("expected error for missing branch")
	}
}

// pushBranch is the one piece of pr.create_and_link testable hermetically
// — gh pr create fundamentally requires a real GitHub-hosted remote (gh
// validates against the live API), so the PR-creation half is verified
// separately via a live, manual check against a real repo, not in this
// automated suite.
func TestPushBranchLandsOnRemote(t *testing.T) {
	requireGit(t)

	remote := t.TempDir()
	runGit(t, remote, "init", "-q", "--bare", "-b", "main")

	work := t.TempDir()
	runGit(t, work, "init", "-q", "-b", "main")
	runGit(t, work, "remote", "add", "origin", remote)
	runGit(t, work, "config", "user.email", "test@example.com")
	runGit(t, work, "config", "user.name", "test")
	writeFile(t, filepath.Join(work, "f.txt"), "hi\n")
	runGit(t, work, "add", "f.txt")
	runGit(t, work, "commit", "-q", "-m", "init")
	runGit(t, work, "push", "-q", "origin", "main")

	runGit(t, work, "checkout", "-q", "-b", "factory/run-1")
	writeFile(t, filepath.Join(work, "f.txt"), "hi again\n")
	runGit(t, work, "add", "f.txt")
	runGit(t, work, "commit", "-q", "-m", "change")

	if err := pushBranch(context.Background(), work, "factory/run-1"); err != nil {
		t.Fatalf("pushBranch: %v", err)
	}

	out := runGitOutput(t, remote, "--git-dir=.", "rev-parse", "--verify", "refs/heads/factory/run-1")
	if strings.TrimSpace(out) == "" {
		t.Errorf("expected refs/heads/factory/run-1 to exist on the remote after push")
	}
}

// TestPushBranchSurvivesLocalHistoryDiverging reproduces the shape of a
// retried Run: worktree.create's `-B` semantics reset the local branch to
// its base on every invocation (see gitops.WorktreeCreate), so a second
// push of the "same" Run's branch routinely has a different, diverged
// commit rather than a fast-forward of what was pushed before. A plain
// `git push` rejects that as a conflict; pushBranch must not.
func TestPushBranchSurvivesLocalHistoryDiverging(t *testing.T) {
	requireGit(t)

	remote := t.TempDir()
	runGit(t, remote, "init", "-q", "--bare", "-b", "main")

	base := t.TempDir()
	runGit(t, base, "init", "-q", "-b", "main")
	runGit(t, base, "remote", "add", "origin", remote)
	runGit(t, base, "config", "user.email", "test@example.com")
	runGit(t, base, "config", "user.name", "test")
	writeFile(t, filepath.Join(base, "f.txt"), "hi\n")
	runGit(t, base, "add", "f.txt")
	runGit(t, base, "commit", "-q", "-m", "init")
	runGit(t, base, "push", "-q", "origin", "main")

	commitAndPush := func(content string) {
		work := t.TempDir()
		runGit(t, work, "clone", "-q", remote, ".")
		runGit(t, work, "config", "user.email", "test@example.com")
		runGit(t, work, "config", "user.name", "test")
		runGit(t, work, "checkout", "-q", "-B", "factory/run-1", "origin/main") // mirrors worktree.create's `-B ... origin/<default>`
		writeFile(t, filepath.Join(work, "g.txt"), content)
		runGit(t, work, "add", "g.txt")
		runGit(t, work, "commit", "-q", "-m", "attempt")
		if err := pushBranch(context.Background(), work, "factory/run-1"); err != nil {
			t.Fatalf("pushBranch(%q): %v", content, err)
		}
	}

	commitAndPush("first attempt")
	firstSHA := strings.TrimSpace(runGitOutput(t, remote, "--git-dir=.", "rev-parse", "refs/heads/factory/run-1"))

	commitAndPush("second attempt, different content") // diverged local history, same branch name
	secondSHA := strings.TrimSpace(runGitOutput(t, remote, "--git-dir=.", "rev-parse", "refs/heads/factory/run-1"))

	if firstSHA == secondSHA {
		t.Fatalf("expected the remote branch to actually update on the second push")
	}
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

func runGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
