// Package workflowdef holds the schema types, YAML parsing, and static
// validator for a Workflow Definition (docs/02-workflow-definition-schema.md).
package workflowdef

// StepType distinguishes a deterministic tool step from an LLM-backed agent
// step — this is Rule 2's "tool vs. agent" ownership, visible per step.
type StepType string

const (
	StepTypeTool  StepType = "tool"
	StepTypeAgent StepType = "agent"
)

// TerminalStates are the Run states (docs/01-run-state-machine.md) legal as
// next/on targets in place of a step id.
var TerminalStates = map[string]bool{
	"FAILED":         true,
	"COMPLETED":      true,
	"REVIEW_PENDING": true,
	"CANCELLED":      true,
}

// IsTerminalState reports whether s names a Run terminal state rather than
// a step id.
func IsTerminalState(s string) bool {
	return TerminalStates[s]
}

// Definition is the parsed form of a Workflow Definition YAML document.
type Definition struct {
	Workflow string            `yaml:"workflow"`
	Version  int               `yaml:"version"`
	Roles    map[string]Role   `yaml:"roles"`
	Budgets  map[string]Budget `yaml:"budgets"`
	Trigger  *Trigger          `yaml:"trigger,omitempty"`
	Steps    []Step            `yaml:"steps"`
}

// Role is a (harness, model) pair — see docs/03-roles-and-harness-contract.md.
type Role struct {
	Harness string `yaml:"harness"`
	Model   string `yaml:"model"`

	// Params carries harness-invocation parameters that aren't the model
	// itself — e.g. reasoning effort. Keys are canonical (adapter-agnostic)
	// names the harness adapter translates into its own CLI convention
	// (doc 03: harness-specific invocation shape must live in the
	// adapter, never leak into the Workflow Definition or the conductor).
	// "effort" is the only key defined so far — see
	// internal/activities/harness's per-adapter files for how each one
	// maps it.
	Params map[string]string `yaml:"params,omitempty"`
}

// Budget bounds a loop in the graph. Zero value for a field means
// "unbounded on that dimension" — at least one of the three should be set
// for a budget attached to a cycle to be meaningful, but that's a runtime/
// authoring concern, not something Validate enforces field-by-field.
type Budget struct {
	MaxAttempts int `yaml:"max_attempts,omitempty"`
	MaxRounds   int `yaml:"max_rounds,omitempty"`
	MaxTokens   int `yaml:"max_tokens,omitempty"`
}

// Trigger makes a Workflow Definition an Automation — not a separate concept
// per doc 00, just an optional field.
type Trigger struct {
	OnEvent  string `yaml:"on_event,omitempty"`
	Schedule string `yaml:"schedule,omitempty"`
}

// Step is one node in the Workflow Definition DAG.
type Step struct {
	ID                string            `yaml:"id"`
	Type              StepType          `yaml:"type"`
	Role              string            `yaml:"role,omitempty"`
	Action            string            `yaml:"action,omitempty"`
	Context           []string          `yaml:"context,omitempty"`
	OutputSchema      map[string]any    `yaml:"output_schema,omitempty"`
	Budget            string            `yaml:"budget,omitempty"`
	Next              string            `yaml:"next,omitempty"`
	On                map[string]Target `yaml:"on,omitempty"`
	OnMalformedOutput string            `yaml:"on_malformed_output,omitempty"`
}
