# AGENTS.md

Guidance for any coding agent (or human) working in this repository —
tool-agnostic on purpose: `docs/03-roles-and-harness-contract.md`
already establishes that this project itself is operated on by multiple
harnesses (Claude Code, Codex, Copilot), so the project's own guidance
shouldn't be locked to one of them either. `CLAUDE.md` points here.

## Repository state

Real and running, not docs-only: a Go backend (`cmd/`, `internal/`),
a vanilla-JS control-plane SPA, real Workflow Definitions
(`workflows/`), a Docker Compose deploy for Temporal + Postgres
(`deploy/`), and a Go test suite. See [`README.md`](README.md) for
setup, build/run/test commands, and an env-var reference — this file
doesn't repeat those.

Before writing any implementation code, read `docs/00` through `docs/08`
in order; they're marked "Status: decided" and constitute an approved
spec, not a draft to redesign. Each doc lists its `Depends on` /
`Consumed by` relationships at the top — follow those links rather than
reading files in isolation.

`.sketchpad/` is a gitignored working area for planning notes, findings,
and other output that matters to *building* the factory but isn't part
of the factory itself. Nothing in there ships — keep it out of `docs/`
and out of the source tree.

## What this project is

An internal control plane for conducting a fleet of coding agents against
a set of repositories (the "factory"): define versioned Workflow DAGs, run
them concurrently across repos as Runs, observe agent activity, and gate on
human review where needed. Single internal tool, one team, one VM — not a
general-purpose SaaS product.

## Cardinal rules (apply to every design/implementation decision)

1. **The factory itself should use as few tokens as possible.**
   - Prefer deterministic computation over an LLM call whenever the outcome
     has one correct mechanical answer.
   - Track token spend per Run and per step type as a first-class metric.
   - Never make an agent re-derive information a prior step already
     computed (pass deltas/diffs, not full transcripts).
   - Bound every loop with both an attempt cap *and* a token budget —
     attempt count alone doesn't limit tokens burned per attempt.
   - Detect non-convergence (oscillation, repeated identical findings) and
     fail fast instead of exhausting the budget passively.

2. **Tools before agents.** An LLM call is justified only for a step whose
   input/output requires judgment about code or intent. Everything else
   (git/worktree ops, PR creation, running tests/lint/build and parsing
   results, CI polling, diff-size/protected-path/scope checks) is
   deterministic and belongs in code. The test for any new step: "does this
   have exactly one correct mechanical answer, or does it require
   judgment?" One answer → tool. Judgment → agent, and only agent.

## Core entities

- **Repository** — a repo the factory operates on: build/test/lint
  commands, scope-affecting path rules, default Workflow.
- **Task** — a unit of work (target repo, description/acceptance intent,
  source: human / ticket / auto-generated from a review finding).
- **Workflow (definition)** — a versioned DAG of steps, roles, budgets, and
  routing that a Task is executed against. Hand-authored YAML, checked into
  git, reviewed like code — not versioned beyond a single active definition
  + provenance hash in v1 (no visual editor, no A/B variants).
- **Run** — one execution attempt of a Workflow definition against a Task.
  Retries are new Runs, never mutations, so every attempt stays traceable
  and replayable.
- **Role** — a named function a step is responsible for: `planner`,
  `coder`, or `reviewer` — a fixed, closed set (maps 1:1 onto a state in
  the Run state machine), not something a Workflow invents. A step
  references a role by name only, never a harness/model directly.
- **Worker** — the `(harness, model, params)` triad a Role is played by
  (e.g. claude-code + sonnet-5-medium; codex + a coding model; a
  review-focused harness). Persisted, control-plane-CRUD'd
  (`internal/workers`), independent of any Workflow. Which Worker plays
  which Role *for a given Workflow* is a separate mapping
  (`internal/roleassignment`, `(workflow, role) -> worker_id`), edited
  from the Workflows view — not a YAML field, so swapping a Worker is a
  control-plane action, not a Workflow rewrite. Workers are
  deployment-target-agnostic: the backing model may be a frontier API or
  a self-hosted model — this must never leak into the factory's own
  hosting/deployment design.

