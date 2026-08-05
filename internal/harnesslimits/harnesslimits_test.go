package harnesslimits

import "testing"

func TestKeyFormat(t *testing.T) {
	got := Key("claude-code", "sonnet", "high")
	want := "claude-code/sonnet/high"
	if got != want {
		t.Errorf("Key() = %q, want %q", got, want)
	}
}

func TestParseEnvUnsetReturnsEmptyMapNotError(t *testing.T) {
	t.Setenv(EnvVar, "")
	limits, err := ParseEnv()
	if err != nil {
		t.Fatalf("ParseEnv: %v", err)
	}
	if len(limits) != 0 {
		t.Errorf("limits = %v, want empty", limits)
	}
}

func TestParseEnvValid(t *testing.T) {
	t.Setenv(EnvVar, `{"claude-code/sonnet/high": 500, "claude-code/sonnet/low": 2000}`)
	limits, err := ParseEnv()
	if err != nil {
		t.Fatalf("ParseEnv: %v", err)
	}
	if limits[Key("claude-code", "sonnet", "high")] != 500 {
		t.Errorf("high limit = %d, want 500", limits[Key("claude-code", "sonnet", "high")])
	}
	if limits[Key("claude-code", "sonnet", "low")] != 2000 {
		t.Errorf("low limit = %d, want 2000", limits[Key("claude-code", "sonnet", "low")])
	}
}

func TestParseEnvInvalidJSONErrors(t *testing.T) {
	t.Setenv(EnvVar, "not json")
	_, err := ParseEnv()
	if err == nil {
		t.Fatalf("expected an error for invalid JSON")
	}
}
