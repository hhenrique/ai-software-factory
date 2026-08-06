// Workflow visualization prototype v2: D3 (github.com/d3/d3) for
// rendering + pan/zoom (d3-zoom, part of the bundle), dagre
// (github.com/dagrejs/dagre) for layout — a real graph-layout algorithm
// (handles the verify<->revise_verify back-edges by routing around
// rather than v1's straight-line approximation) instead of hand-rolled
// BFS columns. More capable than v1 for less layout code, at the cost of
// two vendored dependencies (static/vendor/README.md) and D3's own
// imperative selection/data-join API to learn.

function renderWorkflowV2(container) {
  buildGraphViewShell(container, {
    title: "workflow_v2 — D3 + dagre",
    interactionHint: "drag to pan, scroll (or pinch) to zoom",
    render: drawVizV2,
  });
}

function drawVizV2(canvas, graph) {
  canvas.innerHTML = "";
  const rect = canvas.getBoundingClientRect();
  const width = rect.width || 800;
  const height = rect.height || 640;

  // multigraph: true — a named edge (below) needs it, since dagre's
  // default graph rejects a name argument to setEdge outright rather
  // than silently ignoring it.
  const g = new dagre.graphlib.Graph({ multigraph: true });
  g.setGraph({ rankdir: "LR", nodesep: 24, ranksep: 90 });
  g.setDefaultEdgeLabel(() => ({}));
  for (const node of graph.nodes) {
    g.setNode(node.id, { width: 170, height: 52, data: node });
  }
  for (const edge of graph.edges) {
    // dagre keys parallel edges by name — without one, a second edge
    // between the same two nodes (not expected today, but not
    // structurally impossible) would silently overwrite the first.
    g.setEdge(edge.from, edge.to, { label: edge.label || "" }, edge.label || edge.to);
  }
  dagre.layout(g);

  const svg = d3.select(canvas).append("svg")
    .attr("width", "100%").attr("height", "100%")
    .attr("viewBox", `0 0 ${width} ${height}`);

  svg.append("defs").append("marker")
    .attr("id", "v2-arrow")
    .attr("viewBox", "0 0 10 10").attr("refX", 9).attr("refY", 5)
    .attr("markerWidth", 7).attr("markerHeight", 7)
    .attr("orient", "auto-start-reverse")
    .append("path").attr("d", "M0,0 L10,5 L0,10 z").attr("fill", "var(--color-edge)");

  const root = svg.append("g");

  // Fit the whole graph in the viewport on first render (same reasoning
  // as v1: every real reference Workflow Definition is wider than the
  // canvas). dagre lays out in its own coordinate space starting near
  // (0,0); the fit scale + centering offset is applied below via
  // d3.zoom's own initial transform, not a separate attr() call, so
  // panning/zooming compose correctly from the start (see the zoom setup
  // at the bottom of this function).
  const graphInfo = g.graph();
  const fitScale = Math.min(1, (width - 60) / graphInfo.width, (height - 60) / graphInfo.height);
  const tx = width / 2 - (graphInfo.width / 2) * fitScale;
  const ty = height / 2 - (graphInfo.height / 2) * fitScale;

  const line = d3.line().x((d) => d.x).y((d) => d.y).curve(d3.curveBasis);

  const edgeLayer = root.append("g").attr("class", "graph-edges");
  g.edges().forEach((e) => {
    const edge = g.edge(e);
    edgeLayer.append("path")
      .attr("class", "graph-edge-path")
      .attr("d", line(edge.points))
      .attr("marker-end", "url(#v2-arrow)");
    if (edge.label) {
      const mid = edge.points[Math.floor(edge.points.length / 2)];
      edgeLayer.append("text")
        .attr("class", "graph-edge-label")
        .attr("x", mid.x).attr("y", mid.y - 6)
        .attr("text-anchor", "middle")
        .text(edge.label);
    }
  });

  const nodeLayer = root.append("g").attr("class", "graph-nodes");
  const nodeG = nodeLayer.selectAll("g.graph-node")
    .data(g.nodes().map((v) => g.node(v)))
    .join("g")
    .attr("class", (d) => `graph-node kind-${d.data.kind}`)
    .attr("transform", (d) => `translate(${d.x - d.width / 2},${d.y - d.height / 2})`);

  nodeG.append("rect")
    .attr("class", "graph-node-shape")
    .attr("width", (d) => d.width).attr("height", (d) => d.height)
    .attr("rx", 8);

  nodeG.append("text").attr("x", 10).attr("y", 21).text((d) => d.data.id);

  nodeG.filter((d) => d.data.kind === "terminal" || d.data.role || d.data.action)
    .append("text")
    .attr("class", "graph-node-sub")
    .attr("x", 10).attr("y", 38)
    .text((d) => d.data.kind === "terminal" ? "terminal" : (d.data.role || d.data.action));

  // Seed d3.zoom's own tracked transform at (tx,ty) rather than
  // compositing a separate fixed offset on top of it — that keeps
  // cursor-centered zoom math correct (d3 assumes the rendered
  // transform *is* its tracked state, not state-plus-an-external-offset).
  const zoom = d3.zoom()
    .scaleExtent([0.2, 3])
    .on("zoom", (event) => root.attr("transform", event.transform));
  svg.call(zoom);
  svg.call(zoom.transform, d3.zoomIdentity.translate(tx, ty).scale(fitScale));
}
