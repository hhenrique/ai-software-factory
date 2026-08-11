# Workflow Definition Schema

Status: decided (schema shape); example is illustrative, adjust field
names to match implementation conventions
Depends on: 00-vision-and-principles.md, 01-run-state-machine.md
Consumed by: 05-architecture-temporal.md (each step maps to a Temporal
activity or a signal-wait)

## Purpose

A Workflow is the DAG definition that a Task is executed against to
produce a Run. It is authored as data (YAML), not code, so that:

- the tool-vs-agent distinction (Rule 2) is structurally visible and
  auditable per step, not buried in a prompt
- simpler work (e.g. a dependency bump) can use a shorter DAG without
  forcing the full plan/execute/verify/review cycle
- the definition can be statically validated before a Run starts
  (e.g. every cycle in the graph has a budget attached — no unbounded
  loops)
- swapping a Role's backing harness/model is a control-plane action
  (internal/roleassignment), not a Workflow edit at all — see "Roles vs.
  Workers" below

## MVP scope decision

Workflows are **not versioned** in v1. There is a single active
definition per Workflow, edited in place, checked into git (reviewed
like code — no visual DAG editor). Provenance is a hash of the YAML
recorded on the Run at start time, which is enough traceability without
building a versioning system nobody yet needs. Revisit only if real
evidence emerges that concurrent workflow variants are needed.

An Automation is not a separate concept in v1 — it is a `trigger` field
on a Workflow (e.g. `on_event: ci_failure`, or a cron expression),
handled the same way a human-created Task is: it produces a Task, which
starts a Run under this Workflow.

## Schema

```yaml
workflow: <string, unique id>
version: 1                      # static for MVP; see scope decision above

roles: [<role_name>, ...]       # which of the fixed role set (see 03) this
                                 # Workflow uses — no harness/model/params
                                 # here; see "Roles vs. Workers" below

budgets:
  <budget_name>:
    max_attempts: <int>         # optional
    max_rounds: <int>           # optional
    max_tokens: <int>           # optional

trigger:                        # optional; absence means manually/task-queue-driven only
  on_event: <string>            # e.g. ci_failure, dependency_pr_opened
  schedule: <cron expression>   # optional, for scheduled Runs

steps:
  - id: <string, unique within workflow>
    type: tool | agent
    role: <role_name>           # required if type: agent
    action: <string>            # required if type: tool — deterministic action identifier
    context: [<field names pulled from Run context>]   # agent steps only
    output_schema: { ... }      # agent steps only, when structured verdict required
    budget: <budget_name>       # required if this step is part of a bounded loop
    next: <step id>             # for steps with a single unconditional successor
    on:                         # for steps with conditional routing
      <outcome>: <step id | STATE | { action: ..., next: <step id> }>
    on_malformed_output: <step id | STATE>   # agent steps with output_schema
    approve_resume: <step id>   # only meaningful on a step whose on: routes an
                                 # outcome to REVIEW_PENDING as a mandatory
                                 # approval gate (01's "Mandatory plan approval")
                                 # — names where an *approval* resumes to, since
                                 # REVIEW_PENDING itself no longer carries a
                                 # normal-path destination. A request-changes
                                 # resume doesn't need this: it always goes back
                                 # to the step's own id.
```

Terminal targets (`FAILED`, `COMPLETED`, `REVIEW_PENDING`, `CANCELLED`)
reference the states defined in 01-run-state-machine.md, not step ids.

## Roles vs. Workers

`roles:` only declares which of the fixed role names (`planner`, `coder`,
`reviewer` — see 03-roles-and-harness-contract.md) this Workflow uses; a
step's `role:` field is validated against it exactly as before. The
harness/model/params triad that used to live inline here is now a Worker
(internal/workers), a persisted, control-plane-CRUD'd entity independent
of any Workflow, and a separate mapping (internal/roleassignment) decides
which Worker currently plays which role *for this Workflow* — edited from
the Workflows view, takes effect on the next Run, no YAML edit or
re-review needed. This is resolved once per Run at submission time
(before `conductor.RunWorkflow` starts — see 05-architecture-temporal.md's
determinism requirement) and is a hard failure if any declared role has
no current assignment.

