// Graph-building for the workflow_v1/v2/v3 visualization prototypes:
// turning a parsed Workflow Definition into a renderable node/edge graph,
// plus cluster detection (which nodes form a real back/forth loop) so
// every layout algorithm can use the same real data instead of each
// prototype re-deriving it in JS three different ways.
package main

import (
	"fmt"
	"net/http"
	"os"
	"sort"

	"factory/internal/workflowdef"
)

// WorkflowGraph is the full step graph behind a Workflow Definition —
// what the workflow_v1/v2/v3 visualization prototypes render. Separate
// from WorkflowInfo (which is a summary for the Workflows list): a graph
// view needs every step and every edge, not just counts.
type WorkflowGraph struct {
	Workflow string      `json:"workflow"`
	Path     string      `json:"path"`
	Nodes    []GraphNode `json:"nodes"`
	Edges    []GraphEdge `json:"edges"`
	// Clusters lists every real back/forth loop (a strongly connected
	// component of size > 1) found in the graph — e.g. {verify,
	// revise_verify} or the larger {review, coder_response,
	// revise_review, verify} loop issue-to-pr-claude-only.yaml has via
	// coder_response's "address" path back through revise_review to
	// verify. See computeClusters.
	Clusters []GraphCluster `json:"clusters,omitempty"`
}

// GraphNode is one step, or one of the terminal states (COMPLETED/FAILED/
// CANCELLED/REVIEW_PENDING) an edge points at — terminals are real nodes
// here (Kind: "terminal") since a graph with dangling edges pointing at
// nothing isn't a graph a human can read.
type GraphNode struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"` // "tool" | "agent" | "terminal"
	Role   string `json:"role,omitempty"`
	Action string `json:"action,omitempty"`
	Budget string `json:"budget,omitempty"`
	// Cluster is the id of this node's GraphCluster entry, "" if it isn't
	// part of a real cycle (a terminal state never is: nothing routes out
	// of one, so it can't be mutually reachable with anything).
	Cluster string `json:"cluster,omitempty"`
}

// GraphEdge is one routing possibility out of a step: an unconditional
// `next:`, one entry of an `on:` map (Label is the outcome, e.g. "pass"/
// "fail"/"budget_exhausted" — the last already appears as an ordinary
// `on:` key in every reference Workflow Definition, no special-casing
// needed), or `on_malformed_output` (Label: "malformed_output").
type GraphEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label,omitempty"`
}

// GraphCluster is one strongly connected component of size > 1 — a real
// back/forth loop a human would want a layout to visually group rather
// than stretch across ranks. Id is stable (a "cluster:" prefix plus the
// lexicographically-first member id) so re-requesting the same
// Workflow's graph doesn't renumber clusters between calls — the prefix
// is load-bearing, not decorative: a graph library that treats every
// node/container id as one shared namespace (every one of them does)
// would otherwise collide a cluster's id with its own first member's id,
// since "the first member" is by definition a real step id too.
type GraphCluster struct {
	ID      string   `json:"id"`
	NodeIDs []string `json:"node_ids"`
}

