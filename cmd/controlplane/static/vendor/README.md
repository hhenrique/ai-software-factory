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
| `cytoscape.v3.min.js`      | https://unpkg.com/cytoscape@3/dist/cytoscape.min.js | 3.x     |
| `cytoscape-dagre.v2.js`    | https://unpkg.com/cytoscape-dagre@2/cytoscape-dagre.js | 2.x  |

To upgrade: re-download from the same unpkg URL with a newer major
version pinned, verify the visualization pages still render, update the
version in the filename and this table.
