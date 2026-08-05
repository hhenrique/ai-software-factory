package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

// claudeAdapter invokes the Claude Code CLI (`claude`) non-interactively.
type claudeAdapter struct{}

// claudeJSONResult is the shape of `claude --output-format json`'s single
// JSON result object.
type claudeJSONResult struct {
	Result     string `json:"result"`
	IsError    bool   `json:"is_error"`
	NumTurns   int    `json:"num_turns"`
	DurationMs int    `json:"duration_ms"`
	Usage      struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

func (claudeAdapter) invoke(ctx context.Context, inv invocation) (invocationResult, error) {
	args := []string{
		"-p", inv.Prompt,
		"--output-format", "json",
		// Activities run headless, with no interactive terminal to approve
		// tool use from — bypassPermissions is what makes non-interactive
		// file edits/commands possible at all.
		"--permission-mode", "bypassPermissions",
	}
	if inv.Model != "" {
		args = append(args, "--model", inv.Model)
	}
	if inv.Effort != "" {
		args = append(args, "--effort", inv.Effort)
	}

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = inv.WorktreePath
	out, err := cmd.Output()
	if err != nil {
		return invocationResult{}, fmt.Errorf("claude: %w: %s", err, stderrOf(err))
	}

	var res claudeJSONResult
	if err := json.Unmarshal(out, &res); err != nil {
		return invocationResult{}, fmt.Errorf("claude: parse --output-format json: %w", err)
	}
	if res.IsError {
		return invocationResult{}, fmt.Errorf("claude: reported an error result: %s", res.Result)
	}

	return invocationResult{
		FinalText: res.Result,
		// input+output alone dramatically undercounts real usage for a
		// cache-heavy call — a live check showed input_tokens=10,
		// output_tokens=44, but cache_creation_input_tokens=7101,
		// cache_read_input_tokens=11609 for the same call. All four are
		// billed (at different rates), so summing all four is the honest
		// normalized total for budget-enforcement purposes (doc 03: "do
		// not let a harness that under-reports usage silently exceed
		// budgets other harnesses are held to").
		TokensUsed: res.Usage.InputTokens + res.Usage.OutputTokens +
			res.Usage.CacheCreationInputTokens + res.Usage.CacheReadInputTokens,
	}, nil
}

func stderrOf(err error) string {
	if exitErr, ok := err.(*exec.ExitError); ok {
		return string(exitErr.Stderr)
	}
	return ""
}
