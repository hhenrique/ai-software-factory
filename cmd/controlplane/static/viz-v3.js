// Workflow visualization prototype v3: Cytoscape.js
// (github.com/cytoscape/cytoscape.js) with the cytoscape-dagre layout
// extension. The "pick a mature graph library and configure it"
// approach — pan/zoom, drag, and layered layout all come from the
// library with a declarative style/layout config, essentially no custom
// interaction code (contrast v1's hand-rolled pan/zoom and v2's
// D3-assembled-from-primitives approach). Heaviest vendored dependency
// of the three (static/vendor/README.md), in exchange for the least
// code here.

function renderWorkflowV3(container) {
  buildGraphViewShell(container, {
    title: "workflow_v3 — Cytoscape.js + dagre",
    interactionHint: "drag to pan, scroll to zoom, drag a node to move it — all built in",
    render: drawVizV3,
  });
}

let vizV3DagreRegistered = false;

function cssVar(name) {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

function drawVizV3(canvas, graph) {
  canvas.innerHTML = "";

  if (!vizV3DagreRegistered) {
    cytoscape.use(cytoscapeDagre);
    vizV3DagreRegistered = true;
  }

  const kindColor = (kind) => cssVar(`--color-node-${kind}`) || cssVar("--color-border");

  const elements = [];
  for (const node of graph.nodes) {
    const sub = node.kind === "terminal" ? "terminal" : (node.role || node.action || "");
    elements.push({
      data: {
        id: node.id,
        label: sub ? `${node.id}\n${sub}` : node.id,
        kind: node.kind,
      },
    });
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

  const cy = cytoscape({
    container: canvas,
    elements: elements,
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
    layout: {
      name: "dagre",
      rankDir: "LR",
      nodeSep: 24,
      rankSep: 90,
      fit: true,
      padding: 40,
    },
    // Every one of these is Cytoscape's default; listed explicitly so
    // the "this is all built in, not code we wrote" point is visible in
    // the source, not just in the behavior.
    userZoomingEnabled: true,
    userPanningEnabled: true,
    boxSelectionEnabled: false,
    minZoom: 0.2,
    maxZoom: 3,
  });

  // Cytoscape owns its own lifecycle; nothing else to wire up (contrast
  // v1's manual mousedown/mousemove/wheel listeners).
  canvas._cy = cy;
}
