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
| issue-to-pr-standard | planner | claude-code / sonnet-5-medium |
| issue-to-pr-standard | coder | codex / chatgpt-sol |
| issue-to-pr-standard | reviewer | copilot / auto |

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
   `context:`) and edits files directly, the same way a human would. The
   conductor never parses a diff out of the harness's own text output —
   it computes the diff itself deterministically (stage + diff the
   worktree) after the harness call returns. That's what "the conductor
   can apply" means in practice: one uniform diff-computation path across
   every harness, rather than trusting each harness's self-reported patch
   format.

5. **Enforcing read-only access where a role requires it.** Planner also
   declares `worktree_path` now (01-run-state-machine.md: it drafts a
   real plan against the real repository) but must never edit anything —
   a plan is reviewed as a plan, not as a change already applied.
   Reviewer still never touches the worktree at all (it judges an
   already-produced diff).

   Each adapter translates a read-only invocation into its own harness's
   mechanism: Claude Code's `--permission-mode plan`, Codex's `--sandbox
   read-only`, Copilot's explicit `--deny-tool write --deny-tool shell`
   (Copilot has no real sandboxed read-only mode — this is a denylist,
   only as complete as the tool names enumerated). Necessary, not
   sufficient: a harness's own claim of read-only isn't something the
   factory verifies at the source, and a denylist-style implementation
   can miss a write-capable tool nobody thought to name. So this is
   backed by the same principle as item 4 above applied to the read
   path — don't trust a harness's self-report, verify mechanically: a
   deterministic `git status --porcelain` check against the worktree
   after every read-only invocation returns, regardless of harness or
   which flag it was given. Any unexpected write resets the worktree
   (so it never leaks into what a later Coder-role call sees) and is
   recorded as a violation rather than silently discarded or silently
   let through — same spirit as "a harness adapter that can't reliably
   produce X for its role shouldn't be used for that role," applied here
   to read-only-ness specifically.

## Required structured outputs by step type

These schemas are the contract between the Workflow definition, the
harness adapter, and the state machine. A harness adapter that cannot
reliably produce one of these for its role should not be used for that
role.

**Planner:**
```
verdict: proceed | reject | escalate
assessment: string       # what the Planner read, what it's proposing, why
scope_contract:          # required when verdict = proceed
  acceptance_criteria: [...]
  in_scope_paths: [...]
  non_goals: [...]
```
`assessment` is required here, not deferred to the narrative-content
schema below like the other roles' equivalent — 01-run-state-machine.md's
mandatory plan-approval gate means a human reads this to decide whether
to approve, not just a tracker mirror later, so it's part of the routing
contract itself.

**Coder (initial execution, and any REVISING pass — a failing test or a
review finding):** a patch/diff in the adapter's normalized format, plus
```
change_summary: string   # what this changed and why, covering the diff as a whole
```
No verdict schema required for these passes — `change_summary` is
additive, populated from the same call that produces the diff (doc01's
Rule 1: not a second call re-reading the diff to describe it
afterward). Unlike Planner's `assessment`, this isn't itself a routing
field — a step with it still just declares `next:`, never `on:` keyed
off it.

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
assessment: string   # overall reasoning behind the review as a whole, not any one finding
```

## Near future: structured tracking content per role (not built yet)

The schemas above are the *routing* contract — the minimum a role must
produce for the state machine to decide where to go next (a verdict
enum, a diff). They are deliberately terse: `dispute` needs reasoning
text, not a paragraph the router has to parse.

Separately, and not yet built: each remaining role's output should also
carry structured *narrative* content, meant for a human reading the
Run's external trace (01-run-state-machine.md's mirrored-transitions
note), not for routing. Framing, not a finalized schema:

- **Coder:** root-cause analysis (when responding to a review finding or
  a failing test) and how to test the change are still deferred — "what
  actually changed and why" is no longer: see `change_summary` in the
  required-schema section above.
- **Reviewer:** similar in spirit — assessment behind each finding, not
  just the finding itself.

Planner's version of this (assessment, plan/slices, impact analysis) and
Coder's (`change_summary`) are no longer deferred — see the
required-schema section above. For Planner, the mandatory plan-approval
gate is what moved it: a human decides off this content directly, so it
can't wait for the tracking mirror. For Coder, the trigger was narrower
(01-run-state-machine.md, 04-control-plane-mvp-scope.md: a bare diff
line-count in an opened PR's description said nothing about what
actually changed) but the shape is the same — reuse the call that's
already happening rather than adding a second one to summarize its own
output after the fact.

What's still deferred for the Reviewer is finer-grained than what's
built: `assessment` (overall reasoning, already in the routing schema
above) exists; per-finding rationale — *why* each specific finding was
raised, not just its description — doesn't yet. Both this and Coder's
still-deferred root-cause-analysis/how-to-test content are additive to
the routing schemas above, not a replacement — a harness adapter would
populate both from the same call, same as today's `findings`/`verdict`
split. Deferred until the tracking mirror itself
(08-tracking-integration.md, design decided, not yet built) actually
exists to consume it — that doc's v1 content uses only the routing
schemas already defined above; this narrative-content schema is its
explicitly separable v2.

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
