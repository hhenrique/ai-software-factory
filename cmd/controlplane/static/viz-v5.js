// Workflow visualization prototype v5: real BPMN toolchain comparison
// against v4's hand-rolled renderer, requested directly after v4 showed
// real legibility problems (overlapping edges, gateways drawn on top of
// labels/boxes, arrows disappearing behind nodes) — the concern being
// whether those are fixable with more effort or symptomatic of rolling a
// layout engine from scratch. This uses the same semantic mapping v4
// does (vizV4Lanes, vizV4ShapeKind — shared, not reimplemented, so the
// comparison isolates "layout + rendering engine" as the only variable)
// but hands the actual positioning and drawing to real BPMN tooling:
//
//   - bpmn-auto-layout (github.com/bpmn-io/bpmn-auto-layout) computes
//     DI (position/routing) for semantic-only BPMN XML we generate here.
//   - bpmn-js (github.com/bpmn-io/bpmn-js), the same rendering engine
//     Camunda Modeler itself uses, draws the laid-out XML.
//
// Still not adopting BPMN as a stored format: the XML built below is
// generated fresh from the already-parsed WorkflowGraph on every render
// and thrown away, same as v1-v4's in-memory layouts — YAML stays the
// only source of truth.
//
// Known scope-limits vs v4:
//   - No lanes. bpmn-auto-layout@1.3.0's own size-lookup table has a case
//     for bpmn:Lane and bpmn:Participant, but neither ever gets a DI
//     shape in its actual output — confirmed directly against the
//     installed package (a bare <bpmn:laneSet>, and separately a
//     <bpmn:collaboration>/<bpmn:participant> wrapper, both tested) —
//     the "horizontal lanes and collaboration pools" claim in its own
//     README doesn't hold up in this version. Rather than ship a lane
//     band that silently never renders, Role is folded into each agent
//     task's own label instead ("plan · Planner").
//   - No sub-process wrapping for loop clusters. A BPMN sub-process is
//     normally owned by a single lane; this workflow's loop spans
//     Conductor/Coder/Reviewer at once, which doesn't have a clean
//     single-lane encoding regardless of the lane issue above. Real BPMN
//     tooling would represent this loop the same way this prototype
//     does — as ordinary backward sequence flows.

let vizV5ActiveViewer = null;

function renderWorkflowV5(container) {
  buildGraphViewShell(container, {
    title: "workflow_v5 — bpmn-js + bpmn-auto-layout",
    interactionHint: "drag to pan, scroll to zoom — real BPMN toolchain, not a hand-rolled renderer",
    layouts: [
      { id: "bpmn-real", label: "bpmn-auto-layout + bpmn-js", render: (canvas, graph) => vizV5Render(canvas, graph) },
    ],
  });
}