// buildWorkflowGraph is a pure function of an already-parsed Definition —
// deliberately not requiring workflowdef.Validate to have passed: seeing
// the actual graph structure, including a broken one (e.g. an unbounded
// cycle), is exactly when a human most wants to look at it.
func buildWorkflowGraph(def *workflowdef.Definition, path string) WorkflowGraph {
	g := WorkflowGraph{Workflow: def.Workflow, Path: path}

	terminals := map[string]bool{}
	addTerminalNode := func(id string) {
		if workflowdef.IsTerminalState(id) && !terminals[id] {
			terminals[id] = true
			g.Nodes = append(g.Nodes, GraphNode{ID: id, Kind: "terminal"})
		}
	}

	for _, step := range def.Steps {
		g.Nodes = append(g.Nodes, GraphNode{
			ID: step.ID, Kind: string(step.Type), Role: step.Role, Action: step.Action, Budget: step.Budget,
		})
	}

	for _, step := range def.Steps {
		if step.Next != "" {
			addTerminalNode(step.Next)
			g.Edges = append(g.Edges, GraphEdge{From: step.ID, To: step.Next})
		}
		for outcome, target := range step.On {
			dest := target.Destination()
			addTerminalNode(dest)
			g.Edges = append(g.Edges, GraphEdge{From: step.ID, To: dest, Label: outcome})
		}
		if step.OnMalformedOutput != "" {
			addTerminalNode(step.OnMalformedOutput)
			g.Edges = append(g.Edges, GraphEdge{From: step.ID, To: step.OnMalformedOutput, Label: "malformed_output"})
		}
	}

	// step.On is a map — Go's iteration order is random. Sort for stable,
	// diffable JSON output rather than a different edge order per request.
	sort.Slice(g.Edges, func(i, j int) bool {
		if g.Edges[i].From != g.Edges[j].From {
			return g.Edges[i].From < g.Edges[j].From
		}
		return g.Edges[i].Label < g.Edges[j].Label
	})

	g.Clusters = computeClusters(g.Nodes, g.Edges)
	byID := make(map[string]*GraphNode, len(g.Nodes))
	for i := range g.Nodes {
		byID[g.Nodes[i].ID] = &g.Nodes[i]
	}
	for _, c := range g.Clusters {
		for _, id := range c.NodeIDs {
			byID[id].Cluster = c.ID
		}
	}

	return g
}

// computeClusters finds every strongly connected component of size > 1 —
// Tarjan's algorithm, same approach and same shape as
// internal/workflowdef's validateCycles (which needs SCC membership for
// rule 1, not just cycle existence — a Workflow Definition can have
// overlapping loops that merge into one component, e.g. this graph's
// verify/revise_verify loop and its review/coder_response/revise_review
// loop share the verify node and are one SCC together, not two). This is
// a separate implementation rather than a shared one: workflowdef's
// operates on *workflowdef.Step and doesn't know about terminal-state
// pseudo-nodes, which this graph has and workflowdef's step graph
// doesn't.
func computeClusters(nodes []GraphNode, edges []GraphEdge) []GraphCluster {
	adj := make(map[string][]string, len(nodes))
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
		adj[n.ID] = nil
	}
	for _, e := range edges {
		adj[e.From] = append(adj[e.From], e.To)
	}
	sort.Strings(ids)

	type nodeState struct {
		idx, low int
		onStack  bool
	}
	states := make(map[string]*nodeState, len(nodes))
	var stack []string
	counter := 0
	var sccs [][]string

	var strongconnect func(v string)
	strongconnect = func(v string) {
		st := &nodeState{idx: counter, low: counter, onStack: true}
		states[v] = st
		counter++
		stack = append(stack, v)

		for _, w := range adj[v] {
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

	for _, id := range ids {
		if _, visited := states[id]; !visited {
			strongconnect(id)
		}
	}

	var clusters []GraphCluster
	for _, scc := range sccs {
		if len(scc) < 2 {
			continue
		}
		sort.Strings(scc)
		clusters = append(clusters, GraphCluster{ID: "cluster:" + scc[0], NodeIDs: scc})
	}
	sort.Slice(clusters, func(i, j int) bool { return clusters[i].ID < clusters[j].ID })
	return clusters
}

// workflowGraphHandler serves one Workflow Definition's full graph, keyed
// by ?path=. path must be one of the files listWorkflowFiles finds under
// dir — checked by membership rather than trusted directly, since unlike
// the directory scan itself (never client-influenced), this handler's
// input is a query parameter and a bare os.ReadFile(path) on it would be
// an arbitrary local file read.
func workflowGraphHandler(dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Query().Get("path")
		if path == "" {
			http.Error(w, "path is required", http.StatusBadRequest)
			return
		}
		files, err := listWorkflowFiles(dir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		known := false
		for _, f := range files {
			if f == path {
				known = true
				break
			}
		}
		if !known {
			http.Error(w, fmt.Sprintf("unknown workflow path %q", path), http.StatusNotFound)
			return
		}

		data, err := os.ReadFile(path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		def, err := workflowdef.Parse(data)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, buildWorkflowGraph(def, path))
	}
}
