// Workflow visualization prototype v1: zero dependencies. Three
// hand-rolled layout algorithms sharing one renderer + one hand-rolled
// pan/zoom implementation (drag to pan, wheel to zoom, both just an SVG
// <g transform>) — the "how much can vanilla JS/SVG actually do"
// baseline the other two prototypes are compared against.
//
// Layout research (see docs/04's visualization note for the summary):
// pure layered/Sugiyama layout (dagre, and this file's own "Layered"
// option) ranks nodes by longest path from the entry step, which is
// exactly why a back/forth loop like verify<->revise_verify gets its
// members pushed onto *different* ranks and stretched apart — the loop
// isn't visually a loop anymore, it's a detour. "Clustered layered"
// fixes this the direct way: collapse each real cycle (a
// GraphCluster — see cmd/controlplane/graph.go's computeClusters) into
// one slot for ranking purposes, then expand it back into a tight
// stack at render time. "Force-directed" fixes it a different way: a
// physics simulation naturally pulls mutually-connected nodes (a loop,
// almost by definition) closer together than nodes with only one
// forward edge between them, with no explicit cluster-awareness needed
// at all — clustering is an emergent property of the physics, not
// something computed and applied.

// Tracks the current draw call's pan/zoom listeners so the next one can
// abort them — see vizV1DrawGraph's cleanup comment.
let vizV1ActivePanZoom = null;

const VIZ_V1_NODE_WIDTH = 170;
const VIZ_V1_NODE_HEIGHT = 52;
const VIZ_V1_COLUMN_GAP = 90;
const VIZ_V1_ROW_GAP = 24;

const VIZ_V1_LAYOUTS = [
  {
    id: "layered", label: "Layered (BFS ranks)",
    render: (canvas, graph) => vizV1DrawGraph(canvas, graph, vizV1LayeredPositions, false),
  },
  {
    id: "clustered", label: "Clustered layered",
    render: (canvas, graph) => vizV1DrawGraph(canvas, graph, vizV1ClusteredLayeredPositions, true),
  },
  {
    id: "force", label: "Force-directed",
    render: (canvas, graph) => vizV1DrawGraph(canvas, graph, vizV1ForcePositions, true),
  },
];

function renderWorkflowV1(container) {
  buildGraphViewShell(container, {
    title: "workflow_v1 — vanilla SVG",
    interactionHint: "drag to pan, scroll to zoom",
    layouts: VIZ_V1_LAYOUTS,
  });
}

// ---- layout: plain layered (BFS distance-from-entry as column) ----