function vizV5Escape(s) {
  return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

// ---- semantic BPMN XML generation (no DI — bpmn-auto-layout computes that) ----

function vizV5BuildBpmnXML(graph) {
  const outcomeEdgesByFrom = {};
  for (const e of graph.edges) {
    if (e.label && e.label !== "malformed_output") {
      (outcomeEdgesByFrom[e.from] = outcomeEdgesByFrom[e.from] || []).push(e);
    }
  }
  const gatewayFor = {};
  for (const [from, edges] of Object.entries(outcomeEdgesByFrom)) {
    if (edges.length >= 2) gatewayFor[from] = "Gateway_" + from;
  }

  const boundaryEvents = [];
  for (const e of graph.edges) {
    if (e.label !== "malformed_output") continue;
    boundaryEvents.push({ id: "Boundary_" + e.from, hostId: e.from, to: e.to, label: e.label });
  }

  const entry = graph.nodes.find((n) => n.kind !== "terminal") || graph.nodes[0];
  const startID = "StartEvent_1";

  let flowCounter = 0;
  const flows = [];
  function addFlow(from, to, label) {
    flowCounter++;
    flows.push({ id: "Flow_" + flowCounter, from, to, label });
  }
  addFlow(startID, entry.id, "");
  for (const e of graph.edges) {
    if (e.label === "malformed_output") continue;
    const gwID = gatewayFor[e.from];
    const from = gwID && e.label ? gwID : e.from;
    addFlow(from, e.to, e.label || "");
  }
  for (const [from, gwID] of Object.entries(gatewayFor)) addFlow(from, gwID, "");
  for (const be of boundaryEvents) addFlow(be.id, be.to, be.label);

  // flow node XML, one element per graph node + synthesized gateways —
  // boundary events are emitted separately below since they nest inside
  // their host's incoming/outgoing differently (attachedToRef, no
  // incoming flow of their own).
  const nodeXML = [];

  for (const n of graph.nodes) {
    const shape = vizV4ShapeKind(n);
    const incoming = flows.filter((f) => f.to === n.id).map((f) => `<bpmn:incoming>${f.id}</bpmn:incoming>`).join("");
    const outgoing = flows.filter((f) => f.from === n.id).map((f) => `<bpmn:outgoing>${f.id}</bpmn:outgoing>`).join("");
    // Lanes don't render (see file header) — fold Role into the task's
    // own label instead of losing "who owns this step" entirely.
    const name = vizV5Escape(n.role ? `${n.id} · ${n.role}` : n.id);
    if (shape === "task") {
      nodeXML.push(`<bpmn:task id="${n.id}" name="${name}">${incoming}${outgoing}</bpmn:task>`);
    } else if (shape === "end") {
      nodeXML.push(`<bpmn:endEvent id="${n.id}" name="${name}">${incoming}</bpmn:endEvent>`);
    } else if (shape === "end-error") {
      nodeXML.push(`<bpmn:endEvent id="${n.id}" name="${name}">${incoming}<bpmn:errorEventDefinition/></bpmn:endEvent>`);
    } else if (shape === "end-terminate") {
      nodeXML.push(`<bpmn:endEvent id="${n.id}" name="${name}">${incoming}<bpmn:terminateEventDefinition/></bpmn:endEvent>`);
    } else if (shape === "intermediate") {
      nodeXML.push(`<bpmn:intermediateCatchEvent id="${n.id}" name="${name}">${incoming}${outgoing}<bpmn:signalEventDefinition/></bpmn:intermediateCatchEvent>`);
    }
  }
  for (const [from, gwID] of Object.entries(gatewayFor)) {
    const incoming = flows.filter((f) => f.to === gwID).map((f) => `<bpmn:incoming>${f.id}</bpmn:incoming>`).join("");
    const outgoing = flows.filter((f) => f.from === gwID).map((f) => `<bpmn:outgoing>${f.id}</bpmn:outgoing>`).join("");
    nodeXML.push(`<bpmn:exclusiveGateway id="${gwID}">${incoming}${outgoing}</bpmn:exclusiveGateway>`);
  }
  for (const be of boundaryEvents) {
    const outgoing = flows.filter((f) => f.from === be.id).map((f) => `<bpmn:outgoing>${f.id}</bpmn:outgoing>`).join("");
    nodeXML.push(
      `<bpmn:boundaryEvent id="${be.id}" attachedToRef="${be.hostId}" name="${vizV5Escape(be.label)}">` +
      `${outgoing}<bpmn:errorEventDefinition/></bpmn:boundaryEvent>`,
    );
  }

  nodeXML.unshift(`<bpmn:startEvent id="${startID}" name="Task created"><bpmn:outgoing>Flow_1</bpmn:outgoing></bpmn:startEvent>`);

  const flowXML = flows.map((f) =>
    `<bpmn:sequenceFlow id="${f.id}" sourceRef="${f.from}" targetRef="${f.to}"` +
    (f.label ? ` name="${vizV5Escape(f.label)}"` : "") + `/>`,
  ).join("");

  return (
    `<?xml version="1.0" encoding="UTF-8"?>` +
    `<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL" ` +
    `id="Definitions_1" targetNamespace="http://factory.local/bpmn">` +
    `<bpmn:process id="Process_1" isExecutable="false">` +
    nodeXML.join("") + flowXML +
    `</bpmn:process>` +
    `</bpmn:definitions>`
  );
}

// ---- render: bpmn-auto-layout for DI, bpmn-js to draw it ----

async function vizV5Render(canvas, graph) {
  canvas.innerHTML = "";
  if (vizV5ActiveViewer) {
    vizV5ActiveViewer.destroy();
    vizV5ActiveViewer = null;
  }
  // bpmn-js's default theme (sequence-flow labels especially) is tuned
  // for a white canvas — forcing it onto this app's dark canvas leaves
  // low-contrast gray-on-near-black text. A light panel here is a fairer
  // comparison than fighting bpmn-js's own theme with CSS overrides.
  canvas.style.background = "#fff";
  canvas.style.borderRadius = "8px";

  const xml = vizV5BuildBpmnXML(graph);
  // layoutProcess resolves to the laid-out XML directly (a plain string),
  // not {xml, warnings} — double-checked against the installed package's
  // own source after the first pass got this wrong from a stale summary.
  const laidOutXML = await BpmnAutoLayout.layoutProcess(xml);

  const viewer = new BpmnJS({ container: canvas });
  vizV5ActiveViewer = viewer;
  await viewer.importXML(laidOutXML);
  viewer.get("canvas").zoom("fit-viewport");
}
