// Workflow visualization prototype v3: Cytoscape.js
// (github.com/cytoscape/cytoscape.js). Four layouts, each just a config
// object handed to a mature library — the "pick a mature graph library
// and configure it" approach, essentially no custom layout math anywhere
// in this file (contrast v1's hand-rolled algorithms and v2's
// D3-assembled-from-primitives approach):
//
//   - dagre: the layered baseline, same as v2's.
//   - cose (built-in): Cytoscape's native force-directed layout —
//     clusters emerge from physics the same way v2's d3-force option
//     does, but with no clustering force added, as a baseline for
//     what plain force-directed gets you here on its own.
//   - cose-bilkent: an improved compound-aware force-directed layout
//     from Bilkent University's i-Vis lab, run flat (no compound
//     grouping) — isolates "is bilkent's algorithm better than plain
//     cose" from "does compound grouping help," since the next option
//     changes both at once.
//   - cose-bilkent, compound: the real clustering demonstration — each
//     GraphCluster becomes an actual Cytoscape compound (parent) node,
//     which cose-bilkent is specifically built to handle well (research:
//     "CoSE-Bilkent... features enhanced compound node placement").
//     Member nodes are literally contained within their cluster's box
//     the layout draws, not just visually near each other.

function renderWorkflowV3(container) {
  buildGraphViewShell(container, {
    title: "workflow_v3 — Cytoscape.js",
    interactionHint: "drag to pan, scroll to zoom, drag a node to move it — all built in",
    layouts: [
      { id: "dagre", label: "Dagre", render: (canvas, graph) => vizV3Render(canvas, graph, vizV3DagreLayoutOptions(), false) },
      { id: "cose", label: "Cose (built-in, flat)", render: (canvas, graph) => vizV3Render(canvas, graph, vizV3CoseLayoutOptions(), false) },
      { id: "cose-bilkent-flat", label: "Cose-Bilkent (flat)", render: (canvas, graph) => vizV3Render(canvas, graph, vizV3CoseBilkentLayoutOptions(), false) },
      { id: "cose-bilkent-compound", label: "Cose-Bilkent (compound clusters)", render: (canvas, graph) => vizV3Render(canvas, graph, vizV3CoseBilkentLayoutOptions(), true) },
    ],
  });
}

let vizV3ExtensionsRegistered = false;

function cssVar(name) {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

function vizV3DagreLayoutOptions() {
  return { name: "dagre", rankDir: "LR", nodeSep: 24, rankSep: 90, fit: true, padding: 40 };
}

function vizV3CoseLayoutOptions() {
  return { name: "cose", animate: false, nodeRepulsion: 8000, idealEdgeLength: 130, fit: true, padding: 40 };
}

function vizV3CoseBilkentLayoutOptions() {
  return {
    name: "cose-bilkent", animate: false, fit: true, padding: 40,
    idealEdgeLength: 130, nodeRepulsion: 8000,
    // Only meaningful when compound parent nodes are present — ignored
    // otherwise, so this is safe to always pass.
    tile: false,
  };
}

function vizV3Render(canvas, graph, layoutOptions, useCompoundClusters) {
  canvas.innerHTML = "";

  if (!vizV3ExtensionsRegistered) {
    cytoscape.use(cytoscapeDagre);
    cytoscape.use(cytoscapeCoseBilkent);
    vizV3ExtensionsRegistered = true;
  }

  const kindColor = (kind) => cssVar(`--color-node-${kind}`) || cssVar("--color-border");
  const clusterOf = {};
  if (useCompoundClusters) {
    for (const c of graph.clusters || []) {
      for (const id of c.node_ids) clusterOf[id] = c.id;
    }
  }

  const elements = [];
  if (useCompoundClusters) {
    for (const c of graph.clusters || []) {
      elements.push({ data: { id: c.id, isClusterParent: true } });
    }
  }
  for (const node of graph.nodes) {
    const sub = node.kind === "terminal" ? "terminal" : (node.role || node.action || "");
    const data = { id: node.id, label: sub ? `${node.id}\n${sub}` : node.id, kind: node.kind };
    if (clusterOf[node.id]) data.parent = clusterOf[node.id];
    elements.push({ data });
  }
  for (const edge of graph.edges) {
    elements.push({
      data: {
        id: `${edge.from}->${edge.to}:${edge.label || ""}`,
        source: edge.from,
        target: edge.to,
        label: edge.label || "",
      },
    });
  }

  cytoscape({
    container: canvas,
    elements,
    style: [
      {
        selector: "node",
        style: {
          shape: "round-rectangle",
          width: 170,
          height: 52,
          "background-color": (ele) => ele.data("kind") === "terminal" ? cssVar("--color-bg") : cssVar("--color-surface-raised"),
          "border-width": 1.5,
          "border-color": (ele) => kindColor(ele.data("kind")),
          label: "data(label)",
          "text-wrap": "wrap",
          "text-max-width": "150px",
          "text-valign": "center",
          "text-halign": "center",
          color: cssVar("--color-text"),
          "font-family": "ui-monospace, Menlo, Consolas, monospace",
          "font-size": 11,
        },
      },
      {
        // Compound (cluster) container nodes — matched by the
        // isClusterParent flag rather than the `:parent` pseudo-selector,
        // since with useCompoundClusters: false no node has children and
        // `:parent` would just never match (equivalent here, but explicit
        // reads clearer than relying on that always being true).
        selector: "node[?isClusterParent]",
        style: {
          shape: "round-rectangle",
          // Cytoscape's background-color doesn't honor an rgba() alpha
          // channel — it takes the color literally and expects
          // background-opacity as the separate, only transparency
          // control. --color-cluster-fill is rgba(...,0.08) for the
          // SVG-based v1/v2 hulls (an actual translucent fill there);
          // reusing it here with background-opacity:1 rendered as a
          // solid, fully-opaque swatch instead of a subtle highlight —
          // caught via a live screenshot, not visible from the code
          // alone. Use the solid ring color plus a low opacity instead.
          "background-color": cssVar("--color-cluster-ring"),
          "background-opacity": 0.12,
          "border-width": 1.5,
          "border-style": "dashed",
          "border-color": cssVar("--color-cluster-ring"),
          label: "",
          padding: "16px",
        },
      },
      {
        selector: "edge",
        style: {
          width: 1.5,
          "line-color": cssVar("--color-edge"),
          "target-arrow-color": cssVar("--color-edge"),
          "target-arrow-shape": "triangle",
          "arrow-scale": 0.9,
          "curve-style": "bezier",
          label: "data(label)",
          "font-family": "ui-monospace, Menlo, Consolas, monospace",
          "font-size": 9,
          color: cssVar("--color-edge-label"),
          "text-background-color": cssVar("--color-bg"),
          "text-background-opacity": 1,
          "text-background-padding": "2px",
        },
      },
    ],
    layout: layoutOptions,
    userZoomingEnabled: true,
    userPanningEnabled: true,
    boxSelectionEnabled: false,
    minZoom: 0.2,
    maxZoom: 3,
  });
}
