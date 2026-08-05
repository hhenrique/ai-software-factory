package workflowdef

import "fmt"

// validateOutcomes is rule 4, flagged partial by design: doc 02 requires
// every declared outcome to have an on: mapping, but the schema only makes
// outcomes mechanically enumerable where output_schema declares a verdict
// enum (e.g. plan's `verdict: [proceed, reject, escalate]`). Tool steps and
// schema-less/non-enum agent steps have no statically enumerable outcome
// set — that's a real gap in the schema as written, not cut for
// convenience. It's mitigated by a runtime safety net: the conductor's
// router hard-errors on any outcome it sees with no on: mapping.
func validateOutcomes(def *Definition) ValidationErrors {
	var errs ValidationErrors
	for _, s := range def.Steps {
		verdictRaw, ok := s.OutputSchema["verdict"]
		if !ok {
			continue
		}
		enumVals, ok := asStringSlice(verdictRaw)
		if !ok {
			continue // verdict present but not a statically enumerable list
		}
		for _, v := range enumVals {
			if _, mapped := s.On[v]; !mapped {
				errs = append(errs, &ValidationError{
					Rule: RuleOutcomesMapped, StepID: s.ID,
					Message: fmt.Sprintf("verdict outcome %q has no on: mapping", v),
				})
			}
		}
	}
	return errs
}

func asStringSlice(v any) ([]string, bool) {
	items, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		s, ok := it.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}
