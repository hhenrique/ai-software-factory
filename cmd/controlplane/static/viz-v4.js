// Workflow visualization prototype v4: BPMN-styled swimlane diagram.
//
// Scope, per the design discussion this follows: BPMN's *visual
// vocabulary* only — pools/lanes, exclusive gateways, boundary events,
// sub-processes — hand-computed and hand-drawn in SVG, the same posture
// as v1/v2. This deliberately does NOT depend on bpmn-js, bpmn-auto-
// layout, or emit real BPMN XML: no BPMN toolchain, no second stored
// format, YAML stays the only source of truth. The point of borrowing the
// notation is that a reader already fluent in it can look at the diagram
// and ask "does this make sense" (e.g. spot that MERGING — PR creation —
// sits strictly downstream of the whole Coder/Reviewer loop) without
// reading prose; it is not a validation/linting layer, which
// internal/workflowdef's Validate already owns and this doesn't touch.
//
// Mapping from our schema onto BPMN elements:
//   - lane (pool)     -> Role. Conductor lane holds every tool-owned step
//                        plus the tool-owned terminals COMPLETED/FAILED/
//                        CANCELLED (doc01's ownership table). REVIEW_PENDING
//                        is the one human-owned wait point, drawn in a
//                        Human lane. Every agent step's own Role gets a
//                        lane, in first-appearance (YAML declaration) order.
//   - exclusive gateway -> a step's `on:` outcomes, when there are 2+ of
//                        them (a single labeled edge needs no gateway —
//                        BPMN linting itself would flag a one-branch
//                        gateway as pointless, so this prototype doesn't
//                        draw one either).
//   - boundary event   -> `on_malformed_output`. This is an interrupting
//                        exception, not an expected-outcome route, so it's
//                        drawn as BPMN convention has it: a small circle on
//                        the task's own border with a dashed flow out, kept
//                        visually distinct from ordinary gateway branches.
//   - sub-process      -> a GraphCluster (a real back/forth loop). Drawn
//                        expanded (member nodes visible inside a dashed
//                        boundary), not collapsible — that's real BPMN
//                        interactivity this prototype doesn't attempt yet.
//   - end event        -> COMPLETED (plain), FAILED (error end event,
//                        lightning-bolt icon), CANCELLED (terminate end
//                        event, filled inner circle — "stop everything now"
//                        is what CANCELLED actually means).
//   - intermediate event -> REVIEW_PENDING: doc05 already implements this
//                        as a literal Temporal signal-wait, which is
//                        exactly what a BPMN intermediate catch event
//                        models — it is not a true terminal (a Run can
//                        resume from it), unlike the other three.
//
// Column position uses vizV4ComputeLayers (longest-path-from-entry BFS
// ranks) purely for left-to-right order; the cross-axis (lane) comes
// from Role/ownership, never from the layering algorithm.

const VIZ_V4_TASK_WIDTH = 170;
const VIZ_V4_TASK_HEIGHT = 52;
const VIZ_V4_COLUMN_WIDTH = 210;
const VIZ_V4_GATEWAY_SIZE = 34;

// vizV4ComputeLayers assigns each node a rank (its longest path from
// entry, via BFS relaxation) — the same layering primitive the pruned
// vizV1 hand-rolled-layout prototype used, kept here since v4 is its
// only remaining consumer.
function vizV4ComputeLayers(nodeIds, adj, entryId) {
  const layer = { [entryId]: 0 };
  const visited = new Set([entryId]);
  const queue = [entryId];
  while (queue.length > 0) {
    const cur = queue.shift();
    for (const next of adj[cur] || []) {
      layer[next] = Math.max(layer[next] ?? 0, layer[cur] + 1);
      if (!visited.has(next)) {
        visited.add(next);
        queue.push(next);
      }
    }
  }
  for (const id of nodeIds) {
    if (!(id in layer)) layer[id] = 0;
  }
  return layer;
}
const VIZ_V4_GATEWAY_GAP = 60;
const VIZ_V4_EVENT_DIAMETER = 44;
const VIZ_V4_BOUNDARY_DIAMETER = 18;
const VIZ_V4_LANE_HEIGHT = 130;
const VIZ_V4_LANE_LABEL_WIDTH = 130;
const VIZ_V4_MARGIN = 60;

