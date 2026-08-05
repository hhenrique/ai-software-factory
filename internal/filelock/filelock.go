// Package filelock provides a simple per-path advisory lock via flock(2).
// It exists to serialize concurrent Activity calls that touch the same
// shared git clone (see internal/activities/gitops): concurrent Runs
// against the same repo would otherwise race on git's own ref/index
// locks inside that shared clone.
//
// flock is kernel-managed: the lock is released automatically when the
// holding file descriptor is closed or the process exits, even on a hard
// crash — no stale-lock cleanup logic needed, unlike a "check whether a
// lock file exists, then create it" convention (which is also not atomic:
// the check and the create are separate steps, racing against another
// acquirer between them).
package filelock

import (
	"fmt"
	"os"
	"syscall"
)

// Unlock releases a Lock. Safe to call at most once — it closes the
// underlying file descriptor, which is what actually releases the flock.
type Unlock func() error

// Lock acquires an exclusive advisory lock on path (the file is created
// if it doesn't exist), blocking until it's available.
func Lock(path string) (Unlock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("filelock: open %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("filelock: flock %s: %w", path, err)
	}
	return f.Close, nil
}
