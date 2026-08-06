// Workflow visualization prototype v2: D3 (github.com/d3/d3) for
// rendering + pan/zoom, three different layout engines behind one
// shared renderer:
//
//   - dagre (LR / TB): the original baseline — a real Sugiyama/layered
//     algorithm, but with no concept of grouping, so a loop's members
//     land on whichever ranks their longest path from the entry step
//     puts them on.
//   - d3-force + a custom clustering force: physics-based, so a loop's
//     members cluster together as an emergent property (more mutual
//     edges pulling on each other than a single forward edge has), not
//     a computed grouping — plus a weak x-axis bias toward each node's
//     topological layer to keep left-to-right flow readable, the same
//     hybrid idea v1's force layout uses.
//   - ELK layered, hierarchical: the Eclipse Layout Kernel (via elkjs)
//     is the one engine here with real compound/nested-node support —
//     each GraphCluster becomes an actual ELK child container, laid out
//     internally by the same layered algorithm and then placed as one
//     unit in the top-level layout. This is the "do it properly"
//     option research pointed at directly (react-flow and svelte-flow's
//     own docs recommend ELK specifically for hierarchical grouping;
//     dagre has no equivalent).
//
// Every layout converts to one shared shape — {nodes, edges,
// clusterBoxes} in absolute pixel coordinates — so vizV2Render only
// needs to know how to draw that shape once, not once per engine.

const VIZ_V2_NODE_WIDTH = 170;
const VIZ_V2_NODE_HEIGHT = 52;

const VIZ_V2_LAYOUTS = [
  { id: "dagre-lr", label: "Dagre (left-to-right)", render: (canvas, graph) => vizV2Render(canvas, vizV2DagreLayout(graph, "LR")) },
  { id: "dagre-tb", label: "Dagre (top-to-bottom)", render: (canvas, graph) => vizV2Render(canvas, vizV2DagreLayout(graph, "TB")) },
  { id: "force-cluster", label: "D3-force + clustering", render: (canvas, graph) => vizV2Render(canvas, vizV2ForceLayout(graph)) },
  { id: "elk-hierarchical", label: "ELK layered (hierarchical)", render: (canvas, graph) => vizV2ElkLayout(graph).then((layout) => vizV2Render(canvas, layout)) },
];

function renderWorkflowV2(container) {
  buildGraphViewShell(container, {
    title: "workflow_v2 — D3",
    interactionHint: "drag to pan, scroll (or pinch) to zoom",
    layouts: VIZ_V2_LAYOUTS,
  });
}

function vizV2NodeSub(node) {
  return node.kind === "terminal" ? "terminal" : (node.role || node.action || "");
}

// ---- layout: dagre (LR or TB) ----

function vizV2DagreLayout(graph, rankdir) {
  // multigraph: true — a named edge (below) needs it, since dagre's
  // default graph rejects a name argument to setEdge outright rather
  // than silently ignoring it.
  const g = new dagre.graphlib.Graph({ multigraph: true });
  g.setGraph({ rankdir, nodesep: 24, ranksep: 90 });
  g.setDefaultEdgeLabel(() => ({}));
  for (const node of graph.nodes) {
    g.setNode(node.id, { width: VIZ_V2_NODE_WIDTH, height: VIZ_V2_NODE_HEIGHT, data: node });
  }
  for (const edge of graph.edges) {
    g.setEdge(edge.from, edge.to, { label: edge.label || "" }, edge.label || edge.to);
  }
  dagre.layout(g);

  const nodes = g.nodes().map((v) => {
    const n = g.node(v);
    return {
      id: v, x: n.x - n.width / 2, y: n.y - n.height / 2, width: n.width, height: n.height,
      kind: n.data.kind, sub: vizV2NodeSub(n.data),
    };
  });
  const edges = g.edges().map((e) => {
    const edge = g.edge(e);
    return { from: e.v, to: e.w, label: edge.label, points: edge.points };
  });

  return { nodes, edges, clusterBoxes: vizV2BoundingClusterBoxes(graph, nodes) };
}