// path, when given, renders exactly that Workflow Definition with no
// picker — the Workflows view's "View BPMN" row toggle already knows
// which one.
function renderWorkflowV4(container, path) {
  buildGraphViewShell(container, {
    title: "BPMN-styled diagram",
    fixedPath: path,
    interactionHint:
      "drag to pan, scroll to zoom — lanes are Role, diamonds are on: outcomes, " +
      "dashed circles are on_malformed_output escape hatches, dashed boxes are loop sub-processes",
    layouts: [
      { id: "bpmn", label: "BPMN-styled swimlane", render: (canvas, graph) => vizV4Render(canvas, graph) },
    ],
  });
}

// ---- lanes ----

function vizV4Lanes(graph) {
  const lanes = [{ id: "conductor", label: "Conductor" }];
  const roleLaneIndex = {};
  for (const n of graph.nodes) {
    if (n.kind === "agent" && n.role && !(n.role in roleLaneIndex)) {
      roleLaneIndex[n.role] = lanes.length;
      lanes.push({ id: "role:" + n.role, label: n.role });
    }
  }
  const humanLane = lanes.length;
  lanes.push({ id: "human", label: "Human" });

  function laneOf(n) {
    if (n.id === "REVIEW_PENDING") return humanLane;
    if (n.kind === "agent") return roleLaneIndex[n.role];
    return 0; // tool steps + tool-owned terminals (COMPLETED/FAILED/CANCELLED)
  }

  return { lanes, laneOf };
}

function vizV4ShapeKind(node) {
  if (node.id === "REVIEW_PENDING") return "intermediate";
  if (node.kind === "terminal") {
    if (node.id === "FAILED") return "end-error";
    if (node.id === "CANCELLED") return "end-terminate";
    return "end";
  }
  return "task";
}

// ---- layout ----

function vizV4Layout(graph) {
  const { lanes, laneOf } = vizV4Lanes(graph);

  const adj = {};
  for (const n of graph.nodes) adj[n.id] = [];
  for (const e of graph.edges) (adj[e.from] = adj[e.from] || []).push(e.to);
  const entry = graph.nodes.find((n) => n.kind !== "terminal") || graph.nodes[0];
  const layer = vizV4ComputeLayers(graph.nodes.map((n) => n.id), adj, entry.id);

  // A real branch point is a node with 2+ outcome-labeled (`on:`) edges.
  // on_malformed_output never counts here — it becomes a boundary event
  // on the task itself below, not a gateway branch.
  const outcomeEdgesByFrom = {};
  for (const e of graph.edges) {
    if (e.label && e.label !== "malformed_output") {
      (outcomeEdgesByFrom[e.from] = outcomeEdgesByFrom[e.from] || []).push(e);
    }
  }
  const gatewayFor = {};
  for (const [from, edges] of Object.entries(outcomeEdgesByFrom)) {
    if (edges.length >= 2) gatewayFor[from] = "gateway:" + from;
  }

  const laneTop = (idx) => VIZ_V4_MARGIN + idx * VIZ_V4_LANE_HEIGHT;
  const colX = (col) => VIZ_V4_LANE_LABEL_WIDTH + VIZ_V4_MARGIN + col * VIZ_V4_COLUMN_WIDTH;

  // Collision handling within a lane+column is a simple downward stack,
  // not true 2D packing — these workflows run a few dozen steps at most,
  // so a dense layout that would need real packing never comes up.
  const occupied = {};
  const nodes = [];
  for (const n of graph.nodes) {
    const lane = laneOf(n);
    const col = layer[n.id];
    const key = lane + ":" + col;
    const stackIndex = occupied[key] || 0;
    occupied[key] = stackIndex + 1;
    const shape = vizV4ShapeKind(n);
    const isEvent = shape !== "task";
    const w = isEvent ? VIZ_V4_EVENT_DIAMETER : VIZ_V4_TASK_WIDTH;
    const h = isEvent ? VIZ_V4_EVENT_DIAMETER : VIZ_V4_TASK_HEIGHT;
    const bandCenter = laneTop(lane) + VIZ_V4_LANE_HEIGHT / 2 + stackIndex * (VIZ_V4_TASK_HEIGHT + 14);
    nodes.push({
      id: n.id, kind: n.kind, role: n.role, action: n.action, shape, lane,
      x: colX(col) - w / 2, y: bandCenter - h / 2, width: w, height: h,
    });
  }
  const byNodeId = {};
  for (const n of nodes) byNodeId[n.id] = n;

  const gatewayNodes = [];
  for (const [from, gwID] of Object.entries(gatewayFor)) {
    const src = byNodeId[from];
    gatewayNodes.push({
      id: gwID, shape: "gateway", lane: src.lane,
      x: src.x + src.width + VIZ_V4_GATEWAY_GAP - VIZ_V4_GATEWAY_SIZE / 2,
      y: src.y + src.height / 2 - VIZ_V4_GATEWAY_SIZE / 2,
      width: VIZ_V4_GATEWAY_SIZE, height: VIZ_V4_GATEWAY_SIZE,
    });
  }
  for (const g of gatewayNodes) byNodeId[g.id] = g;

  const edges = [];
  const boundaryEvents = [];
  for (const e of graph.edges) {
    if (e.label === "malformed_output") {
      const host = byNodeId[e.from];
      const marker = {
        x: host.x + host.width - VIZ_V4_BOUNDARY_DIAMETER * 0.8,
        y: host.y + host.height - VIZ_V4_BOUNDARY_DIAMETER * 0.8,
        width: VIZ_V4_BOUNDARY_DIAMETER, height: VIZ_V4_BOUNDARY_DIAMETER,
      };
      boundaryEvents.push({ hostId: e.from, to: e.to, label: e.label, marker });
      continue;
    }
    const gwID = gatewayFor[e.from];
    const from = gwID && e.label ? gwID : e.from;
    edges.push({ from, to: e.to, label: e.label });
  }
  // Each gatewayed task needs exactly one plain feeder edge into its gateway.
  for (const [from, gwID] of Object.entries(gatewayFor)) {
    edges.push({ from, to: gwID, label: "" });
  }

  return {
    lanes, nodes: [...nodes, ...gatewayNodes], edges, boundaryEvents, byNodeId,
    clusters: graph.clusters || [],
  };
}