Naming caution: "Workflow" is overloaded. Use "Workflow Definition" for the
domain YAML DAG and "Temporal Workflow" / "workflow execution" for the
Temporal primitive — keep this distinction in code and docs (see
`docs/05-architecture-temporal.md`). `docs/07-glossary.md` has the full
canonical-term list, including other overloaded short forms (Worker,
Task, Step) — check there before introducing a new name for something.

## Architecture

### Run state machine (`docs/01-run-state-machine.md`)

States: `QUEUED → PROVISIONING → PLANNING → EXECUTING ↔ VERIFYING →
REVIEWING ↔ REVISING → REVIEW_PENDING → CREATE_PR → COMPLETED`, with `FAILED`,
`CANCELLED`, `BLOCKED` as terminal/terminal-adjacent states. Every state is
owned by exactly one of **tool**, **agent**, or **human** — this ownership
must be visible in the Workflow Definition (`type: tool | agent` per step)
so it's auditable against the tools-before-agents rule.

Key mechanics to preserve when touching this area:
- **Every drafted plan pauses for mandatory human approval.** A `proceed`
  verdict out of PLANNING routes to `REVIEW_PENDING` (reason
  `planning_awaiting_approval`), never straight to EXECUTING — a human
  approves (with a justification) or requests changes (with a hint) via
  the control plane's Pending Approvals view. No round cap on this loop,
  deliberately: the human's own attention is the brake, not a counter.
  See `approve_resume` in `docs/02` and "Mandatory plan approval" in
  `docs/01`.
- **PLANNING is single-shot for ambiguity** — never auto-retried; malformed
  structured output is a *distinct* failure mode from ambiguity and routes
  to `REVIEW_PENDING` tagged `planning_malformed_output`, not silently
  retried. The Planner has real, read-only worktree access (enforced both
  at the harness level and by a deterministic post-call git-status check)
  — it drafts a real plan against the real repo, not a guess from the task
  description alone.
- **Verify loop** (`EXECUTING ↔ VERIFYING`) is allowed to repeat because a
  failing test is new information; `REVISING` gets only the structured
  failure diff, never the full context again. Includes an oscillation
  check (failing-test-set not shrinking → fail fast) and a flakiness guard
  (deterministic re-run in isolation before routing to an agent).
- **Review loop** (`REVIEWING ↔ REVISING`) — Coder and Reviewer are
  independently configured Roles. Any code change made in response to a
  finding must re-enter `VERIFYING` before going back to `REVIEWING`.
  Verify-rounds and review-rounds are tracked as two independent budgets.
  The Coder's verdict on a finding is structured:
  `address | dispute | escalate | out_of_scope`. `out_of_scope` findings
  spawn a new backlog Task (`auto-generated: review-finding`) rather than
  being discarded, and that classification is final for the Run (not
  contestable). A repeated identical finding + dispute pair is a deadlock
  — escalate to `REVIEW_PENDING` before the round cap, not after.
- **Scope contract** (from PLANNING: `acceptance_criteria`,
  `in_scope_paths`, `non_goals`) is handed to both Coder and Reviewer —
  this is what prevents Reviewer gold-plating loops.
- **Budget reset on human-resumed Runs**: when a human sends a Run back
  from `REVIEW_PENDING` with a hint, *all* of the Run's budget counters
  (verify_rounds and review_rounds) reset to zero-spent, not just the one
  tied to whichever loop escalated. Deliberate trade-off — a human's
  attention on each reset is what keeps this from being a free retry.
- Every state transition emits a structured event (Task ID, Run ID,
  from/to-state, timestamp, token delta, tool-calls-made), including Runs
  that fail before any model call — and is best-effort mirrored onto
  the Run's PR and/or the Task's source (a GitHub issue), curated to
  agent-step results and terminal states only (`docs/08`).

### Workflow Definition schema (`docs/02-workflow-definition-schema.md`)

YAML with `roles`, `budgets`, optional `trigger` (an Automation is just a
`trigger` field, not a separate concept), and `steps` (each `type: tool`
with a deterministic `action`, or `type: agent` with a `role`, `context`
fields pulled from Run context, and optionally `output_schema` +
`on_malformed_output`). The conductor must statically validate a
definition before it can be saved/activated:
1. No unbounded cycles — a cycle is only legal if some step in it has a
   `budget`.