// ---- layout: d3-force with a custom clustering force ----

function vizV2ForceLayout(graph) {
  const clusterOf = {};
  for (const c of graph.clusters || []) {
    for (const id of c.node_ids) clusterOf[id] = c.id;
  }

  const directedAdj = {};
  for (const n of graph.nodes) directedAdj[n.id] = [];
  for (const e of graph.edges) directedAdj[e.from].push(e.to);
  const entry = graph.nodes.find((n) => n.kind !== "terminal") || graph.nodes[0];
  const layer = vizV1ComputeLayers(graph.nodes.map((n) => n.id), directedAdj, entry.id);
  const maxLayer = Math.max(1, ...Object.values(layer));
  const targetX = (id) => (layer[id] / maxLayer) * (graph.nodes.length * 90);

  const simNodes = graph.nodes.map((n, i) => ({
    id: n.id, kind: n.kind, sub: vizV2NodeSub(n),
    x: Math.cos(i) * 200, y: Math.sin(i) * 200,
  }));
  const simLinks = graph.edges.map((e) => ({ source: e.from, target: e.to, label: e.label }));

  // Custom clustering force: pull each clustered node toward its
  // cluster's current centroid — the standard "cluster force" pattern
  // (e.g. github.com/vasturiano/d3-force-cluster), hand-rolled here
  // rather than vendored since it's ~10 lines and this is the one force
  // in the simulation this whole prototype exists to demonstrate.
  function clusterForce(alpha) {
    const centroids = {};
    const counts = {};
    for (const n of simNodes) {
      const c = clusterOf[n.id];
      if (!c) continue;
      centroids[c] = centroids[c] || { x: 0, y: 0 };
      centroids[c].x += n.x;
      centroids[c].y += n.y;
      counts[c] = (counts[c] || 0) + 1;
    }
    for (const c in centroids) {
      centroids[c].x /= counts[c];
      centroids[c].y /= counts[c];
    }
    for (const n of simNodes) {
      const c = clusterOf[n.id];
      if (!c) continue;
      n.vx -= (n.x - centroids[c].x) * alpha * 0.4;
      n.vy -= (n.y - centroids[c].y) * alpha * 0.4;
    }
  }

  const simulation = d3.forceSimulation(simNodes)
    .force("link", d3.forceLink(simLinks).id((d) => d.id).distance(140).strength(0.5))
    .force("charge", d3.forceManyBody().strength(-450))
    .force("collide", d3.forceCollide(70))
    .force("flow", d3.forceX((d) => targetX(d.id)).strength(0.03))
    .force("cluster", clusterForce)
    .stop();
  for (let i = 0; i < 350; i++) simulation.tick();

  const byId = {};
  for (const n of simNodes) byId[n.id] = n;

  const nodes = simNodes.map((n) => ({
    id: n.id, x: n.x - VIZ_V2_NODE_WIDTH / 2, y: n.y - VIZ_V2_NODE_HEIGHT / 2,
    width: VIZ_V2_NODE_WIDTH, height: VIZ_V2_NODE_HEIGHT, kind: n.kind, sub: n.sub,
  }));
  // Force layout has no routed edge geometry — a straight line between
  // centers is the honest representation of what the physics actually
  // produced, not an approximation of something fancier. It does need
  // trimming back from each *center* to each node's approximate border,
  // though: an untrimmed line ends underneath the target node's own
  // rect, which is drawn on top of it — the arrowhead marker (and the
  // label, placed at the line's midpoint) would be invisible, hidden
  // behind the very node they're pointing at.
  const borderMargin = VIZ_V2_NODE_WIDTH / 2;
  const edges = graph.edges.map((e) => {
    const a = byId[e.from], b = byId[e.to];
    const dx = b.x - a.x, dy = b.y - a.y;
    const dist = Math.max(1, Math.sqrt(dx * dx + dy * dy));
    const ux = dx / dist, uy = dy / dist;
    const trim = Math.min(borderMargin, dist / 2 - 4);
    return {
      from: e.from, to: e.to, label: e.label,
      points: [
        { x: a.x + ux * trim, y: a.y + uy * trim },
        { x: b.x - ux * trim, y: b.y - uy * trim },
      ],
    };
  });

  return { nodes, edges, clusterBoxes: vizV2BoundingClusterBoxes(graph, nodes) };
}

