package repoconfig

import "testing"

func TestEnvProviderDefaultRoot(t *testing.T) {
	t.Setenv("FACTORY_ROOT", "")

	p := NewEnvProvider()
	got := p.Paths("acme-api", "run-123")

	want := Paths{
		CloneDir:    "/var/lib/factory/repos/acme-api.git",
		WorktreeDir: "/var/lib/factory/worktrees/acme-api/run-123",
	}
	if got != want {
		t.Errorf("Paths() = %+v, want %+v", got, want)
	}
}

func TestEnvProviderCustomRoot(t *testing.T) {
	t.Setenv("FACTORY_ROOT", "/data/factory")

	p := NewEnvProvider()
	got := p.Paths("acme-api", "run-123")

	want := Paths{
		CloneDir:    "/data/factory/repos/acme-api.git",
		WorktreeDir: "/data/factory/worktrees/acme-api/run-123",
	}
	if got != want {
		t.Errorf("Paths() = %+v, want %+v", got, want)
	}
}

func TestEnvProviderDifferentRunsGetDifferentWorktreeDirs(t *testing.T) {
	t.Setenv("FACTORY_ROOT", "")

	p := NewEnvProvider()
	run1 := p.Paths("acme-api", "run-1")
	run2 := p.Paths("acme-api", "run-2")

	if run1.WorktreeDir == run2.WorktreeDir {
		t.Errorf("expected distinct WorktreeDir per run, got %q for both", run1.WorktreeDir)
	}
	if run1.CloneDir != run2.CloneDir {
		t.Errorf("expected the same shared CloneDir across runs of the same repo, got %q vs %q", run1.CloneDir, run2.CloneDir)
	}
}

func TestEnvProviderDifferentReposDoNotShareCloneDir(t *testing.T) {
	t.Setenv("FACTORY_ROOT", "")

	p := NewEnvProvider()
	a := p.Paths("repo-a", "run-1")
	b := p.Paths("repo-b", "run-1")

	if a.CloneDir == b.CloneDir {
		t.Errorf("expected distinct CloneDir per repo, got %q for both", a.CloneDir)
	}
}
