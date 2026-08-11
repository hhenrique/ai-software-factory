# Workflow Definition Visualizations — Spike Notes

Status: decided — v3 (Cytoscape.js) and v4 (hand-rolled BPMN-styled)
kept, accessible from the Workflows view's "View Cytoscape"/"View BPMN"
row toggles; v1, v2, and v5 dropped
Depends on: 01-run-state-machine.md (the states/transitions being drawn),
02-workflow-definition-schema.md (the YAML being drawn), 04-control-plane-mvp-scope.md
(where these renderers live in the control plane)
Consumed by: 04-control-plane-mvp-scope.md's "Graph visualizations" section

## Scope

Definition-level visualization only: a Workflow Definition has structure
and bounds (`max_attempts`/`max_rounds`) but no real elapsed time. This is
explicitly not about Run-level/runtime visualization (a Run has real
timestamps/counts/token deltas already captured in `run_events`) — that
stays on the roadmap, out of scope here.

The goal is a way to **reason about, present, discuss, and validate** a
given Workflow Definition — "does this make sense," read by eye. This is
explicitly not about replacing `internal/workflowdef`'s validator (Rules
1–5, doc02) with a rendering-engine's own structural checks; those solve a
different problem (generic graph well-formedness vs. this schema's
domain-specific rules) and don't overlap enough to substitute for each
other.

## v1–v3: layout algorithm research

Three node-link prototypes (`workflow_v1`/`v2`/`v3`), eleven layout
algorithm runs total, comparing how well each clusters a Workflow
Definition's back/forth loops instead of scattering their members across
ranks. Full writeup, screenshots, and ranking already in
04-control-plane-mvp-scope.md's "Graph visualization prototypes, not a
decision yet" section and the linked report. Not repeated here — that
verdict (Cytoscape + `cose-bilkent` compound nodes as lead candidate, ELK
as fallback) stands on its own and doesn't depend on anything below.

## Why BPMN was considered

A follow-up discussion asked whether a from-scratch node-link diagram is
the right representation at all, versus a notation someone can already
read. BPMN (Business Process Model and Notation, an OMG standard) has a
real formal metamodel and a mature tooling ecosystem (bpmn-js/Camunda
Modeler, execution engines), which gives it genuine structural-soundness
checking (unreachable nodes, dangling gateways, missing end events) that a
from-scratch diagram doesn't have for free.

Two different levels of adoption were distinguished up front:

