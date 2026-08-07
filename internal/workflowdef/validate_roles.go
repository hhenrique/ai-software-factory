package workflowdef

import "fmt"

// validateRoles is rule 2: every type: agent step's role must be declared
// in roles:, and every name declared in roles: must itself be one of
// KnownRoles — the same conceptual failure (an unreal role token), just
// caught at declaration time too rather than only when a step references
// it.
func validateRoles(def *Definition) ValidationErrors {
	var errs ValidationErrors

	declared := make(map[string]bool, len(def.Roles))
	for _, name := range def.Roles {
		declared[name] = true
		if !IsKnownRole(name) {
			errs = append(errs, &ValidationError{
				Rule:    RuleAgentRoleExists,
				Message: fmt.Sprintf("roles: declares %q, which is not one of the known roles (%v)", name, KnownRoles),
			})
		}
	}

	for _, s := range def.Steps {
		if s.Type != StepTypeAgent || s.Role == "" {
			continue // empty role already reported by rule 0
		}
		if !declared[s.Role] {
			errs = append(errs, &ValidationError{
				Rule: RuleAgentRoleExists, StepID: s.ID,
				Message: fmt.Sprintf("role %q is not declared in roles:", s.Role),
			})
		}
	}
	return errs
}