// ---- layout: ELK layered, hierarchical (compound nodes for clusters) ----

async function vizV2ElkLayout(graph) {
  const clusterByID = {};
  const superOfNode = {};
  for (const c of graph.clusters || []) {
    clusterByID[c.id] = c;
    for (const id of c.node_ids) superOfNode[id] = c.id;
  }

  const children = [];
  for (const c of graph.clusters || []) {
    children.push({
      id: c.id,
      layoutOptions: { "elk.algorithm": "layered", "elk.direction": "RIGHT" },
      children: c.node_ids.map((id) => ({ id, width: VIZ_V2_NODE_WIDTH, height: VIZ_V2_NODE_HEIGHT })),
    });
  }
  for (const n of graph.nodes) {
    if (superOfNode[n.id]) continue;
    children.push({ id: n.id, width: VIZ_V2_NODE_WIDTH, height: VIZ_V2_NODE_HEIGHT });
  }

  const elkGraph = {
    id: "root",
    layoutOptions: {
      "elk.algorithm": "layered",
      "elk.direction": "RIGHT",
      "elk.hierarchyHandling": "INCLUDE_CHILDREN",
      "elk.layered.spacing.nodeNodeBetweenLayers": "70",
      "elk.spacing.nodeNode": "24",
    },
    children,
    edges: graph.edges.map((e, i) => ({ id: `e${i}`, sources: [e.from], targets: [e.to] })),
  };

  const elk = new ELK();
  const result = await elk.layout(elkGraph);

  // ELK gives every child's x/y relative to its own parent — walk the
  // tree accumulating parent offsets to get absolute canvas positions,
  // same reason a nested SVG <g transform="translate(...)"> tree needs
  // walking to know where a deeply-nested element actually ends up.
  const nodes = [];
  const clusterBoxes = [];
  const byID = {};
  for (const n of graph.nodes) byID[n.id] = n;

  function walk(elkNode, offsetX, offsetY) {
    const absX = offsetX + (elkNode.x || 0);
    const absY = offsetY + (elkNode.y || 0);
    if (clusterByID[elkNode.id]) {
      clusterBoxes.push({ x: absX - 10, y: absY - 10, width: (elkNode.width || 0) + 20, height: (elkNode.height || 0) + 20 });
    }
    if (byID[elkNode.id]) {
      const src = byID[elkNode.id];
      nodes.push({
        id: elkNode.id, x: absX, y: absY, width: elkNode.width, height: elkNode.height,
        kind: src.kind, sub: vizV2NodeSub(src),
      });
    }
    for (const child of elkNode.children || []) walk(child, absX, absY);
  }
  walk(result, 0, 0);

  // Edge routing: ELK reports one or more "sections" per edge (start
  // point, optional bend points, end point), each in the coordinate
  // space of the edge's own container in the hierarchy — for a
  // cross-hierarchy edge (e.g. plan -> the cluster's "verify" child)
  // that container is the least common ancestor, not always the root.
  // Rather than re-deriving each edge's LCA offset, ELK's own
  // `result.edges[].sections` already come back in ROOT coordinates for
  // edges elkjs promotes to the root during hierarchical layout, which
  // is the case for every edge here since hierarchyHandling is
  // INCLUDE_CHILDREN — use them directly.
  const edgesByID = {};
  graph.edges.forEach((e, i) => (edgesByID[`e${i}`] = e));
  const edges = (result.edges || []).map((edge) => {
    const src = edgesByID[edge.id];
    const section = (edge.sections || [])[0];
    const points = section
      ? [section.startPoint, ...(section.bendPoints || []), section.endPoint]
      : [];
    return { from: src.from, to: src.to, label: src.label, points };
  });

  return { nodes, edges, clusterBoxes };
}

