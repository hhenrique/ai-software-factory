package workflowdef

import "fmt"

// validateStructure is the rule-0 prerequisite the other rules build on:
// unique non-empty step ids, well-formed types, required per-type fields,
// budget references that resolve, and next/on targets that resolve to a
// real step id or a terminal state.
func validateStructure(def *Definition) ValidationErrors {
	var errs ValidationErrors
	seen := map[string]bool{}

	for i, s := range def.Steps {
		if s.ID == "" {
			errs = append(errs, &ValidationError{
				Rule:    RuleStructural,
				Message: fmt.Sprintf("step at index %d has an empty id", i),
			})
			continue
		}
		if seen[s.ID] {
			errs = append(errs, &ValidationError{
				Rule:    RuleStructural,
				StepID:  s.ID,
				Message: "duplicate step id",
			})
		}
		seen[s.ID] = true

		switch s.Type {
		case StepTypeTool:
			if s.Action == "" {
				errs = append(errs, &ValidationError{
					Rule: RuleStructural, StepID: s.ID,
					Message: "type: tool step must declare action",
				})
			}
		case StepTypeAgent:
			if s.Role == "" {
				errs = append(errs, &ValidationError{
					Rule: RuleStructural, StepID: s.ID,
					Message: "type: agent step must declare role",
				})
			}
		default:
			errs = append(errs, &ValidationError{
				Rule: RuleStructural, StepID: s.ID,
				Message: fmt.Sprintf("unknown step type %q (want tool|agent)", s.Type),
			})
		}

		if s.Budget != "" {
			if _, ok := def.Budgets[s.Budget]; !ok {
				errs = append(errs, &ValidationError{
					Rule: RuleStructural, StepID: s.ID,
					Message: fmt.Sprintf("budget %q is not declared in budgets:", s.Budget),
				})
			}
		}
	}

	checkTarget := func(stepID, label, dest string) {
		if dest == "" || IsTerminalState(dest) {
			return
		}
		if !seen[dest] {
			errs = append(errs, &ValidationError{
				Rule: RuleStructural, StepID: stepID,
				Message: fmt.Sprintf("%s target %q does not resolve to a step id or terminal state", label, dest),
			})
		}
	}

	for _, s := range def.Steps {
		if s.ID == "" {
			continue
		}
		checkTarget(s.ID, "next", s.Next)
		for outcome, t := range s.On {
			checkTarget(s.ID, fmt.Sprintf("on[%s]", outcome), t.Destination())
		}
		checkTarget(s.ID, "on_malformed_output", s.OnMalformedOutput)
		checkTarget(s.ID, "approve_resume", s.ApproveResume)
	}

	return errs
}
