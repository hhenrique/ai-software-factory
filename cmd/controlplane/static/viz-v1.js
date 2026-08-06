// Workflow visualization prototype v1: zero dependencies. Hand-rolled
// layered layout (BFS distance-from-entry-step as column, handles the
// verify<->revise_verify-style back-edges every reference Workflow
// Definition has without special-casing them) and hand-rolled pan/zoom
// (drag to pan, wheel to zoom, both just an SVG <g transform>). This is
// the "how much can vanilla JS/SVG actually do" baseline the other two
// prototypes are compared against.

const VIZ_V1_NODE_WIDTH = 170;
const VIZ_V1_NODE_HEIGHT = 52;
const VIZ_V1_COLUMN_GAP = 90;
const VIZ_V1_ROW_GAP = 24;

function renderWorkflowV1(container) {
  buildGraphViewShell(container, {
    title: "workflow_v1 — vanilla SVG",
    interactionHint: "drag to pan, scroll to zoom",
    render: drawVizV1,
  });
}

function vizV1ComputeLayers(graph) {
  const adj = {};
  for (const n of graph.nodes) adj[n.id] = [];
  for (const e of graph.edges) {
    if (!adj[e.from]) adj[e.from] = [];
    adj[e.from].push(e.to);
  }

  const entry = graph.nodes.find((n) => n.kind !== "terminal") || graph.nodes[0];
  const layer = {};
  layer[entry.id] = 0;
  const visited = new Set([entry.id]);
  const queue = [entry.id];
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
  for (const n of graph.nodes) {
    if (!(n.id in layer)) layer[n.id] = 0;
  }
  return layer;
}

function vizV1Layout(graph) {
  const layer = vizV1ComputeLayers(graph);
  const byLayer = {};
  for (const n of graph.nodes) {
    (byLayer[layer[n.id]] = byLayer[layer[n.id]] || []).push(n);
  }

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

const SVG_NS = "http://www.w3.org/2000/svg";
function svgEl(tag, attrs) {
  const el = document.createElementNS(SVG_NS, tag);
  for (const [k, v] of Object.entries(attrs || {})) el.setAttribute(k, v);
  return el;
}

function drawVizV1(canvas, graph) {
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

  const pos = vizV1Layout(graph);

  // Fit the whole graph in the viewport on first render — a graph wider
  // than the canvas (every real reference Workflow Definition is) must
  // not start partially off-screen requiring the human to pan blind
  // before seeing the shape of what they opened this page to look at.
  const xs = Object.values(pos).map((p) => p.x);
  const ys = Object.values(pos).map((p) => p.y);
  const minX = Math.min(...xs), maxX = Math.max(...xs) + VIZ_V1_NODE_WIDTH;
  const minY = Math.min(...ys), maxY = Math.max(...ys) + VIZ_V1_NODE_HEIGHT;
  const graphWidth = maxX - minX, graphHeight = maxY - minY;
  let scale = Math.min(1, (width - 60) / graphWidth, (height - 60) / graphHeight);
  let tx = width / 2 - ((minX + maxX) / 2) * scale;
  let ty = height / 2 - ((minY + maxY) / 2) * scale;
  const applyTransform = () => g.setAttribute("transform", `translate(${tx},${ty}) scale(${scale})`);
  applyTransform();

  // Edges first, so nodes draw on top.
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

  // Pan (drag) + zoom (wheel), plain mouse events — no library.
  const wrap = canvas.parentElement;
  let dragging = false, lastX = 0, lastY = 0;
  wrap.addEventListener("mousedown", (ev) => {
    dragging = true;
    lastX = ev.clientX;
    lastY = ev.clientY;
    wrap.classList.add("grabbing");
  });
  window.addEventListener("mouseup", () => {
    dragging = false;
    wrap.classList.remove("grabbing");
  });
  window.addEventListener("mousemove", (ev) => {
    if (!dragging) return;
    tx += ev.clientX - lastX;
    ty += ev.clientY - lastY;
    lastX = ev.clientX;
    lastY = ev.clientY;
    applyTransform();
  });
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
  }, { passive: false });
}