function vizV1ComputeLayers(nodeIds, adj, entryId) {
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

function vizV1LayeredPositions(graph) {
  const adj = {};
  for (const n of graph.nodes) adj[n.id] = [];
  for (const e of graph.edges) (adj[e.from] = adj[e.from] || []).push(e.to);

  const entry = graph.nodes.find((n) => n.kind !== "terminal") || graph.nodes[0];
  const layer = vizV1ComputeLayers(graph.nodes.map((n) => n.id), adj, entry.id);

  const byLayer = {};
  for (const n of graph.nodes) (byLayer[layer[n.id]] = byLayer[layer[n.id]] || []).push(n);

  const pos = {};
  const columnKeys = Object.keys(byLayer).map(Number).sort((a, b) => a - b);
  for (const col of columnKeys) {
    const nodes = byLayer[col];
    const totalHeight = nodes.length * VIZ_V1_NODE_HEIGHT + (nodes.length - 1) * VIZ_V1_ROW_GAP;
    nodes.forEach((n, i) => {
      pos[n.id] = {
        x: col * (VIZ_V1_NODE_WIDTH + VIZ_V1_COLUMN_GAP),
        y: i * (VIZ_V1_NODE_HEIGHT + VIZ_V1_ROW_GAP) - totalHeight / 2,
      };
    });
  }
  return pos;
}

// ---- layout: clustered layered (condense each loop into one rank slot) ----

function vizV1ClusteredLayeredPositions(graph) {
  const clusterByID = {};
  const superOfNode = {};
  for (const c of graph.clusters || []) {
    clusterByID[c.id] = c;
    for (const id of c.node_ids) superOfNode[id] = c.id;
  }
  const superOf = (id) => superOfNode[id] || id;

  const supers = Array.from(new Set(graph.nodes.map((n) => superOf(n.id))));
  const condAdj = {};
  for (const s of supers) condAdj[s] = [];
  for (const e of graph.edges) {
    const a = superOf(e.from), b = superOf(e.to);
    if (a !== b) condAdj[a].push(b);
  }

  const entry = graph.nodes.find((n) => n.kind !== "terminal") || graph.nodes[0];
  const layer = vizV1ComputeLayers(supers, condAdj, superOf(entry.id));

  const byLayer = {};
  for (const s of supers) (byLayer[layer[s]] = byLayer[layer[s]] || []).push(s);

  const memberGap = 14;
  const pos = {};
  const columnKeys = Object.keys(byLayer).map(Number).sort((a, b) => a - b);
  for (const col of columnKeys) {
    const items = byLayer[col];
    const heights = items.map((s) => {
      const c = clusterByID[s];
      return c ? c.node_ids.length * VIZ_V1_NODE_HEIGHT + (c.node_ids.length - 1) * memberGap : VIZ_V1_NODE_HEIGHT;
    });
    const totalHeight = heights.reduce((a, b) => a + b, 0) + (items.length - 1) * VIZ_V1_ROW_GAP;
    const x = col * (VIZ_V1_NODE_WIDTH + VIZ_V1_COLUMN_GAP);
    let y = -totalHeight / 2;
    items.forEach((s, i) => {
      const c = clusterByID[s];
      if (c) {
        c.node_ids.forEach((id, j) => {
          pos[id] = { x, y: y + j * (VIZ_V1_NODE_HEIGHT + memberGap) };
        });
      } else {
        pos[s] = { x, y };
      }
      y += heights[i] + VIZ_V1_ROW_GAP;
    });
  }
  return pos;
}

// ---- layout: force-directed (Fruchterman-Reingold-ish, hand-rolled) ----
//
// No cluster-awareness at all — a loop's members simply have more edges
// pulling them toward each other than a node with a single forward edge
// has pulling it toward its neighbor, so they settle closer together as
// an emergent property of the simulation, not a computed grouping. A
// weak x-axis bias toward each node's BFS layer keeps some left-to-right
// reading order rather than a fully undirected blob — pure force layout
// throws away the graph's directionality entirely, which reads badly for
// a process with a clear start and end.
function vizV1ForcePositions(graph) {
  const adj = {};
  for (const n of graph.nodes) adj[n.id] = [];
  for (const e of graph.edges) {
    (adj[e.from] = adj[e.from] || []).push(e.to);
    (adj[e.to] = adj[e.to] || []).push(e.from);
  }
  const entry = graph.nodes.find((n) => n.kind !== "terminal") || graph.nodes[0];
  const directedAdj = {};
  for (const n of graph.nodes) directedAdj[n.id] = [];
  for (const e of graph.edges) directedAdj[e.from].push(e.to);
  const layer = vizV1ComputeLayers(graph.nodes.map((n) => n.id), directedAdj, entry.id);
  const maxLayer = Math.max(1, ...Object.values(layer));

  const pos = {};
  graph.nodes.forEach((n, i) => {
    const angle = (i / graph.nodes.length) * Math.PI * 2;
    pos[n.id] = { x: Math.cos(angle) * 300, y: Math.sin(angle) * 300, vx: 0, vy: 0 };
  });

  const targetX = (id) => (layer[id] / maxLayer) * (graph.nodes.length * 90) - (graph.nodes.length * 45);

  const iterations = 300;
  const idealEdgeLength = 130;
  for (let iter = 0; iter < iterations; iter++) {
    const forces = {};
    for (const n of graph.nodes) forces[n.id] = { x: 0, y: 0 };

    // Repulsion between every pair (Coulomb's law analog).
    for (let i = 0; i < graph.nodes.length; i++) {
      for (let j = i + 1; j < graph.nodes.length; j++) {
        const a = graph.nodes[i].id, b = graph.nodes[j].id;
        const dx = pos[a].x - pos[b].x, dy = pos[a].y - pos[b].y;
        const distSq = Math.max(1, dx * dx + dy * dy);
        const dist = Math.sqrt(distSq);
        const force = 9000 / distSq;
        forces[a].x += (dx / dist) * force;
        forces[a].y += (dy / dist) * force;
        forces[b].x -= (dx / dist) * force;
        forces[b].y -= (dy / dist) * force;
      }
    }

    // Attraction along edges (Hooke's law analog) — this is what pulls a
    // loop's mutually-connected members together.
    for (const e of graph.edges) {
      const a = pos[e.from], b = pos[e.to];
      if (!a || !b) continue;
      const dx = b.x - a.x, dy = b.y - a.y;
      const dist = Math.max(1, Math.sqrt(dx * dx + dy * dy));
      const force = (dist - idealEdgeLength) * 0.02;
      forces[e.from].x += (dx / dist) * force;
      forces[e.from].y += (dy / dist) * force;
      forces[e.to].x -= (dx / dist) * force;
      forces[e.to].y -= (dy / dist) * force;
    }

    // Weak pull toward each node's topological-layer x position — keeps
    // overall left-to-right flow readable without fighting the physics.
    for (const n of graph.nodes) {
      forces[n.id].x += (targetX(n.id) - pos[n.id].x) * 0.01;
    }

    for (const n of graph.nodes) {
      pos[n.id].vx = (pos[n.id].vx + forces[n.id].x) * 0.6;
      pos[n.id].vy = (pos[n.id].vy + forces[n.id].y) * 0.6;
      pos[n.id].x += pos[n.id].vx;
      pos[n.id].y += pos[n.id].vy;
    }
  }

  // Node rects are drawn from their top-left corner elsewhere in this
  // file; the simulation above treats (x,y) as a center point, so shift
  // once at the end rather than carrying two conventions through 300
  // iterations of arithmetic.
  const out = {};
  for (const n of graph.nodes) {
    out[n.id] = { x: pos[n.id].x - VIZ_V1_NODE_WIDTH / 2, y: pos[n.id].y - VIZ_V1_NODE_HEIGHT / 2 };
  }
  return out;
}

// ---- shared renderer + pan/zoom ----

const SVG_NS = "http://www.w3.org/2000/svg";
function svgEl(tag, attrs) {
  const el = document.createElementNS(SVG_NS, tag);
  for (const [k, v] of Object.entries(attrs || {})) el.setAttribute(k, v);
  return el;
}

function vizV1DrawGraph(canvas, graph, computePositions, showClusterHulls) {
  canvas.innerHTML = "";
  const rect = canvas.getBoundingClientRect();
  const width = rect.width || 800;
  const height = rect.height || 640;

  const svg = svgEl("svg", { width: "100%", height: "100%", viewBox: `0 0 ${width} ${height}` });
  canvas.appendChild(svg);

  const defs = svgEl("defs", {});
  const marker = svgEl("marker", {
    id: "v1-arrow", viewBox: "0 0 10 10", refX: "9", refY: "5",
    markerWidth: "7", markerHeight: "7", orient: "auto-start-reverse",
  });
  marker.appendChild(svgEl("path", { d: "M0,0 L10,5 L0,10 z", fill: "var(--color-edge)" }));
  defs.appendChild(marker);
  svg.appendChild(defs);

  const g = svgEl("g", { transform: "translate(0,0) scale(1)" });
  svg.appendChild(g);

  const pos = computePositions(graph);

  // Fit the whole graph in the viewport on first render — a graph wider
  // than the canvas (every real reference Workflow Definition is) must
  // not start partially off-screen requiring the human to pan blind
  // before seeing the shape of what they opened this page to look at.
  const xs = Object.values(pos).map((p) => p.x);
  const ys = Object.values(pos).map((p) => p.y);
  const minX = Math.min(...xs), maxX = Math.max(...xs) + VIZ_V1_NODE_WIDTH;
  const minY = Math.min(...ys), maxY = Math.max(...ys) + VIZ_V1_NODE_HEIGHT;
  const graphWidth = Math.max(1, maxX - minX), graphHeight = Math.max(1, maxY - minY);
  let scale = Math.min(1, (width - 60) / graphWidth, (height - 60) / graphHeight);
  let tx = width / 2 - ((minX + maxX) / 2) * scale;
  let ty = height / 2 - ((minY + maxY) / 2) * scale;
  const applyTransform = () => g.setAttribute("transform", `translate(${tx},${ty}) scale(${scale})`);
  applyTransform();

  if (showClusterHulls) {
    const hullLayer = svgEl("g", { class: "graph-clusters" });
    g.appendChild(hullLayer);
    for (const c of graph.clusters || []) {
      const members = c.node_ids.map((id) => pos[id]).filter(Boolean);
      if (members.length === 0) continue;
      const hMinX = Math.min(...members.map((p) => p.x)) - 16;
      const hMinY = Math.min(...members.map((p) => p.y)) - 16;
      const hMaxX = Math.max(...members.map((p) => p.x + VIZ_V1_NODE_WIDTH)) + 16;
      const hMaxY = Math.max(...members.map((p) => p.y + VIZ_V1_NODE_HEIGHT)) + 16;
      hullLayer.appendChild(svgEl("rect", {
        class: "graph-cluster-hull",
        x: hMinX, y: hMinY, width: hMaxX - hMinX, height: hMaxY - hMinY, rx: 14,
      }));
    }
  }

  const edgeLayer = svgEl("g", { class: "graph-edges" });
  g.appendChild(edgeLayer);
  for (const edge of graph.edges) {
    const from = pos[edge.from], to = pos[edge.to];
    if (!from || !to) continue;
    const x1 = from.x + VIZ_V1_NODE_WIDTH, y1 = from.y + VIZ_V1_NODE_HEIGHT / 2;
    const x2 = to.x, y2 = to.y + VIZ_V1_NODE_HEIGHT / 2;
    const dx = Math.max(40, Math.abs(x2 - x1) / 2);
    const path = svgEl("path", {
      class: "graph-edge-path",
      d: `M${x1},${y1} C${x1 + dx},${y1} ${x2 - dx},${y2} ${x2},${y2}`,
      "marker-end": "url(#v1-arrow)",
    });
    edgeLayer.appendChild(path);
    if (edge.label) {
      const label = svgEl("text", {
        class: "graph-edge-label",
        x: (x1 + x2) / 2, y: (y1 + y2) / 2 - 6, "text-anchor": "middle",
      });
      label.textContent = edge.label;
      edgeLayer.appendChild(label);
    }
  }

  const nodeLayer = svgEl("g", { class: "graph-nodes" });
  g.appendChild(nodeLayer);
  for (const node of graph.nodes) {
    const p = pos[node.id];
    if (!p) continue;
    const nodeG = svgEl("g", { class: `graph-node kind-${node.kind}`, transform: `translate(${p.x},${p.y})` });
    nodeG.appendChild(svgEl("rect", {
      class: "graph-node-shape", width: VIZ_V1_NODE_WIDTH, height: VIZ_V1_NODE_HEIGHT, rx: 8,
    }));
    const label = svgEl("text", { x: 10, y: 21 });
    label.textContent = node.id;
    nodeG.appendChild(label);
    const sub = node.kind === "terminal" ? "terminal" : (node.role || node.action || node.kind);
    if (sub) {
      const subEl = svgEl("text", { class: "graph-node-sub", x: 10, y: 38 });
      subEl.textContent = sub;
      nodeG.appendChild(subEl);
    }
    nodeLayer.appendChild(nodeG);
  }

  // Pan (drag) + zoom (wheel), plain mouse events — no library. Every
  // switch of workflow or layout calls this function again, and two of
  // these listeners are attached to `window` (mouseup/mousemove need to
  // keep tracking a drag even if the cursor leaves the canvas) — without
  // explicit cleanup they'd accumulate one full extra set per switch,
  // silently piling up duplicate handlers for the life of the page. An
  // AbortController tied to this draw call's own listeners, aborted at
  // the start of the next one, is the cleanup.
  if (vizV1ActivePanZoom) vizV1ActivePanZoom.abort();
  vizV1ActivePanZoom = new AbortController();
  const { signal } = vizV1ActivePanZoom;

  const wrap = canvas.parentElement;
  let dragging = false, lastX = 0, lastY = 0;
  wrap.addEventListener("mousedown", (ev) => {
    dragging = true;
    lastX = ev.clientX;
    lastY = ev.clientY;
    wrap.classList.add("grabbing");
  }, { signal });
  window.addEventListener("mouseup", () => {
    dragging = false;
    wrap.classList.remove("grabbing");
  }, { signal });
  window.addEventListener("mousemove", (ev) => {
    if (!dragging) return;
    tx += ev.clientX - lastX;
    ty += ev.clientY - lastY;
    lastX = ev.clientX;
    lastY = ev.clientY;
    applyTransform();
  }, { signal });
  wrap.addEventListener("wheel", (ev) => {
    ev.preventDefault();
    const wrapRect = wrap.getBoundingClientRect();
    const cx = ev.clientX - wrapRect.left, cy = ev.clientY - wrapRect.top;
    const factor = ev.deltaY < 0 ? 1.1 : 1 / 1.1;
    const newScale = Math.min(3, Math.max(0.2, scale * factor));
    // Zoom centered on the cursor: keep the point under the cursor fixed.
    tx = cx - ((cx - tx) / scale) * newScale;
    ty = cy - ((cy - ty) / scale) * newScale;
    scale = newScale;
    applyTransform();
  }, { passive: false, signal });
}
