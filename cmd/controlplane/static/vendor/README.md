# Vendored libraries

Used by the Workflows view's "View Cytoscape" (`viz-v3.js`) and "View
BPMN" (`viz-v4.js`, hand-rolled — d3 only, no BPMN toolchain) row
toggles. Downloaded once and committed rather than loaded from a CDN —
this is a self-hosted app, and a CSP/offline-friendly static asset beats
a runtime fetch to a third party for a handful of files this small.

| File                      | Package                                            | Version |
|---------------------------|-----------------------------------------------------|---------|
| `d3.v7.min.js`             | https://unpkg.com/d3@7/dist/d3.min.js               | 7.x     |
| `dagre.v1.min.js`          | https://unpkg.com/@dagrejs/dagre@1/dist/dagre.min.js | 1.x     |
| `cytoscape.v3.min.js`      | https://unpkg.com/cytoscape@3/dist/cytoscape.min.js | 3.x     |
| `cytoscape-dagre.v2.js`    | https://unpkg.com/cytoscape-dagre@2/cytoscape-dagre.js | 2.x  |
| `layout-base.v2.js`        | https://unpkg.com/layout-base@2/layout-base.js      | 2.x     |
| `cose-base.v2.js`          | https://unpkg.com/cose-base@2/cose-base.js          | 2.x     |
| `cytoscape-cose-bilkent.v4.js` | https://unpkg.com/cytoscape-cose-bilkent@4/cytoscape-cose-bilkent.js | 4.x |

`dagre.v1.min.js` is a peer dependency, not loaded directly by any of our
own code — `cytoscape-dagre.v2.js` (v3's "Dagre" layout option) expects
the plain `dagre` global it exposes to already exist, same relationship
the cose-bilkent chain below has. Load order matters for that chain too
— each expects the previous as an un-bundled peer dependency exposed as
a plain global: `layout-base` (`window.layoutBase`) → `cose-base`
(`window.coseBase`, itself needs `layoutBase`) → `cytoscape-cose-bilkent`
(needs `coseBase`).

To upgrade: re-download from the same unpkg URL with a newer major
version pinned, verify the Workflows view's graph toggles still render,
update the version in the filename and this table.

## Pruned: ELK, bpmn-js, bpmn-auto-layout

`docs/06-workflow-visualizations.md`'s design-spike compared five
visualization prototypes (`workflow_v1`–`v5`, each its own standalone nav
item). Decision: keep only Cytoscape (v3, the layout-algorithm research's
lead candidate) and the hand-rolled BPMN-styled renderer (v4) — both
folded into the Workflows view as row-level toggles rather than separate
nav items — and drop v1/v2 (superseded by v3 in that same comparison)
and v5 (a real BPMN toolchain that hit the *same* unresolved edge-
convergence problem v4 has, plus its own toolchain gaps — lanes silently
dropped by `bpmn-auto-layout`, a mandatory watermark, heavier vendoring —
with no offsetting benefit over v4 on the actual open problem). See that
doc's Decision section for the full reasoning.

Removed with them: `elk.v0.9.bundled.js` (only `workflow_v2` used it —
`dagre.v1.min.js` stayed despite v2 also using it directly, since v3's
own "Dagre" layout option needs it as `cytoscape-dagre.v2.js`'s peer
dependency, above), `bpmn-js.v18.navigated-viewer.min.js` /
`bpmn-js.v18.css` / `diagram-js.v18.css` / `bpmn-embedded-font.v18.css` /
`bpmn-auto-layout.v1.bundle.js` (only `workflow_v5` used any of these —
v4 deliberately never depended on the real BPMN toolchain at all).
