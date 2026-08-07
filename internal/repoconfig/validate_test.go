package repoconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateRootEmptyErrors(t *testing.T) {
	if err := ValidateRoot(""); err == nil {
		t.Fatalf("expected an error for an empty root")
	}
}

func TestValidateRootWritableExistingDirSucceeds(t *testing.T) {
	if err := ValidateRoot(t.TempDir()); err != nil {
		t.Errorf("ValidateRoot(writable dir): %v", err)
	}
}

func TestValidateRootCreatesMissingDirUnderWritableParent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "does", "not", "exist", "yet")
	if err := ValidateRoot(root); err != nil {
		t.Fatalf("ValidateRoot: %v", err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		t.Errorf("expected ValidateRoot to have created %q as a directory", root)
	}
}

// TestValidateRootExistingReadOnlyDirFails reproduces the exact real bug
// reported (`/var/games` — exists, not writable): os.MkdirAll alone
// treats an already-existing directory as success regardless of write
// access, so ValidateRoot must catch this via its write probe, not
// MkdirAll's own return value.
func TestValidateRootExistingReadOnlyDirFails(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root — permission checks don't apply")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(root, 0o755) }) // let t.TempDir() clean up afterward

	err := ValidateRoot(root)
	if err == nil {
		t.Fatalf("expected an error for an existing but read-only directory")
	}
}

// TestValidateRootMissingDirUnderReadOnlyParentFails reproduces the other
// real bug reported (`/xyz` — does not exist, and the process can't
// create it because its parent isn't writable).
func TestValidateRootMissingDirUnderReadOnlyParentFails(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root — permission checks don't apply")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o755) })

	err := ValidateRoot(filepath.Join(parent, "xyz"))
	if err == nil {
		t.Fatalf("expected an error for a missing directory under a read-only parent")
	}
}
