# Vendored libraries

Used only by the workflow_v2/workflow_v3 visualization prototypes
(workflow_v1 is dependency-free vanilla SVG). Downloaded once and
committed rather than loaded from a CDN — this is a self-hosted app, and
a CSP/offline-friendly static asset beats a runtime fetch to a third
party for a handful of files this small.

| File                      | Package                                            | Version |
|---------------------------|-----------------------------------------------------|---------|
| `d3.v7.min.js`             | https://unpkg.com/d3@7/dist/d3.min.js               | 7.x     |
| `dagre.v1.min.js`          | https://unpkg.com/@dagrejs/dagre@1/dist/dagre.min.js | 1.x     |
| `elk.v0.9.bundled.js`      | https://unpkg.com/elkjs@0.9/lib/elk.bundled.js      | 0.9.x   |
| `cytoscape.v3.min.js`      | https://unpkg.com/cytoscape@3/dist/cytoscape.min.js | 3.x     |
| `cytoscape-dagre.v2.js`    | https://unpkg.com/cytoscape-dagre@2/cytoscape-dagre.js | 2.x  |
| `layout-base.v2.js`        | https://unpkg.com/layout-base@2/layout-base.js      | 2.x     |
| `cose-base.v2.js`          | https://unpkg.com/cose-base@2/cose-base.js          | 2.x     |
| `cytoscape-cose-bilkent.v4.js` | https://unpkg.com/cytoscape-cose-bilkent@4/cytoscape-cose-bilkent.js | 4.x |

Load order matters for the cose-bilkent chain — each expects the previous
as an un-bundled peer dependency exposed as a plain global, same
relationship `dagre.v1.min.js` has to `cytoscape-dagre.v2.js`:
`layout-base` (`window.layoutBase`) → `cose-base` (`window.coseBase`,
itself needs `layoutBase`) → `cytoscape-cose-bilkent` (needs `coseBase`).

`elk.v0.9.bundled.js` (the Eclipse Layout Kernel, via elkjs) is the
heaviest single file here (~1.6MB) — vendored anyway because it's the one
library in this set with real hierarchical/compound-node support, used by
workflow_v2's "ELK layered (hierarchical)" option to cluster a Workflow
Definition's back/forth loops properly rather than approximating it
(dagre has no compound-node concept at all).

To upgrade: re-download from the same unpkg URL with a newer major
version pinned, verify the visualization pages still render, update the
version in the filename and this table.
