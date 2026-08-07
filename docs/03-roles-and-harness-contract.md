# Roles & Harness Contract

Status: decided
Depends on: 00-vision-and-principles.md
Consumed by: 02-workflow-definition-schema.md (`roles:` list),
05-architecture-temporal.md (adapter boundary), 04-control-plane-mvp-scope.md
(Workers section: internal/workers, internal/roleassignment),
08-tracking-integration.md (mirrors each role's structured output)

## Definitions

- **Role** — a named function within a Workflow: `planner`, `coder`, or
  `reviewer` — a fixed, closed set (internal/workflowdef.KnownRoles), not
  something a Workflow or a human invents freely. Fixed because each maps
  1:1 onto a Planner/Coder/Reviewer state in 01-run-state-machine.md;
  adding a role means adding a state, a bigger change than config. A
  Workflow's `roles:` list just names which of these it uses; a step
  references a role by name, never a harness or model directly.
- **Harness** — the agentic tool/CLI/interface that actually executes a
  step (e.g. Claude in plan mode, Codex, a review-focused CLI). Harnesses
  differ in invocation, context format, and how they report token usage.
- **Model** — the backing model a harness is configured to use for a
  given invocation. May be a frontier API model or a self-hosted local
  model. The factory does not distinguish between these at the
  conductor level — see "Deployment independence" below.
- **Worker** — the persisted `(harness, model, params)` triad
  (internal/workers), independent of any Workflow — a CRUD entity
  maintained in the control plane's Workers view, not YAML. A Worker has
  its own identity (a name, an id): two Workers configured identically
  are still two distinct rows, and editing one immediately affects every
  role assignment currently pointing at it.

A Role is *played by* a Worker, per Workflow — that assignment
(internal/roleassignment, `(workflow, role) -> worker_id`) is what used to
be the inline `roles:` config in 02-workflow-definition-schema.md, and is
now a separate, database-backed mapping edited from the Workflows view.
Example of a current assignment set:

| Workflow | Role | Worker (harness / model) |
|---|---|---|
| issue-to-pr-standard | planner | claude-plan / sonnet-5-medium |
| issue-to-pr-standard | coder | codex / chatgpt-sol |
| issue-to-pr-standard | reviewer | copilot-cli / auto |

Changing a role's backing Worker (or a Worker's own harness/model) is a
control-plane action, not a Workflow edit, because steps reference the
role name only — the assignment is resolved once per Run at submission
time (internal/roleassignment.Resolve, called from internal/taskintake,
*before* `conductor.RunWorkflow` starts — see "Deployment independence"
below and 05-architecture-temporal.md's determinism requirement) and
takes effect on the next Run submitted, not retroactively on one already
running. A role declared in `roles:` with no current Worker assignment is
a hard failure at submission time, not a silent default.

## Deployment independence

The factory (control plane + conductor) runs as a service on
a VM. It has no dependency on where any Worker's model is hosted. A
Worker's `harness`/`model` config resolves to an endpoint at invocation
time:

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

   Concretely: a Coder-role harness is given real file access to the
   Run's worktree (this is *why* these steps declare `worktree_path` in
   `context:`, unlike Planner/Reviewer, which judge a task description or
   an already-produced diff and never touch the worktree) and edits files
   directly, the same way a human would. The conductor never parses a
   diff out of the harness's own text output — it computes the diff
   itself deterministically (stage + diff the worktree) after the harness
   call returns. That's what "the conductor can apply" means in practice:
   one uniform diff-computation path across every harness, rather than
   trusting each harness's self-reported patch format.

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

## Near future: structured tracking content per role (not built yet)

The schemas above are the *routing* contract — the minimum a role must
produce for the state machine to decide where to go next (a verdict
enum, a diff). They are deliberately terse: `dispute` needs reasoning
text, not a paragraph the router has to parse.

Separately, and not yet built: each role's output should also carry
structured *narrative* content, meant for a human reading the Run's
external trace (01-run-state-machine.md's mirrored-transitions note),
not for routing. Framing, not a finalized schema:

- **Planner:** assessment of the task, the plan/slices it's broken into,
  impact analysis (what areas of the codebase/system this touches).
- **Coder:** assessment of the change, root-cause analysis (when
  responding to a review finding or a failing test), what actually
  changed and why, how to test it.
- **Reviewer:** similar in spirit — assessment behind each finding, not
  just the finding itself.

This is additive to the routing schemas above, not a replacement — a
harness adapter would populate both from the same call, same as today's
`findings`/`verdict` split for the Reviewer. Deferred until the tracking
mirror itself (08-tracking-integration.md, design decided, not yet
built) actually exists to consume it — that doc's v1 content uses only
the routing schemas already defined above; this narrative-content
schema is its explicitly separable v2.

## Adding a new harness

1. Implement the adapter contract above (input normalization, output
   normalization to the relevant schema, token accounting).
2. Register it under a harness identifier — usable immediately when
   creating or editing a Worker in the control plane's Workers view, no
   Workflow Definition change needed.
3. No changes to the state machine or Workflow schema are required —
   this is the point of the role indirection.

## Adding a new role

Unlike a harness, this is not a lightweight config change: a role maps
1:1 onto a state in 01-run-state-machine.md (Planner -> PLANNING, Coder ->
EXECUTING/REVISING, Reviewer -> REVIEWING), so a new role needs a new
state and new routing through the state machine first. Only once that
exists does it become a name added to internal/workflowdef.KnownRoles,
usable in a Workflow's `roles:` list and assignable a Worker like any
other role.
