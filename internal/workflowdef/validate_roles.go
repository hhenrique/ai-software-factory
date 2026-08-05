package workflowdef

import "fmt"

// validateRoles is rule 2: every type: agent step's role must exist in
// roles:.
func validateRoles(def *Definition) ValidationErrors {
	var errs ValidationErrors
	for _, s := range def.Steps {
		if s.Type != StepTypeAgent || s.Role == "" {
			continue // empty role already reported by rule 0
		}
		if _, ok := def.Roles[s.Role]; !ok {
			errs = append(errs, &ValidationError{
				Rule: RuleAgentRoleExists, StepID: s.ID,
				Message: fmt.Sprintf("role %q is not declared in roles:", s.Role),
			})
		}
	}
	return errs
}