## Validation rules the conductor must enforce before saving a
## Workflow definition

1. The graph is acyclic **except** where a cycle has a `budget`
   attached to at least one step in the cycle. An unbounded cycle is a
   hard validation error, not a runtime risk to discover later.
2. Every `type: agent` step declares a `role` that exists in `roles`,
   and every name in `roles` is itself one of the fixed role names (see
   "Roles vs. Workers" above).
3. Every `type: agent` step with an `output_schema` declares
   `on_malformed_output` — do not let malformed-output handling default
   silently.
4. Every step reachable via `on:` conditional routing has all of its
   declared outcomes mapped (no dangling outcome with no `next`).
5. `context` fields referenced by an agent step must be producible by
   the graph at that point (i.e. either part of the immutable Run
   context set in an earlier step's output, or a Run-level input).

## Reference example: issue-to-pr-standard

This is the "non-trivial change" Workflow the state machine in
01-run-state-machine.md was designed against. Simpler Workflows (e.g.
`dependency-bump-minimal`) should use a strict subset of these steps —
see the note at the end.

```yaml
workflow: issue-to-pr-standard
version: 1

roles: [planner, coder, reviewer]

budgets:
  verify_rounds: { max_attempts: 3 }
  review_rounds: { max_rounds: 4, max_tokens: 60000 }

steps:
  - id: provision
    type: tool
    action: worktree.create
    next: plan

  - id: plan
    type: agent
    role: planner
    context: [task_description, worktree_path]
    output_schema: { verdict: [proceed, reject, escalate], assessment: string, scope_contract: object }
    approve_resume: execute   # see 01's "Mandatory plan approval" — where an approved plan resumes
    on:
      proceed:  REVIEW_PENDING   # mandatory approval gate, not a direct route to execute
      reject:   FAILED
      escalate: REVIEW_PENDING
    on_malformed_output: REVIEW_PENDING

  - id: execute
    type: agent
    role: coder
    context: [scope_contract]
    next: verify

  - id: verify
    type: tool
    action: run.tests_lint_build
    budget: verify_rounds
    on:
      pass: review
      fail: revise_verify
      budget_exhausted: FAILED

  - id: revise_verify
    type: agent
    role: coder
    context: [scope_contract, failing_tests_diff]
    next: verify

  - id: review
    type: agent
    role: reviewer
    context: [scope_contract, diff]
    budget: review_rounds
    output_schema: { findings: array }
    on:
      approved:         create_pr
      changes_required: coder_response
      budget_exhausted: REVIEW_PENDING

  - id: coder_response
    type: agent
    role: coder
    context: [scope_contract, findings, conversation_open_items]
    output_schema: { verdict: [address, dispute, escalate, out_of_scope] }
    on:
      address:      revise_review
      dispute:      review
      escalate:     REVIEW_PENDING
      out_of_scope: { action: task.create(source=review-finding), next: review }

  - id: revise_review
    type: agent
    role: coder
    next: verify

  - id: create_pr
    type: tool
    action: pr.create_and_link
    next: COMPLETED
```

## Building a minimal Workflow variant

A trivial change (docs-only fix, dependency bump) should skip the scope
contract and the entire Coder/Reviewer loop:

```yaml
workflow: dependency-bump-minimal
version: 1

roles: [coder]

budgets:
  verify_rounds: { max_attempts: 2 }

steps:
  - id: provision
    type: tool
    action: worktree.create
    next: execute

  - id: execute
    type: agent
    role: coder
    next: verify

  - id: verify
    type: tool
    action: run.tests_lint_build
    budget: verify_rounds
    on:
      pass: create_pr
      fail: revise_verify
      budget_exhausted: FAILED

  - id: revise_verify
    type: agent
    role: coder
    context: [failing_tests_diff]
    next: verify

  - id: create_pr
    type: tool
    action: pr.create_and_link
    next: COMPLETED
```

Note the visible proxy metric this gives Overview for free: the ratio of
`type: agent` steps to total steps per Workflow. A Workflow that skews
heavily toward `agent` steps for work that should be mechanical is a
signal to re-examine it, per Rule 2.
