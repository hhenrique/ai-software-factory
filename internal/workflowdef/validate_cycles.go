package workflowdef

import "fmt"

// validateCycles is rule 1: the graph must be acyclic except where a cycle
// has a budget attached to at least one member step. This needs actual
// cycle *membership* (Tarjan's SCC), not just "does a cycle exist" — the
// reference issue-to-pr-standard definition has multiple loops sharing
// nodes ({verify, revise_verify, review, coder_response, revise_review})
// that Tarjan correctly merges into one strongly connected component.
func validateCycles(def *Definition) ValidationErrors {
	var errs ValidationErrors

	index := buildStepIndex(def)
	edges := buildEdges(index)
	sccs := tarjanSCC(index, edges)

	for _, scc := range sccs {
		isCycle := len(scc) > 1
		if len(scc) == 1 {
			id := scc[0]
			for _, to := range edges[id] {
				if to == id {
					isCycle = true
					break
				}
			}
		}
		if !isCycle {
			continue
		}

		budgeted := false
		for _, id := range scc {
			if index[id].Budget != "" {
				budgeted = true
				break
			}
		}
		if budgeted {
			continue
		}

		errs = append(errs, &ValidationError{
			Rule:   RuleAcyclicOrBudgeted,
			StepID: scc[0],
			Message: fmt.Sprintf(
				"unbounded cycle with no budgeted step among members: %v", scc),
		})
	}

	return errs
}

// tarjanSCC computes strongly connected components of the step graph.
// Iteration order over index is sorted for deterministic error output;
// SCC discovery order and in-SCC member order otherwise follow Tarjan's
// algorithm and aren't independently meaningful.
func tarjanSCC(index map[string]*Step, edges map[string][]string) [][]string {
	type nodeState struct {
		idx, low int
		onStack  bool
	}

	states := make(map[string]*nodeState, len(index))
	var stack []string
	counter := 0
	var sccs [][]string

	var strongconnect func(v string)
	strongconnect = func(v string) {
		st := &nodeState{idx: counter, low: counter, onStack: true}
		states[v] = st
		counter++
		stack = append(stack, v)

		for _, w := range edges[v] {
			ws, visited := states[w]
			if !visited {
				strongconnect(w)
				ws = states[w]
				if ws.low < st.low {
					st.low = ws.low
				}
			} else if ws.onStack {
				if ws.idx < st.low {
					st.low = ws.idx
				}
			}
		}

		if st.low == st.idx {
			var scc []string
			for {
				n := len(stack) - 1
				w := stack[n]
				stack = stack[:n]
				states[w].onStack = false
				scc = append(scc, w)
				if w == v {
					break
				}
			}
			sccs = append(sccs, scc)
		}
	}

	for _, id := range sortedStepIDs(index) {
		if _, visited := states[id]; !visited {
			strongconnect(id)
		}
	}

	return sccs
}