// Forward edges get a horizontal S-curve; a back edge (target column
// behind source — a loop closing) dips below both nodes' own bands so it
// reads as a return path instead of cutting back through intervening
// columns. Same idea v1 relies on for loop edges, expressed as explicit
// via-points instead of emerging from a force simulation.
function vizV4EdgePoints(a, b) {
  const scx = a.x + a.width / 2, scy = a.y + a.height / 2;
  const tcx = b.x + b.width / 2, tcy = b.y + b.height / 2;
  if (tcx >= scx) {
    const sx = a.x + a.width, sy = scy;
    const tx = b.x, ty = tcy;
    const midX = (sx + tx) / 2;
    return [{ x: sx, y: sy }, { x: midX, y: sy }, { x: midX, y: ty }, { x: tx, y: ty }];
  }
  const dipY = Math.max(a.y + a.height, b.y + b.height) + 44;
  return [
    { x: scx, y: a.y + a.height },
    { x: scx, y: dipY },
    { x: tcx, y: dipY },
    { x: tcx, y: b.y + b.height },
  ];
}

// ---- render ----

function vizV4Render(canvas, graph) {
  canvas.innerHTML = "";
  const rect = canvas.getBoundingClientRect();
  const width = rect.width || 800;
  const height = rect.height || 640;

  const layout = vizV4Layout(graph);

  const svg = d3.select(canvas).append("svg")
    .attr("width", "100%").attr("height", "100%")
    .attr("viewBox", `0 0 ${width} ${height}`);

  const defs = svg.append("defs");
  defs.append("marker")
    .attr("id", "v4-arrow").attr("viewBox", "0 0 10 10").attr("refX", 9).attr("refY", 5)
    .attr("markerWidth", 7).attr("markerHeight", 7).attr("orient", "auto-start-reverse")
    .append("path").attr("d", "M0,0 L10,5 L0,10 z").attr("fill", "var(--color-edge)");
  defs.append("marker")
    .attr("id", "v4-arrow-boundary").attr("viewBox", "0 0 10 10").attr("refX", 9).attr("refY", 5)
    .attr("markerWidth", 7).attr("markerHeight", 7).attr("orient", "auto-start-reverse")
    .append("path").attr("d", "M0,0 L10,5 L0,10 z").attr("fill", "var(--color-boundary)");

  const root = svg.append("g");

  const contentRight = Math.max(
    VIZ_V4_LANE_LABEL_WIDTH + 400,
    ...layout.nodes.map((n) => n.x + n.width),
  ) + VIZ_V4_MARGIN;
  const contentBottom = VIZ_V4_MARGIN + layout.lanes.length * VIZ_V4_LANE_HEIGHT;

  // lanes
  const laneLayer = root.append("g").attr("class", "v4-lanes");
  layout.lanes.forEach((lane, idx) => {
    const top = VIZ_V4_MARGIN + idx * VIZ_V4_LANE_HEIGHT;
    laneLayer.append("rect")
      .attr("class", "v4-lane-band" + (idx % 2 === 1 ? " alt" : ""))
      .attr("x", 0).attr("y", top).attr("width", contentRight).attr("height", VIZ_V4_LANE_HEIGHT);
    laneLayer.append("text")
      .attr("class", "v4-lane-label")
      .attr("x", 16).attr("y", top + VIZ_V4_LANE_HEIGHT / 2)
      .attr("dominant-baseline", "middle")
      .text(lane.label);
  });
  laneLayer.append("line")
    .attr("class", "v4-lane-rule")
    .attr("x1", VIZ_V4_LANE_LABEL_WIDTH).attr("x2", VIZ_V4_LANE_LABEL_WIDTH)
    .attr("y1", VIZ_V4_MARGIN).attr("y2", contentBottom);

  // sub-process boxes for loop clusters, drawn under nodes/edges
  const subProcLayer = root.append("g").attr("class", "v4-subprocesses");
  for (const cluster of layout.clusters) {
    const memberBoxes = [];
    for (const id of cluster.node_ids) {
      if (layout.byNodeId[id]) memberBoxes.push(layout.byNodeId[id]);
      if (layout.byNodeId["gateway:" + id]) memberBoxes.push(layout.byNodeId["gateway:" + id]);
    }
    for (const be of layout.boundaryEvents) {
      if (cluster.node_ids.includes(be.hostId)) memberBoxes.push(be.marker);
    }
    if (memberBoxes.length === 0) continue;
    const minX = Math.min(...memberBoxes.map((n) => n.x)) - 24;
    const minY = Math.min(...memberBoxes.map((n) => n.y)) - 26;
    const maxXb = Math.max(...memberBoxes.map((n) => n.x + n.width)) + 24;
    const maxYb = Math.max(...memberBoxes.map((n) => n.y + n.height)) + 20;
    subProcLayer.append("rect")
      .attr("class", "v4-subprocess")
      .attr("x", minX).attr("y", minY).attr("width", maxXb - minX).attr("height", maxYb - minY)
      .attr("rx", 14);
    const budgetNode = graph.nodes.find((n) => cluster.node_ids.includes(n.id) && n.budget);
    subProcLayer.append("text")
      .attr("class", "v4-subprocess-label")
      .attr("x", minX + 10).attr("y", minY + 16)
      .text(budgetNode ? `↻ loop · budget: ${budgetNode.budget}` : "↻ loop");
  }

  // edges
  const edgeLayer = root.append("g").attr("class", "v4-edges");
  const line = d3.line().x((d) => d.x).y((d) => d.y).curve(d3.curveBasis);
  for (const e of layout.edges) {
    const a = layout.byNodeId[e.from], b = layout.byNodeId[e.to];
    if (!a || !b) continue;
    const pts = vizV4EdgePoints(a, b);
    edgeLayer.append("path").attr("class", "v4-edge").attr("d", line(pts)).attr("marker-end", "url(#v4-arrow)");
    if (e.label) {
      // pts[1] rather than the geometric midpoint: several edges from
      // different sources converging on the same target (e.g. multiple
      // gateways all routing to REVIEW_PENDING) share almost the same
      // midpoint and target-side y, but diverge right after their own
      // source — biasing toward the source keeps labels legible instead
      // of stacking illegibly near the shared target.
      const mid = pts[1];
      edgeLayer.append("text")
        .attr("class", "v4-edge-label")
        .attr("x", mid.x).attr("y", mid.y - 8).attr("text-anchor", "middle")
        .text(e.label);
    }
  }
  for (const be of layout.boundaryEvents) {
    const target = layout.byNodeId[be.to];
    if (!target) continue;
    const pts = vizV4EdgePoints(be.marker, target);
    edgeLayer.append("path")
      .attr("class", "v4-edge v4-edge-boundary")
      .attr("d", line(pts)).attr("marker-end", "url(#v4-arrow-boundary)");
    const mid = pts[1];
    edgeLayer.append("text")
      .attr("class", "v4-edge-label v4-edge-label-boundary")
      .attr("x", mid.x).attr("y", mid.y - 8).attr("text-anchor", "middle")
      .text(be.label);
  }

  // nodes: task rects, gateway diamonds, event circles
  const nodeLayer = root.append("g").attr("class", "v4-nodes");
  for (const n of layout.nodes) {
    const g = nodeLayer.append("g").attr("class", `v4-node v4-shape-${n.shape} kind-${n.kind || ""}`);
    if (n.shape === "task") {
      g.append("rect")
        .attr("class", "v4-task-shape")
        .attr("x", n.x).attr("y", n.y).attr("width", n.width).attr("height", n.height).attr("rx", 8);
      g.append("text")
        .attr("class", "v4-task-icon").attr("x", n.x + 10).attr("y", n.y + 16)
        .text(n.kind === "tool" ? "⚙" : "◆");
      g.append("text")
        .attr("x", n.x + n.width / 2).attr("y", n.y + n.height / 2 - 3)
        .attr("text-anchor", "middle").text(n.id);
      const sub = n.role || n.action || "";
      if (sub) {
        g.append("text")
          .attr("class", "v4-task-sub")
          .attr("x", n.x + n.width / 2).attr("y", n.y + n.height / 2 + 13)
          .attr("text-anchor", "middle").text(sub);
      }
    } else if (n.shape === "gateway") {
      const cx = n.x + n.width / 2, cy = n.y + n.height / 2, r = n.width / 2;
      const pts = [[cx, cy - r], [cx + r, cy], [cx, cy + r], [cx - r, cy]].map((p) => p.join(",")).join(" ");
      g.append("polygon").attr("class", "v4-gateway-shape").attr("points", pts);
      g.append("path")
        .attr("class", "v4-gateway-x")
        .attr("d",
          `M${cx - r * 0.35},${cy - r * 0.35} L${cx + r * 0.35},${cy + r * 0.35} ` +
          `M${cx + r * 0.35},${cy - r * 0.35} L${cx - r * 0.35},${cy + r * 0.35}`);
    } else {
      const cx = n.x + n.width / 2, cy = n.y + n.height / 2, r = n.width / 2;
      g.append("circle").attr("class", "v4-event-outer").attr("cx", cx).attr("cy", cy).attr("r", r);
      if (n.shape === "intermediate") {
        g.append("circle").attr("class", "v4-event-inner-ring").attr("cx", cx).attr("cy", cy).attr("r", r - 4);
      } else if (n.shape === "end-terminate") {
        g.append("circle").attr("class", "v4-event-fill").attr("cx", cx).attr("cy", cy).attr("r", r - 8);
      } else if (n.shape === "end-error") {
        g.append("path")
          .attr("class", "v4-event-error-bolt")
          .attr("d",
            `M${cx - 6},${cy - 9} L${cx + 3},${cy - 1} L${cx - 2},${cy + 1} ` +
            `L${cx + 6},${cy + 9} L${cx - 3},${cy + 1} L${cx + 2},${cy - 1} Z`);
      }
      g.append("text")
        .attr("class", "v4-event-label")
        .attr("x", cx).attr("y", n.y + n.height + 14)
        .attr("text-anchor", "middle").text(n.id);
    }
  }
  for (const be of layout.boundaryEvents) {
    const m = be.marker;
    const cx = m.x + m.width / 2, cy = m.y + m.height / 2, r = m.width / 2;
    const g = nodeLayer.append("g").attr("class", "v4-boundary-event");
    g.append("circle").attr("class", "v4-boundary-outer").attr("cx", cx).attr("cy", cy).attr("r", r);
    g.append("circle").attr("class", "v4-boundary-inner").attr("cx", cx).attr("cy", cy).attr("r", r - 3);
  }

  const graphW = Math.max(1, contentRight);
  const graphH = Math.max(1, contentBottom + 40);
  // No upper cap here (unlike an earlier version that capped at 1x) — a
  // small graph in a large canvas should scale up to fill it, matching
  // v3/Cytoscape's `fit: true` behavior. d3.zoom's own scaleExtent below
  // still clamps the final transform, so this can't runaway.
  const fitScale = Math.min((width - 40) / graphW, (height - 40) / graphH);
  const tx = width / 2 - (contentRight / 2) * fitScale;
  const ty = height / 2 - (contentBottom / 2) * fitScale;

  const zoom = d3.zoom()
    .scaleExtent([0.2, 3])
    .on("zoom", (event) => root.attr("transform", event.transform));
  svg.call(zoom);
  svg.call(zoom.transform, d3.zoomIdentity.translate(tx, ty).scale(fitScale));
}
