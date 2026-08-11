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

// reviewPendingState is the one terminal state buildEdges deliberately
// drops as a sink (correct for validateCycles: a human-mediated pause is
// exactly what makes an otherwise-unbounded loop legal without a
// budget:, see docs/01's "Mandatory plan approval") that this function
// has to model anyway. Doc01: a human resume names *any* step id, not
// one fixed statically by the graph — so once a Workflow routes through
// REVIEW_PENDING for its normal, non-exceptional path (the plan-approval
// gate does, unconditionally), everything downstream of it would
// otherwise look permanently unproducible to this analysis, even though
// runContext genuinely does carry everything forward at runtime (it's
// one flat accumulated map, never reconstructed per edge).
const reviewPendingState = "REVIEW_PENDING"

func computeAvailability(index map[string]*Step, preds map[string][]string, cfg *validationConfig) map[string]map[string]bool {
	// reviewPendingSources: real steps that route to REVIEW_PENDING.
	// Modeled as a permissive pass-through node — available at every one
	// of its sources flows into it, and it in turn flows into every real
	// step, since any of them is a legal resume target. This
	// over-approximates which fields a specific resume target actually
	// has (the same kind of coarsening this file's package doc already
	// accepts for per-branch availability generally), trading precision
	// for soundness: it never flags a legitimately fine Workflow, at the
	// cost of not catching a resume into a step whose fields genuinely
	// were never produced on any path.
	var reviewPendingSources []string
	for _, id := range sortedStepIDs(index) {
		s := index[id]
		isSource := s.Next == reviewPendingState
		if !isSource {
			for _, t := range s.On {
				if t.Destination() == reviewPendingState {
					isSource = true
					break
				}
			}
		}
		if isSource {
			reviewPendingSources = append(reviewPendingSources, id)
		}
	}
	hasReviewPending := len(reviewPendingSources) > 0

	available := make(map[string]map[string]bool, len(index)+1)
	for id := range index {
		set := make(map[string]bool, len(cfg.alwaysAvailableFields))
		for _, f := range cfg.alwaysAvailableFields {
			set[f] = true
		}
		available[id] = set
	}
	if hasReviewPending {
		available[reviewPendingState] = map[string]bool{}
	}

	produced := make(map[string][]string, len(index))
	for id, s := range index {
		produced[id] = producedFields(s, cfg)
	}

	nodePreds := make(map[string][]string, len(index)+1)
	for id := range index {
		ps := preds[id]
		if hasReviewPending {
			ps = append(append([]string{}, ps...), reviewPendingState)
		}
		nodePreds[id] = ps
	}
	if hasReviewPending {
		nodePreds[reviewPendingState] = reviewPendingSources
	}

	nodes := sortedStepIDs(index)
	if hasReviewPending {
		nodes = append(nodes, reviewPendingState)
	}

	changed := true
	for changed {
		changed = false
		for _, id := range nodes {
			for _, p := range nodePreds[id] {
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