- **BPMN as a rendering/notation convention** — translate the existing
  YAML Workflow Definition into BPMN's visual vocabulary for display only.
  Low commitment: YAML stays the sole source of truth (doc02's decided
  architecture doesn't move), no second stored format.
- **BPMN as an actual format** — adopt BPMN XML as a second or canonical
  representation. High commitment, duplicates/replaces the existing
  schema, not requested.

Decision: pursue the first only. No BPMN XML storage, no BPMN toolchain
(`bpmnlint`, Camunda Modeler) as a validator — `internal/workflowdef`
keeps that job.

### Schema → BPMN element mapping

Established once and reused by both v4 and v5 below, so the comparison
between them isolates "layout + rendering engine" as the only variable:

| Schema concept | BPMN element |
|---|---|
| Role (Conductor/Planner/Coder/Reviewer/Human) | lane (pool) |
| `on:` outcome with 2+ branches | exclusive gateway |
| `on_malformed_output` | boundary event (an interrupting exception, not an expected-outcome route — distinct from an ordinary gateway branch) |
| `GraphCluster` (a real back/forth loop) | sub-process (expanded, not collapsible) |
| `REVIEW_PENDING` | intermediate catch event (a real Temporal signal-wait per doc05, not a true terminal — a Run can resume from it) |
| `COMPLETED` | plain end event |
| `FAILED` | end event with an error event definition |
| `CANCELLED` | end event with a terminate event definition |

A single `on:` outcome (no real branching) gets no gateway — a
one-outgoing-flow gateway is pointless and real BPMN linting would flag it
as such.

## v4: hand-rolled BPMN-styled renderer

`cmd/controlplane/static/viz-v4.js` — SVG drawn by hand (own layering +
lane assignment + edge routing), applying the mapping above. No BPMN
toolchain dependency.

**Verified problems, found by direct review of the rendered output:**

- Several sequence-flow labels sat away from the edge they belonged to,
  reading as disconnected from their line once nearby edges converged
  (e.g. "proceed", "escalate", "malformed_output" near the same area).
- Overlapping edges at convergence points, most visibly around
  `REVIEW_PENDING` and the `coder_response` gateway/boundary-event
  cluster.
- Exclusive gateway diamonds drawn overlapping nearby boxes/labels rather
  than in clear space.
- At least one edge (`execute`'s outgoing flow) had an ambiguous endpoint
  — not possible to tell by eye which box it terminated at.

One iteration (biasing edge labels toward their source instead of the
geometric midpoint, since converging edges share a midpoint/target but
diverge right after their own source) measurably reduced label collisions
but did not resolve the gateway-overlap or ambiguous-endpoint problems,
which are architectural to hand-rolled bezier routing, not a labeling
detail.

## v5: real toolchain (`bpmn-auto-layout` + `bpmn-js`)

`cmd/controlplane/static/viz-v5.js` — the same schema→BPMN mapping, but
positioning handed to `bpmn-auto-layout` (computes DI for semantic-only
BPMN XML) and rendering handed to `bpmn-js` (the same engine Camunda
Modeler uses), both real BPMN tooling. Still no BPMN XML storage — the XML
is generated fresh from the `WorkflowGraph` on every render and discarded,
same as v1–v4's in-memory layouts.

### Setup cost

- `bpmn-js` ships a pre-packaged UMD bundle (`bpmn-navigated-viewer.production.min.js`)
  — drop-in, same vendoring pattern as d3/dagre/Cytoscape.
- `bpmn-auto-layout` does not ship a browser bundle — it's ESM-only and
  imports `bpmn-moddle`/`min-dash` as bare specifiers, which a plain
  `<script>` tag can't resolve without an import map or bundler. Bundled
  locally with `esbuild` (one-time; documented in
  `cmd/controlplane/static/vendor/README.md` for reproducibility).
- `bpmn-js`'s license is MIT plus one binding clause: the bpmn.io
  watermark rendered into the diagram must stay visible, not hidden or
  overlapped. Respected as-is (no CSS override attempted).

### A concrete toolchain limitation found

`bpmn-auto-layout`'s own README claims support for horizontal lanes and
collaboration pools. Tested directly against the installed package
(v1.3.0) two ways — a bare `<bpmn:laneSet>`, and separately a
`<bpmn:collaboration>`/`<bpmn:participant>` wrapper — and neither produced
any DI output for the lane/participant elements; they were silently
dropped from the laid-out result. Worked around by folding Role into each
task's own label (`plan · planner`) instead of a lane band. This is a
real gap in the adopted toolchain, not an integration mistake — the kind
of thing only found by building the integration and checking the output,
not by reading the library's own documentation.

### Verified problems, found by direct review of the rendered output

Real orthogonal sequence-flow routing resolves the ambiguous-endpoint and
gateway-overlap problems v4 had: every edge is a traceable right-angle
line with a visible arrowhead at its actual target, and gateways get
dedicated space rather than sitting on top of a box.

It does not resolve the underlying convergence problem. Where several
distinct edges run parallel into a long straight shared horizontal
segment before diverging again (multiple flows — `malformed_output` from
three different hosts, plus `dispute`/`escalate`/`out_of_scope` from
`coder_response`'s own gateway — merging toward `REVIEW_PENDING`), it
becomes impossible to tell, by eye, which arrow entered the "highway" from
which box or which one is currently connected to which target. Labels in
that same region also overlap (three stacked instances of
"malformed_output"; "out_of_scope" and "dispute" rendering as one fused
"outdisputcope"). This is the same class of problem v4 had, at the same
location in the graph, produced by professional BPMN tooling rather than
hand-rolled routing.

## Verdict

Both approaches produce the same failure mode at the same high-convergence
point in this specific Workflow Definition (multiple distinct edges
sharing a target). Since the real toolchain — orthogonal routing, a
dedicated layout algorithm, no hand-rolled bugs — has it too, this reads
as a property of the graph (several structurally distinct routes
converging on one node) rather than a fixable rendering-quality gap in
either implementation. The experiment does not support adopting
`bpmn-auto-layout` as a solution for a workflow this Workflow
Definition's shape/density — it does not currently produce a
substantially clearer result than the hand-rolled v4 renderer, once past
the parts of the diagram v4 also renders cleanly.

## Decision

Both open questions this spike raised are now resolved and acted on —
`workflow_v1` through `workflow_v5`'s standalone nav entries are gone;
v3 and v4 are kept, accessible from the Workflows view's "View
Cytoscape"/"View BPMN" row toggles instead (opened in a modal, not a
separate page).

**Layout-algorithm research (v1/v2/v3): v3/Cytoscape.js wins**, per the
"Verdict" this doc's earlier section already reached and never revisited
— Cytoscape + `cose-bilkent` (compound) gives structurally-enforced
cluster containment with the least code, pan/zoom/drag built in. v1 and
v2 (and ELK, v2's own fallback candidate) are dropped.

**BPMN-as-notation research (v4/v5): v4/hand-rolled wins.** Revisiting
the open question above (does standard BPMN practice have an
established pattern for a node with many distinct incoming exception/
escalation routes) wasn't necessary to decide this — the real toolchain
in v5 hits the identical convergence problem v4 does at the identical
location in the graph, so adopting it wouldn't have resolved the thing
that would have justified adopting it. Given that, v5's own added costs
tip the decision: real gaps found live (lanes silently dropped by
`bpmn-auto-layout`, a mandatory bpmn.io watermark, heavier vendoring —
an ESM-only package hand-bundled with `esbuild` rather than a drop-in
script tag) with no offsetting benefit over v4 on the actual open
problem. v4 — simpler, same real limitation, no extra baggage — is kept;
v5 is dropped. The convergence-routing problem itself is unresolved by
either approach and stays that way; it reads as a property of a
Workflow Definition with this shape (several structurally distinct
routes converging on one node) rather than a gap either renderer can
fix alone. Worth reopening only alongside real BPMN-notation research
(the original open question above), not as a rendering-engine swap.
