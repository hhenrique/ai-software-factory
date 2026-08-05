package workflowdef

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Target is the value side of a step's `on:` map. Doc 02 allows two shapes:
//
//	on:
//	  proceed: execute                                       # plain scalar
//	  out_of_scope: { action: task.create(...), next: review } # compound
//
// The compound shape carries a side-effecting action alongside the routing
// target (e.g. spawning a backlog Task before continuing the loop).
type Target struct {
	// StepOrState holds the destination when the YAML value was a plain
	// scalar (a step id or a terminal state name).
	StepOrState string
	// Action holds the side-effect identifier when the YAML value was a
	// compound map. Empty for the plain-scalar shape.
	Action string
	// Next holds the destination when the YAML value was a compound map.
	Next string
}

// Destination returns the routing target regardless of which shape the
// YAML used.
func (t Target) Destination() string {
	if t.Action != "" {
		return t.Next
	}
	return t.StepOrState
}

// HasSideEffect reports whether this target's compound form declared an
// action to run before routing.
func (t Target) HasSideEffect() bool {
	return t.Action != ""
}

func (t *Target) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		return value.Decode(&t.StepOrState)
	case yaml.MappingNode:
		var compound struct {
			Action string `yaml:"action"`
			Next   string `yaml:"next"`
		}
		if err := value.Decode(&compound); err != nil {
			return err
		}
		t.Action = compound.Action
		t.Next = compound.Next
		return nil
	default:
		return fmt.Errorf("workflowdef: target: unsupported YAML node kind %v", value.Kind)
	}
}
