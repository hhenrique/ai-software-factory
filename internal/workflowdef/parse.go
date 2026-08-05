package workflowdef

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Parse decodes a Workflow Definition YAML document. It only catches
// syntax errors and unknown fields (KnownFields(true) — a typo'd field
// fails loudly instead of being silently dropped); semantic rules live in
// Validate, called separately.
func Parse(data []byte) (*Definition, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var def Definition
	if err := dec.Decode(&def); err != nil {
		return nil, fmt.Errorf("workflowdef: parse: %w", err)
	}
	return &def, nil
}