// ---- shared: bounding-box cluster hulls for layouts without a real
// compound-layout result (dagre, force) ----

function vizV2BoundingClusterBoxes(graph, nodes) {
  const byId = {};
  for (const n of nodes) byId[n.id] = n;
  const boxes = [];
  for (const c of graph.clusters || []) {
    const members = c.node_ids.map((id) => byId[id]).filter(Boolean);
    if (members.length === 0) continue;
    const minX = Math.min(...members.map((n) => n.x)) - 16;
    const minY = Math.min(...members.map((n) => n.y)) - 16;
    const maxX = Math.max(...members.map((n) => n.x + n.width)) + 16;
    const maxY = Math.max(...members.map((n) => n.y + n.height)) + 16;
    boxes.push({ x: minX, y: minY, width: maxX - minX, height: maxY - minY });
  }
  return boxes;
}

// ---- shared renderer: draws {nodes, edges, clusterBoxes} + wires zoom ----

function vizV2Render(canvas, layout) {
  canvas.innerHTML = "";
  const rect = canvas.getBoundingClientRect();
  const width = rect.width || 800;
  const height = rect.height || 640;

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

  const allX = layout.nodes.flatMap((n) => [n.x, n.x + n.width]);
  const allY = layout.nodes.flatMap((n) => [n.y, n.y + n.height]);
  const minX = Math.min(...allX), maxX = Math.max(...allX);
  const minY = Math.min(...allY), maxY = Math.max(...allY);
  const graphW = Math.max(1, maxX - minX), graphH = Math.max(1, maxY - minY);
  const fitScale = Math.min(1, (width - 60) / graphW, (height - 60) / graphH);
  const tx = width / 2 - ((minX + maxX) / 2) * fitScale;
  const ty = height / 2 - ((minY + maxY) / 2) * fitScale;

  if (layout.clusterBoxes && layout.clusterBoxes.length > 0) {
    root.append("g").attr("class", "graph-clusters")
      .selectAll("rect")
      .data(layout.clusterBoxes)
      .join("rect")
      .attr("class", "graph-cluster-hull")
      .attr("x", (d) => d.x).attr("y", (d) => d.y)
      .attr("width", (d) => d.width).attr("height", (d) => d.height)
      .attr("rx", 14);
  }

  const line = d3.line().x((d) => d.x).y((d) => d.y).curve(d3.curveBasis);
  const edgeLayer = root.append("g").attr("class", "graph-edges");
  for (const edge of layout.edges) {
    if (!edge.points || edge.points.length < 2) continue;
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
  }

  const nodeG = root.append("g").attr("class", "graph-nodes")
    .selectAll("g.graph-node")
    .data(layout.nodes)
    .join("g")
    .attr("class", (d) => `graph-node kind-${d.kind}`)
    .attr("transform", (d) => `translate(${d.x},${d.y})`);

  nodeG.append("rect")
    .attr("class", "graph-node-shape")
    .attr("width", (d) => d.width).attr("height", (d) => d.height)
    .attr("rx", 8);
  nodeG.append("text").attr("x", 10).attr("y", 21).text((d) => d.id);
  nodeG.filter((d) => d.sub)
    .append("text").attr("class", "graph-node-sub").attr("x", 10).attr("y", 38)
    .text((d) => d.sub);

  // Seed d3.zoom's own tracked transform at (tx,ty,fitScale) rather than
  // compositing a separate fixed offset on top of it — keeps
  // cursor-centered zoom math correct (d3 assumes the rendered transform
  // *is* its tracked state, not state-plus-an-external-offset).
  const zoom = d3.zoom()
    .scaleExtent([0.2, 3])
    .on("zoom", (event) => root.attr("transform", event.transform));
  svg.call(zoom);
  svg.call(zoom.transform, d3.zoomIdentity.translate(tx, ty).scale(fitScale));
}
