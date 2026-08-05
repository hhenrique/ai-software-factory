package workflowdef

import "fmt"

// validateContext is rule 5, flagged with known simplifications: every
// context field an agent step declares must be producible by the graph at
// that point — either an earlier step's output or a Run-level/allowlisted
// field.
//
// Computed as a forward fixpoint over the step graph:
//
//	available(s) = allowlist ∪ ⋃_{p a predecessor of s} (available(p) ∪ producedFields(p))
//
// This is a monotone union over a finite field set, so it converges via a
// straightforward worklist loop — standard reaching-definitions-style
// dataflow, needed (rather than just "direct predecessor's fields") because
// a field can be produced several hops upstream: revise_verify's context
// includes scope_contract, produced back at plan, not by its direct
// predecessor verify.
//
// Known approximation, documented rather than hidden: this does not model
// per-outcome-branch availability (e.g. a field only produced on one `on:`
// branch out of a step). It's sound for both reference definitions in doc
// 02 — every field consumed downstream is produced on every path that
// reaches the consumer — but is coarser than true per-branch dataflow.
func validateContext(def *Definition, cfg *validationConfig) ValidationErrors {
	var errs ValidationErrors

	index := buildStepIndex(def)
	edges := buildEdges(index)
	preds := buildPredecessors(index, edges)

	available := computeAvailability(index, preds, cfg)

	for _, id := range sortedStepIDs(index) {
		s := index[id]
		for _, field := range s.Context {
			if !available[id][field] {
				errs = append(errs, &ValidationError{
					Rule: RuleContextProducible, StepID: id,
					Message: fmt.Sprintf("context field %q is not producible by any predecessor step", field),
				})
			}
		}
	}

	return errs
}

func computeAvailability(index map[string]*Step, preds map[string][]string, cfg *validationConfig) map[string]map[string]bool {
	available := make(map[string]map[string]bool, len(index))
	for id := range index {
		set := make(map[string]bool, len(cfg.alwaysAvailableFields))
		for _, f := range cfg.alwaysAvailableFields {
			set[f] = true
		}
		available[id] = set
	}

	produced := make(map[string][]string, len(index))
	for id, s := range index {
		produced[id] = producedFields(s, cfg)
	}

	changed := true
	for changed {
		changed = false
		for _, id := range sortedStepIDs(index) {
			for _, p := range preds[id] {
				for f := range available[p] {
					if !available[id][f] {
						available[id][f] = true
						changed = true
					}
				}
				for _, f := range produced[p] {
					if !available[id][f] {
						available[id][f] = true
						changed = true
					}
				}
			}
		}
	}

	return available
}
