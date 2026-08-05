# Roles & Harness Contract

Status: decided
Depends on: 00-vision-and-principles.md
Consumed by: 02-workflow-definition-schema.md (`roles:` block), 05-architecture-temporal.md (adapter boundary)

## Definitions

- **Role** — a named function within a Workflow (Planner, Coder,
  Reviewer, ...). A Workflow references roles by name, never a specific
  harness or model directly.
- **Harness** — the agentic tool/CLI/interface that actually executes a
  step (e.g. Claude in plan mode, Codex, a review-focused CLI). Harnesses
  differ in invocation, context format, and how they report token usage.
- **Model** — the backing model a harness is configured to use for a
  given invocation. May be a frontier API model or a self-hosted local
  model. The factory does not distinguish between these at the
  conductor level — see "Deployment independence" below.

A Role is the pair `(harness, model)`, configured per Workflow (see
`roles:` block in 02-workflow-definition-schema.md). Example:

| Role | Harness | Model |
|---|---|---|
| Planner | claude-plan | sonnet-5-medium |
| Coder | codex | chatgpt-sol |
| Reviewer | copilot-cli | auto |

Changing a Role's backing harness/model is a one-line config edit, not a
Workflow rewrite, because steps reference the role name only.

## Deployment independence

The factory (control plane + conductor) runs as a service on
a VM. It has no dependency on where any Role's model is hosted. A Role's
`harness`/`model` config resolves to an endpoint at invocation time:

- a local network call, if the model is self-hosted
- an outbound API call, if the model is a frontier provider

This must not leak into the factory's own hosting or deployment design.
Do not build any component that assumes a specific model location.

## Harness adapter contract

Every harness needs a thin adapter so the conductor (and the state
machine in 01-run-state-machine.md) can treat all agent steps uniformly,
regardless of which harness produced the output. The adapter is
responsible for:

1. **Normalizing input** — given the step's declared `context` fields
   (from 02-workflow-definition-schema.md), assemble whatever
   prompt/invocation format the harness expects. This is where any
   harness-specific prompt construction lives — it must not leak into
   the conductor or the Workflow definition.

2. **Normalizing output** — parse the harness's raw output into the
   step's declared `output_schema` (see below for the schemas required
   by each agent step type in the reference Workflow). If the harness's
   output cannot be parsed into the schema, this is a
   `malformed_output` result, routed per the step's
   `on_malformed_output` — see 01-run-state-machine.md for why this is
   not auto-retried in the Planning case, and should default to the
   same conservative non-retry pattern elsewhere unless a specific step
   has decided otherwise.

3. **Normalizing token accounting** — harnesses may not report usage the
   same way. The adapter must produce a single consistent token count
   (or best-effort estimate, clearly flagged as such) so budget
   enforcement (Rule 1) is uniform across harnesses. Do not let a
   harness that under-reports usage silently exceed budgets other
   harnesses are held to.

4. **Producing a patch or diff in a uniform representation** — for
   Coder-role harnesses, output must resolve to a standard diff/patch
   format the conductor can apply, regardless of how the harness
   internally represents edits.

## Required structured outputs by step type

These schemas are the contract between the Workflow definition, the
harness adapter, and the state machine. A harness adapter that cannot
reliably produce one of these for its role should not be used for that
role.

**Planner:**
```
verdict: proceed | reject | escalate
scope_contract:          # required when verdict = proceed
  acceptance_criteria: [...]
  in_scope_paths: [...]
  non_goals: [...]
```

**Coder (initial execution):** a patch/diff in the adapter's normalized
format. No verdict schema required for the first EXECUTING pass.

**Coder (responding to a review finding):**
```
verdict: address | dispute | escalate | out_of_scope
```
plus a patch/diff when `verdict: address`, plus reasoning text when
`verdict: dispute` (this text is retained and shown to the Reviewer in
the next round — see 01-run-state-machine.md).

**Reviewer:**
```
findings:
  - description: <string>
    location: <file/lines>
    scope_classification: in_scope | out_of_scope   # tool-checkable when scope is path-based; agent judgment only for ambiguous boundaries
    severity: blocking | advisory
verdict: approved | changes_required
```

## Adding a new role/harness

To add a new harness to the factory:

1. Implement the adapter contract above (input normalization, output
   normalization to the relevant schema, token accounting).
2. Register it under a harness identifier usable in a Workflow's
   `roles:` block.
3. No changes to the state machine or Workflow schema are required —
   this is the point of the role indirection.