2. Every `agent` step's `role` exists in `roles`.
3. Every `agent` step with `output_schema` declares `on_malformed_output`.
4. Every `on:` conditional's outcomes are all mapped.
5. `context` fields are producible by the graph at that point.

Reference definitions: `issue-to-pr-standard` (full plan/execute/verify/
review/merge cycle, `internal/workflowdef/fixtures`) and
`dependency-bump-minimal` (skips scope contract and the whole
Coder/Reviewer loop) — used as the shape to match for new Workflow
Definitions. `workflows/issue-to-pr.yaml` is the real, deployable
counterpart of `issue-to-pr-standard`.

### Roles & harness contract (`docs/03-roles-and-harness-contract.md`)

A harness adapter is the only place harness-specific prompt construction
may live — never in the conductor or the Workflow Definition. Each
adapter must: normalize input from declared `context` fields, normalize
output into the step's `output_schema` (unparseable output →
`malformed_output`, routed per `on_malformed_output`, never silently
retried), normalize token accounting into one consistent count/estimate
(never let an under-reporting harness silently exceed budgets others are
held to), and produce patches/diffs in one standard format regardless of
the harness's internal edit representation. Required output schemas per
role (Planner, Coder, Reviewer) are enumerated in that doc — a harness that
can't reliably produce the schema for a role shouldn't be used for that
role. Adding a harness never requires touching the state machine or
Workflow schema — that's the point of the role indirection. Three real
harness adapters exist today: Claude Code, Codex, and Copilot
(`internal/activities/harness`).

### Execution engine (`docs/05-architecture-temporal.md`)

Built on Temporal, self-hosted on the same VM via Docker Compose, backed by
a single shared Postgres instance — Temporal's own persistence and the
control plane's projection store each get their own database within that
instance, not a shared schema. Mapping: Run → Temporal workflow execution;
`type: tool`/`type: agent` steps → Activities (agent steps just call a
harness adapter from inside the Activity, same execution primitive as tool
steps); `REVIEW_PENDING`/`BLOCKED` → signal-wait, preferred over polling
where the external system can push a webhook. Temporal's native retry
policy covers attempt-count budgets only — token budgets and
oscillation/deadlock detection are custom logic that must run inside the
Activity or as a check before each retry. The control plane must not query
Temporal's raw workflow history directly for every metric — project into
the projection store instead.

### Control plane (`docs/04-control-plane-mvp-scope.md`)

The control plane is a thin window onto the state machine/budgets/role
contracts, not the product itself — `cmd/controlplane`. Built sections:
Repositories, Workflows (list + validation status + YAML/graph viewers),
Workers + role assignment, Tasks (backlog, submission), Inbox
(exceptions: escalations, malformed output, budget/harness-limit
exhaustion, disputed findings), Pending Approvals (the routine
plan-approval queue, split from Inbox on purpose — see that doc), and
Settings. Overview (fleet-health dashboard) remains unbuilt. Non-negotiable
even for a minimal build: full trace/replay per Run (transcript + tool-call
log, diffable against the merged PR or failure state), and every state
transition logged as a structured event.

## Explicitly deferred (do not build first)

Workflow versioning/A-B comparison, a visual DAG editor, a bespoke
scheduling engine, Automations as a separate surface, custom RBAC,
per-model cost dashboards, per-repo custom indexing/RAG config surface. See
`docs/00-vision-and-principles.md` for the full list and rationale.

## Working in this repo

- `go build ./...`, `go vet ./...`, and `go test ./...` before considering
  any Go change done — see `README.md` for the full local dev setup
  (Docker Compose for Temporal/Postgres, building/running `cmd/worker`
  and `cmd/controlplane`).
- `cmd/controlplane`'s static assets (`cmd/controlplane/static/`) are
  Go-embedded — a frontend-only change still needs a Go rebuild
  (`go build -o bin/controlplane ./cmd/controlplane`) and a process
  restart to take effect, not just a browser refresh.
- Keep `docs/00`–`08` in sync with behavior changes — they're the
  decided spec, not just background reading; a code change that
  contradicts one of them either needs the doc updated in the same
  change or is probably the wrong change.
