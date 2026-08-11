package cmderr

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestWrapPrefersDiagnosticOverBareError(t *testing.T) {
	err := Wrap("codex", errors.New("exit status 1"), "Reading additional input from stdin...")
	if err.Error() != "codex: Reading additional input from stdin..." {
		t.Errorf("Error() = %q, want the diagnostic text, not the bare exit status", err.Error())
	}
}

func TestWrapTrimsDiagnosticWhitespace(t *testing.T) {
	err := Wrap("codex", errors.New("exit status 1"), "\n  Reading additional input from stdin...\n\n")
	if err.Error() != "codex: Reading additional input from stdin..." {
		t.Errorf("Error() = %q, want trimmed diagnostic text", err.Error())
	}
}

func TestWrapFallsBackToBareErrorWhenNoDiagnostic(t *testing.T) {
	underlying := errors.New("exit status 1")
	err := Wrap("codex", underlying, "")
	if err.Error() != "codex: exit status 1" {
		t.Errorf("Error() = %q, want the bare error as a last resort", err.Error())
	}
	if !errors.Is(err, underlying) {
		t.Errorf("Wrap should still preserve the underlying error in the chain (errors.Is)")
	}
}

func TestWrapFallsBackWhenDiagnosticIsOnlyWhitespace(t *testing.T) {
	err := Wrap("codex", errors.New("exit status 1"), "   \n  ")
	if err.Error() != "codex: exit status 1" {
		t.Errorf("Error() = %q, want the bare error when diagnostic is only whitespace", err.Error())
	}
}

func TestStderrExtractsFromExitError(t *testing.T) {
	cmd := exec.Command("sh", "-c", "echo 'boom' >&2; exit 1")
	_, err := cmd.Output() // Output(), not Run() — only Output() populates ExitError.Stderr
	if err == nil {
		t.Fatalf("expected the command to fail")
	}
	got := strings.TrimSpace(Stderr(err))
	if got != "boom" {
		t.Errorf("Stderr(err) = %q, want %q", got, "boom")
	}
}

func TestStderrEmptyForNonExitError(t *testing.T) {
	if got := Stderr(errors.New("not an exit error")); got != "" {
		t.Errorf("Stderr(err) = %q, want empty for a non-ExitError", got)
	}
}

func TestStderrEmptyWhenCommandRunViaOutputWithoutStderrCapture(t *testing.T) {
	// cmd.Run() never populates ExitError.Stderr — only cmd.Output()
	// does. Confirms Stderr degrades to "" rather than panicking for a
	// caller that used Run()/CombinedOutput() instead, rather than
	// assuming every *exec.ExitError carries it.
	cmd := exec.Command("sh", "-c", "exit 1")
	err := cmd.Run()
	if err == nil {
		t.Fatalf("expected the command to fail")
	}
	if got := Stderr(err); got != "" {
		t.Errorf("Stderr(err) = %q, want empty when stderr was never captured", got)
	}
}
