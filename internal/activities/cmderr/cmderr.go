// Package cmderr builds human-readable errors for a failed external
// command — shared by every Activity that shells out (gitops, pr,
// harness's three CLI adapters), so a failure ends up phrased the same
// way regardless of which one hit it.
//
// Found live: a real Coder-step failure surfaced to a human as
// "codex: exit status 1: Reading additional input from stdin..." — the
// bare exit code adds nothing a human can act on once there's real
// diagnostic text (stderr) sitting right next to it, and duplicating it
// as its own clause just pushes the actually useful text further right.
// Wrap drops it whenever there's something better to show.
package cmderr

import (
	"fmt"
	"os/exec"
	"strings"
)

// Wrap builds "label: diagnostic" when diagnostic (trimmed) is non-empty
// — that's what a human can act on — falling back to "label: err" (e.g.
// "label: exit status 1") only when there's truly nothing else, since a
// bare exit code alone isn't actionable but is still better than
// silence.
func Wrap(label string, err error, diagnostic string) error {
	diagnostic = strings.TrimSpace(diagnostic)
	if diagnostic != "" {
		return fmt.Errorf("%s: %s", label, diagnostic)
	}
	return fmt.Errorf("%s: %w", label, err)
}

// Stderr extracts captured stderr text from err, if err is an
// *exec.ExitError carrying any. Only populated when the command was run
// via cmd.Output() — cmd.CombinedOutput() interleaves stderr into the
// returned output instead, which callers already have as a separate
// value to pass to Wrap directly.
func Stderr(err error) string {
	if exitErr, ok := err.(*exec.ExitError); ok {
		return string(exitErr.Stderr)
	}
	return ""
}
