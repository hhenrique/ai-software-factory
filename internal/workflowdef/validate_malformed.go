package workflowdef

// validateMalformedOutput is rule 3: every type: agent step that declares
// an output_schema must declare on_malformed_output — malformed-output
// handling must never default silently.
func validateMalformedOutput(def *Definition) ValidationErrors {
	var errs ValidationErrors
	for _, s := range def.Steps {
		if s.Type != StepTypeAgent || len(s.OutputSchema) == 0 {
			continue
		}
		if s.OnMalformedOutput == "" {
			errs = append(errs, &ValidationError{
				Rule: RuleMalformedOutputHandled, StepID: s.ID,
				Message: "agent step declares output_schema but no on_malformed_output",
			})
		}
	}
	return errs
}
