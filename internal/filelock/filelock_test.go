package filelock

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLockSerializesConcurrentAcquirers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")

	unlock1, err := Lock(path)
	if err != nil {
		t.Fatalf("first Lock: %v", err)
	}

	acquired := make(chan Unlock, 1)
	errs := make(chan error, 1)
	go func() {
		unlock2, err := Lock(path)
		if err != nil {
			errs <- err
			return
		}
		acquired <- unlock2
	}()

	select {
	case <-acquired:
		t.Fatalf("second Lock acquired while first still held")
	case err := <-errs:
		t.Fatalf("second Lock: %v", err)
	case <-time.After(200 * time.Millisecond):
		// expected: second Lock is still blocked
	}

	if err := unlock1(); err != nil {
		t.Fatalf("unlock1: %v", err)
	}

	select {
	case unlock2 := <-acquired:
		unlock2()
	case err := <-errs:
		t.Fatalf("second Lock: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatalf("second Lock did not acquire after first was released")
	}
}

func TestLockCreatesFileIfMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")

	unlock, err := Lock(path)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	defer unlock()

	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected Lock to create %s: %v", path, err)
	}
}

func TestLockFailsIfParentDirMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "does", "not", "exist", "test.lock")
	if _, err := Lock(path); err == nil {
		t.Fatalf("expected an error locking a path whose parent directory doesn't exist")
	}
}
