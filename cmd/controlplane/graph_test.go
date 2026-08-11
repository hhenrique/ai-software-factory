package main

import (
	"testing"

	"factory/internal/workflowdef"
)

func TestComputeClustersFindsASimpleTwoNodeLoop(t *testing.T) {
	nodes := []GraphNode{{ID: "verify"}, {ID: "revise_verify"}, {ID: "COMPLETED"}}
	edges := []GraphEdge{
		{From: "verify", To: "revise_verify", Label: "fail"},
		{From: "revise_verify", To: "verify"},
		{From: "verify", To: "COMPLETED", Label: "pass"},
	}

	clusters := computeClusters(nodes, edges)
	if len(clusters) != 1 {
		t.Fatalf("len(clusters) = %d, want 1: %+v", len(clusters), clusters)
	}
	if len(clusters[0].NodeIDs) != 2 {
		t.Fatalf("cluster members = %v, want 2", clusters[0].NodeIDs)
	}
}

func TestComputeClustersMergesOverlappingLoopsIntoOneComponent(t *testing.T) {
	// Mirrors issue-to-pr.yaml's shape: verify<->revise_verify
	// is one loop, and coder_response's "address" path threads back through
	// revise_review to verify — sharing the verify node with review's own
	// loop back through coder_response. Tarjan must merge these into a
	// single SCC, not report two separate ones.
	nodes := []GraphNode{
		{ID: "verify"}, {ID: "review"}, {ID: "coder_response"}, {ID: "revise_review"},
	}
	edges := []GraphEdge{
		{From: "verify", To: "review", Label: "pass"},
		{From: "review", To: "coder_response", Label: "changes_required"},
		{From: "coder_response", To: "revise_review", Label: "address"},
		{From: "revise_review", To: "verify"},
		{From: "coder_response", To: "review", Label: "dispute"},
	}

	clusters := computeClusters(nodes, edges)
	if len(clusters) != 1 {
		t.Fatalf("len(clusters) = %d, want 1 (overlapping loops must merge): %+v", len(clusters), clusters)
	}
	if len(clusters[0].NodeIDs) != 4 {
		t.Errorf("cluster members = %v, want all 4 nodes", clusters[0].NodeIDs)
	}
}

func TestComputeClustersNoLoopsMeansNoClusters(t *testing.T) {
	nodes := []GraphNode{{ID: "a"}, {ID: "b"}, {ID: "COMPLETED"}}
	edges := []GraphEdge{
		{From: "a", To: "b"},
		{From: "b", To: "COMPLETED"},
	}
	clusters := computeClusters(nodes, edges)
	if len(clusters) != 0 {
		t.Errorf("clusters = %+v, want none for a purely linear graph", clusters)
	}
}

func TestComputeClustersIDIsStableLexicographicallyFirstMemberWithClusterPrefix(t *testing.T) {
	nodes := []GraphNode{{ID: "zeta"}, {ID: "alpha"}}
	edges := []GraphEdge{
		{From: "zeta", To: "alpha"},
		{From: "alpha", To: "zeta"},
	}
	clusters := computeClusters(nodes, edges)
	if len(clusters) != 1 || clusters[0].ID != "cluster:alpha" {
		t.Fatalf("clusters = %+v, want one cluster with ID \"cluster:alpha\"", clusters)
	}
}

// TestComputeClustersIDNeverCollidesWithAMemberNodeID guards the actual
// bug this prefix fixed: without it, a cluster's id (its
// lexicographically-first member) is literally identical to that
// member's own node id — every graph library downstream (ELK, Cytoscape,
// dagre) treats node/container ids as one shared namespace, so a
// cluster container and its own child ended up as two elements fighting
// over the same id, corrupting the rendered graph (workflow_v2's ELK
// layout visibly duplicated a node when this shipped uncaught).
func TestComputeClustersIDNeverCollidesWithAMemberNodeID(t *testing.T) {
	nodes := []GraphNode{{ID: "coder_response"}, {ID: "review"}}
	edges := []GraphEdge{
		{From: "coder_response", To: "review"},
		{From: "review", To: "coder_response"},
	}
	clusters := computeClusters(nodes, edges)
	if len(clusters) != 1 {
		t.Fatalf("clusters = %+v, want 1", clusters)
	}
	for _, id := range clusters[0].NodeIDs {
		if clusters[0].ID == id {
			t.Fatalf("cluster ID %q collides with member node ID %q", clusters[0].ID, id)
		}
	}
}

func TestBuildWorkflowGraphTagsNodesWithClusterID(t *testing.T) {
	def := workflowdef.Definition{
		Workflow: "cluster-test", Version: 1,
		Steps: []workflowdef.Step{
			{
				ID: "verify", Type: workflowdef.StepTypeTool, Action: "run.tests_lint_build",
				On: map[string]workflowdef.Target{"pass": {StepOrState: "COMPLETED"}, "fail": {StepOrState: "revise"}},
			},
			{ID: "revise", Type: workflowdef.StepTypeAgent, Role: "coder", Next: "verify"},
		},
	}
	g := buildWorkflowGraph(&def, "workflows/cluster-test.yaml")

	if len(g.Clusters) != 1 {
		t.Fatalf("Clusters = %+v, want 1", g.Clusters)
	}
	byID := map[string]GraphNode{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}
	if byID["verify"].Cluster == "" || byID["revise"].Cluster == "" {
		t.Errorf("verify/revise not tagged with a cluster: %+v / %+v", byID["verify"], byID["revise"])
	}
	if byID["verify"].Cluster != byID["revise"].Cluster {
		t.Errorf("verify and revise are in the same real loop but got different cluster ids: %q vs %q",
			byID["verify"].Cluster, byID["revise"].Cluster)
	}
	if byID["COMPLETED"].Cluster != "" {
		t.Errorf("COMPLETED (a terminal) must never be tagged with a cluster, got %q", byID["COMPLETED"].Cluster)
	}
}
