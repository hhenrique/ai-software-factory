package workflowdef

// validationConfig holds the override tables rule 5 needs. Constructed
// fresh per Validate call so options from one call never leak into another.
type validationConfig struct {
	toolActionProducedFields map[string][]string
	alwaysAvailableFields    []string
}

func newValidationConfig() *validationConfig {
	tool := make(map[string][]string, len(defaultToolActionProducedFields))
	for k, v := range defaultToolActionProducedFields {
		tool[k] = append([]string(nil), v...)
	}
	return &validationConfig{
		toolActionProducedFields: tool,
		alwaysAvailableFields:    append([]string(nil), defaultAlwaysAvailableFields...),
	}
}

// Option customizes a Validate call's rule-5 lookup tables.
type Option func(*validationConfig)

// WithToolActionProducedFields adds/overrides entries in the tool-action ->
// produced-context-fields table used by rule 5, for actions this package
// doesn't already know about.
func WithToolActionProducedFields(overrides map[string][]string) Option {
	return func(c *validationConfig) {
		for k, v := range overrides {
			c.toolActionProducedFields[k] = v
		}
	}
}

// WithAlwaysAvailableFields replaces the set of fields rule 5 treats as
// always available (conductor-computed, not tied to one step's output).
func WithAlwaysAvailableFields(fields []string) Option {
	return func(c *validationConfig) {
		c.alwaysAvailableFields = fields
	}
}

// Validate runs every static rule doc 02 requires the conductor enforce
// before a Workflow Definition can be saved/activated, plus the rule-0
// structural prerequisite. It collects every violation rather than failing
// fast — useful for a human iterating on YAML — and keeps running
// best-effort even when earlier rules found problems, so e.g. a dangling
// step id doesn't suppress every other finding.
func Validate(def *Definition, opts ...Option) ValidationErrors {
	cfg := newValidationConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	var errs ValidationErrors
	errs = append(errs, validateStructure(def)...)
	errs = append(errs, validateCycles(def)...)
	errs = append(errs, validateRoles(def)...)
	errs = append(errs, validateMalformedOutput(def)...)
	errs = append(errs, validateOutcomes(def)...)
	errs = append(errs, validateContext(def, cfg)...)
	return errs
}
