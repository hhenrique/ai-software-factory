package workflowdef

import "sort"

// buildStepIndex maps step id -> *Step, skipping empty-id and duplicate-id
// entries (rule 0 already reports those; the graph rules just need a clean
// index to keep working best-effort).
func buildStepIndex(def *Definition) map[string]*Step {
	index := make(map[string]*Step, len(def.Steps))
	for i := range def.Steps {
		s := &def.Steps[i]
		if s.ID == "" {
			continue
		}
		if _, dup := index[s.ID]; dup {
			continue
		}
		index[s.ID] = s
	}
	return index
}

// buildEdges returns the step-to-step routing graph (next: and on: targets),
// dropping edges to terminal states (sinks, not graph nodes) and dangling
// targets (rule 0 already reports those).
func buildEdges(index map[string]*Step) map[string][]string {
	edges := make(map[string][]string)
	add := func(from, to string) {
		if to == "" || IsTerminalState(to) {
			return
		}
		if _, ok := index[to]; !ok {
			return
		}
		edges[from] = append(edges[from], to)
	}
	for id, s := range index {
		add(id, s.Next)
		for _, t := range s.On {
			add(id, t.Destination())
		}
	}
	return edges
}

// buildPredecessors inverts edges.
func buildPredecessors(index map[string]*Step, edges map[string][]string) map[string][]string {
	preds := make(map[string][]string, len(index))
	for from, tos := range edges {
		for _, to := range tos {
			preds[to] = append(preds[to], from)
		}
	}
	return preds
}

// sortedKeys returns the keys of index in sorted order, for deterministic
// iteration when producing error output.
func sortedStepIDs(index map[string]*Step) []string {
	ids := make([]string, 0, len(index))
	for id := range index {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
