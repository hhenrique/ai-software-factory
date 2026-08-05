# Control Plane — MVP Scope

Status: decided
Depends on: 00-vision-and-principles.md, 01-run-state-machine.md,
02-workflow-definition-schema.md
Purpose: defines what the control plane UI/API must expose for an MVP,
and explicitly what it must not attempt yet.

## Principle

The control plane is a necessity for configuring the factory and
observing it — it is not the product itself. The state machine, budgets,
and role/harness contracts are the core IP; the control plane is a thin
window onto them. Keep it correspondingly thin for MVP.

This is an internal tool for a single small team. Do not build for
multi-tenant, multi-team, or external-user scenarios.

## Sections (MVP)

### Overview

Daily-use fleet health view. MVP fields only:

- Runs currently in flight, by state (from 01-run-state-machine.md)
- Runs blocked on a human (REVIEW_PENDING), with age/SLA-style aging
  indicator
- Success / failure rate over a recent window, overall and per Workflow
- Aggregate token/cost burn over a recent window
- Agent-step token ratio (tokens spent in `type: agent` steps vs.
  `type: tool` steps) — a direct proxy for Rule 2 compliance, per
  Workflow
- Mean time-to-green (Task created → PR merged), overall and per
  Workflow

Explicitly deferred: per-model cost breakdowns, historical trend
charts, anomaly-detection dashboards beyond the oscillation/deadlock
detection already built into the state machine itself. Add these once
there's enough Run volume for them to be meaningful.

### Repositories

Per-repo configuration, minimum required fields:

- Repo identifier / clone URL
- Build / test / lint commands (consumed by the `run.tests_lint_build`
  tool action in VERIFYING)
- `in_scope_paths` defaults / protected paths (consumed by scope
  classification and by PLANNING's scope contract defaults)
- Default Workflow (which Workflow a Task against this repo uses absent
  an override)
- Branching policy reference (how MERGING should behave — direct to
  trunk, PR-required, etc.)

Explicitly deferred: per-repo custom indexing/RAG configuration as a
control-plane-managed setting — if this already exists as separate
tooling, reference it, don't rebuild its config surface here.

### Worktree storage

Where a repo's clone and a Run's worktree live on disk is **not** a
control-plane-managed setting in MVP — it's env/deploy-time
configuration (`FACTORY_ROOT`, defaulting to `/var/lib/factory`), read
once by the worker process at startup. Building this as a
control-plane-editable setting means standing up a real config
store/API/editor, a large effort relative to the need; defer it until
enough of the rest of the control plane exists to justify it. The
implementation sits behind a small `Provider` interface
(`internal/repoconfig`) precisely so a control-plane-backed
implementation can replace the env-backed one later without any caller
changing.

Fixed layout under `FACTORY_ROOT`:
- `repos/<repo>.git` — the repo's own clone, shared across every Run
  against it.
- `worktrees/<repo>/<run-id>` — one isolated `git worktree` checkout per
  Run (keyed by Run id, never Task id — retries are new Runs, never
  mutated ones, so each attempt gets its own directory).

Branch naming: every Run's worktree is checked out on `factory/<run-id>`.
Anchored on Run id, not Task id, because there's no persisted Task entity
yet (see Work, below) — once there is, this can incorporate the Task id
too without changing what calls it.

The shared clone's own `refs/heads/*` is reserved exclusively for these
`factory/<run-id>` branches; the repo's own branches are mirrored into
`refs/remotes/origin/*` via a scoped fetch refspec instead of directly
into `refs/heads/*`. This is load-bearing, not incidental: mirroring
upstream branches into `refs/heads/*` means a routine `fetch --prune`
(needed so a later Run actually sees new upstream commits) deletes any
Run's branch that isn't also on the actual remote — prune can't tell "a
human deleted this upstream" from "this branch only ever existed
locally." Keeping the two namespaces disjoint means `fetch --prune` can
never touch a Run's own branch, no matter how many Runs overlap against
the same repo.

Concurrent access to the shared clone (fetch + worktree add) is
serialized with a per-repo advisory lock (`flock`, not a "check whether a
lock file exists" convention — the latter isn't atomic and leaks on a
crash).

### Work

The Task backlog / intake.

- Task fields: id, target repo, description/acceptance intent, source
  (`human`, `ticket`, `auto-generated:review-finding`), status, assigned
  Workflow, priority
- Must support creating a Task programmatically (from the out-of-scope
  finding path in 01-run-state-machine.md) as well as manually
- A view filtered to `source = auto-generated:review-finding` is useful
  for triage and for spotting repos/areas generating a lot of them (a
  signal that scope contracts for that area are too narrow, or the code
  needs dedicated attention)

### Workflows

- List of Workflow definitions (id, current version hash, trigger
  config if any)
- Workflow content is edited as YAML checked into git, reviewed like
  code — the control plane displays/links to it, it does not provide a
  visual editor in MVP (see 00-vision-and-principles.md, explicitly
  deferred)
- Validation status (does this definition pass the checks in
  02-workflow-definition-schema.md) surfaced before a Workflow can be
  made active

### Workers (Roles)

- List of configured roles (name, harness, model/endpoint)
- Which Workflows reference each role (so changing a role's backing
  model shows its blast radius)
- Concurrency limits, if needed to avoid multiple Runs on the same repo
  causing merge-conflict storms — start with a simple per-repo
  concurrency cap, not a general scheduling system

## Explicitly out of scope for MVP (see 00-vision-and-principles.md)

- Automations as a separate section (folded into Workflow `trigger:`)
- Workflow versioning / A-B comparison UI
- Custom RBAC (inherit from existing repo/VM/SSO access)
- Visual DAG builder

## Non-negotiable even for a minimal build

- **Trace/replay per Run** — full transcript + tool-call log per Run,
  viewable and diffable against the eventual merged PR (or the final
  failure state). This is the only way to debug a Worker without
  re-reading every conversation by hand, and it is what the "keep an
  eye on what agents are doing" requirement actually cashes out to.
- **Every state transition logged as a structured event** (per
  01-run-state-machine.md, Events section), including Runs that fail
  before any model call.
