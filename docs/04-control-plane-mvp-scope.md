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

#### Current state: Inbox

The "Runs blocked on a human" bullet above is built — as its own focused
view (`cmd/controlplane`'s Inbox section), not as part of a full Overview
dashboard, which stays unbuilt otherwise. `internal/inbox.List` finds
every Run whose latest transition landed on `REVIEW_PENDING` (oldest
first); resume/cancel actions send the real
`conductor.HumanDecision` signal (`internal/inbox.SignalResume`/
`SignalCancel`) — the first caller of that contract from outside the
process that started the Run. Deliberately narrow, matching this doc's
"keep it thin" principle: not a general Runs browser (that stays
`cmd/runsview`'s job, unchanged) — just the one thing genuinely blocked
on a human. Resume's step-id field is a combobox populated from the
Run's own Workflow Definition (`GET /api/workflows`' per-file step-id
list), not free text, for the same typo-prevention reason Repositories'
default-workflow field is a combobox rather than a text input.

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

#### Current state: `cmd/controlplane`, first real UI slice

Repositories is the first section built as a real vertical slice — not a
disposable tool like `cmd/runsview` (see that command's doc comment: it's
explicitly meant to be deleted/replaced wholesale, this one isn't).
`cmd/controlplane` is a small SPA (one HTML shell, vanilla JS against a
JSON API, no framework/build step) with a collapsible left-side nav
structured to grow one section at a time; today only Repositories is
wired into that nav. Persistence is a new `repositories` table in the
projection store (`internal/repositories`), covering exactly the fields
`cmd/submittask` already needs and no more: name (canonical identity,
e.g. `github.com/hhenrique/toy-repo`), clone URL, test command, default
workflow, and an enabled flag. `in_scope_paths`/branching-policy fields
above remain unbuilt — added when a caller actually needs them, same
incremental-growth precedent as `backlog_tasks`.

This is a real second consumer, not just a form that writes into a
table nothing reads: `cmd/submittask -repo <identity>` looks a
repository up and uses its clone URL/test command/default workflow as
defaults (explicit flags still override). A disabled repository is a
kill switch — `submittask` refuses to start a Run against one.

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

### Accumulating env-config surfaces (tech debt, tracked not solved)

Every setting below is a plain env var read once at process startup, each
justified individually at the time it was added (repo roots above; harness
token limits alongside `BudgetGate`; etc.) — but as a set, this is drifting
toward the exact "config sprawl" the control plane is supposed to replace.
Listed here so the debt is visible in one place rather than only inside
each feature's own doc comment:

- `FACTORY_ROOT` — worktree/clone storage root (see above)
- `SMOKETEST_REPO_CLONE_URL` / `SMOKETEST_REPO_NAME` — the live repo
  `cmd/smoketest` exercises `pr.create_and_link` against
- `FACTORY_STUB_HARNESS_INVOKE` — routes `harness.invoke` to a stub
  instead of a real (billed) harness CLI call
- `PROJECTION_STORE_DSN` — the projection-store Postgres connection
- `FACTORY_HARNESS_TOKEN_LIMITS` — per-(harness, model, effort) token
  circuit-breaker limits (`internal/harnesslimits`), decoupled from role,
  scoped per-Run

Each of these is deliberately env-backed for now rather than
control-plane-editable, same trade-off as worktree storage above:
standing up a real config store + editor UI isn't worth it yet. The
intended eventual destination is one shared mechanism — a database-backed
config store with its own small, disposable UI (in the spirit of the
Runs-visibility page, not a polished settings page) — rather than each
surface growing its own bespoke migration path independently. Not
scheduled; revisit once the number of these surfaces (or the pain of
redeploying to change one) makes the migration cost worth paying.

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

### Smoke-test strategy

Current state: the smoke test (`cmd/smoketest`) exercises `worktree.create`,
`run.tests_lint_build`, and `pr.create_and_link` for real against a real
repo (`SMOKETEST_REPO_CLONE_URL`/`SMOKETEST_REPO_NAME`). `harness.invoke`
stays on the stub by default (`FACTORY_STUB_HARNESS_INVOKE`, always set by
`make smoketest`) — unlike the other three, every real harness call costs
API credits, so routine runs stay free rather than following the
ship-what-we-test precedent unconditionally. This means today's smoke
test only proves the straight-line DAG mechanics and the budget-counter
loop shape (via the stub's synthetic pass/fail), not that a real harness
can actually converge through the state machine's inner loops (EXECUTING
↔ VERIFYING, REVIEWING ↔ REVISING — see 01-run-state-machine.md).

**Future direction, not built yet**: a dedicated, maintained test repo
(in the spirit of `toy-repo`, which was used as the workbench for
building `pr.create_and_link` and `harness.invoke`) with a small set of
predefined, deterministically-fixable issues — combined with a
cost-effective harness/model/effort selection — would let the smoke test
exercise a real harness through the full verify and review loops, not
just the mechanical Activities around them. "Predefined issues" matters
for reproducibility: an open-ended real task has no guaranteed
convergence behavior to assert against, but a known, deliberately
broken-in-a-specific-way fixture does (e.g. a test that fails until a
specific one-line fix lands — the harness either finds it or it doesn't,
deterministically enough to assert on).

**Trade-off, accepted in advance**: this makes live harness/API access
*and* a maintained fixture repo a hard dependency for the full smoke
test — the same category of trade-off already accepted for
`pr.create_and_link`'s `SMOKETEST_REPO_CLONE_URL` requirement, extended
to cover the harness loops too. Explicitly deferred — not needed until
there's a concrete reason to test the inner loops end to end rather than
per-Activity (as today's unit tests + live-verified adapters already do —
see `internal/activities/harness`).

### Work

The Task backlog / intake.

- Task fields: id, target repo, description/acceptance intent, source
  (`human`, `ticket`, `auto-generated:review-finding`), status, assigned
  Workflow, priority
- Must support creating a Task programmatically (from the out-of-scope
  finding path in 01-run-state-machine.md) as well as manually — see
  "Manual Task submission, current state" below for where this stands
  today (a CLI, not a control-plane UI form yet)
- A view filtered to `source = auto-generated:review-finding` is useful
  for triage and for spotting repos/areas generating a lot of them (a
  signal that scope contracts for that area are too narrow, or the code
  needs dedicated attention)

#### Out-of-scope Task creation, current state

The `task.create` compound action (01-run-state-machine.md's out-of-scope
finding path) is implemented today as a direct write to a `backlog_tasks`
table in the projection store (`internal/backlog`) — real, not a
placeholder: an out-of-scope finding genuinely becomes a queryable row,
tagged with its Run id and source, the moment `coder_response` routes
through it. What's *not* built yet is everything else this section
describes: no priority, no assigned Workflow, no triage UI, and no
`auto-generated:review-finding` source tag distinct from a plain
`source` string. Treat `backlog_tasks` as the seed of the real Task
entity, not the entity itself.

#### Manual Task submission, current state

Real Task/Run submission now has two entry points sharing one
implementation: `cmd/submittask` (CLI) and `cmd/controlplane`'s Work
section (UI form) both call `internal/taskintake.Submit` — extracted
specifically so this stays one code path rather than two that could
drift. Given a repo (in the UI: picked from already-registered, enabled
Repositories; on the CLI: `-repo <identity>` or explicit `-repo-clone-
url`/`-test-command`) and a task description — either free text or a
GitHub issue number, which shells out to `gh issue view` for the
title/body, the same established gh-owns-auth pattern as
`internal/activities/pr` — it records a Task
(`internal/backlog.InsertHumanTask`, `source: human`) and immediately
starts a real Run against it. There's no decoupled scheduler (see
00-vision-and-principles.md's deferred list), so submitting a Task and
starting its Run are one action for now, not two; `AttachRun` records
the Run id back onto the Task row right after Temporal accepts it. The
UI form confirms with the human before submitting — this is the one
control-plane action that spends real API credits the moment it
succeeds, and the confirm dialog says so.

A read-only Task list (`GET /api/tasks` / `internal/backlog.List`) is
also real now — every Task from both sources (human-submitted here, and
`auto-generated:review-finding` from the out-of-scope path below), not
just the human ones. No edit/delete on a Task — matches this doc's
Repositories precedent of exposing exactly the operations there's a real
use for, not a full CRUD surface by default.

This is also the first place a GitHub issue becomes a Task, ahead of the
"near future: GitHub/tracker integration" this section anticipates —
today that integration is a human running the CLI with an issue number,
not a webhook. Verified live against `toy-repo`'s `agent-ready`-labeled
issues, using `workflows/issue-to-pr-claude-only.yaml` (see that file's
own doc comment for why it's a separate Workflow Definition from
`issue-to-pr-standard`) — including a real run through the full
verify/review inner loops (two review rounds, each with a real finding
addressed) before reaching `COMPLETED` and opening a real PR.

Known gap: `backlog_tasks.status` only reflects "a Run was started"
(`AttachRun` sets it to `RUNNING` and stops there) — nothing currently
syncs it to the Run's actual terminal state (`COMPLETED`/`FAILED`/
`CANCELLED`). The Run's real state lives in `run_events`, queryable by
`run_id`; a Task row's `status` is not yet a reliable read of it. Closing
this gap means either a periodic reconciliation pass or a callback from
`RunWorkflow` itself — deferred until the Work section gets a real triage
view that would actually depend on `status` being accurate.

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

#### Current state: `cmd/controlplane`, second slice

Built as a read-only scan, no new persistence — a Workflow Definition is
already checked-in YAML, so there's no user-entered data to store.
`GET /api/workflows` scans `workflows/` (`internal/workflowdef` fixtures
are reference/test definitions, not deployable ones — see that package's
doc comment — and stay out of this scan), parses and validates each file
the same way `cmd/submittask` already does before using one, and returns
per-file: name, version, step count, its `roles:` block (name/harness/
model), whether it declares a `trigger`, and pass/fail with the actual
validation errors. A file that fails to parse or validate is still
listed — invalid, with why — rather than silently dropped; the point is
surfacing the problem. No id/version-hash/trigger-*config* display
beyond what's listed above yet, and no way to activate/deactivate a
Workflow from the UI (doc 02: Workflow Definitions aren't versioned
beyond a single active definition + provenance hash in v1 to begin
with).

This same endpoint backs the Repositories form's "Default workflow"
combobox (added in that slice, before this one existed) — one scan, two
consumers.

#### Graph visualization prototypes, not a decision yet

`GET /api/workflow-graph?path=` serves one Workflow Definition's full
node/edge graph (every step, every `on:`/`next:`/`on_malformed_output`
edge, terminal states included) — deliberately not requiring
`workflowdef.Validate` to pass first, since seeing the actual structure
(including a broken one, e.g. an unbounded cycle) is exactly when a
human most wants to look at it. Three read-only rendering approaches
consume it as `workflow_v1`/`v2`/`v3` nav entries — a genuine
side-by-side comparison, not three redundant features:

- **v1**: zero dependencies — hand-rolled BFS-layer layout, hand-rolled
  pan/zoom (drag + wheel on an SVG `<g transform>`).
- **v2**: D3 (`d3-zoom` for pan/zoom) + dagre for layout — a real
  layered-graph algorithm instead of v1's approximation, noticeably
  better edge routing around back-edges (every reference Workflow
  Definition has at least one, e.g. `verify` ↔ `revise_verify`).
- **v3**: Cytoscape.js + the `cytoscape-dagre` layout extension — pan/
  zoom/node-dragging come from the library via config, essentially no
  custom interaction code; heaviest vendored dependency of the three.

Libraries are vendored under `cmd/controlplane/static/vendor/` (see that
directory's `README.md`), not CDN-loaded. **Not decided which one (or
none) survives** — this doc will be updated once one is picked, at
which point the other two should come back out rather than lingering as
dead nav entries.

### Workers (Roles)

- List of configured roles (name, harness, model/endpoint)
- Which Workflows reference each role (so changing a role's backing
  model shows its blast radius)
- Concurrency limits, if needed to avoid multiple Runs on the same repo
  causing merge-conflict storms — start with a simple per-repo
  concurrency cap, not a general scheduling system

#### Current state: `cmd/controlplane`, third slice

Built as a derived read-only view, same as Workflows — no standalone
role registry, since roles live inline in each Workflow Definition's
`roles:` block. `GET /api/workers` reuses the same `workflows/` scan as
`GET /api/workflows` and aggregates every role usage by **(harness,
model)**, not by role name: two workflows' "coder" roles pointing at the
same harness/model are one blast-radius group even if named differently;
the same role name in two workflows pointing at different models is not.
That's the literal reading of "changing a role's backing model shows its
blast radius" — the thing a config change actually touches is the
(harness, model) pair, and each group lists every (Workflow, role name)
using it.

Concurrency limits are not surfaced — nothing in the system tracks or
enforces them yet (this doc already called them "if needed"), so there's
no real data to show; a fabricated column would be worse than an absent
one.

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
