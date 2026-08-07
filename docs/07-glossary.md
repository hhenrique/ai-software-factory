# Glossary

Status: living document — not a spec, a check against vocabulary drift.
Update it whenever a naming inconsistency is found or a new overloaded
term is introduced; don't let it go stale the way `docs/04`'s "Workers
(Roles)" heading did.

Depends on: 00-vision-and-principles.md (Core entities), 04-control-plane-mvp-scope.md

## Product name: Factory

**Factory** — not "the tool," not "the app." This isn't a new decision;
it's formalizing what's already true everywhere in the codebase (Go
module `factory`, HTML `<title>factory control plane</title>`, the
sidebar wordmark, doc00's own "a small internal control plane... (\"the
factory\")"). "Tool" and "App" never made it into anything built — they
were only ever said out loud, not written down. Use "Factory" (capitalized,
as a proper noun) in prose and docs; the lowercase `factory` wordmark in
the UI is a deliberate logo/brand-mark style choice, not the spelling to
use in a sentence.

## Mental model: What / How / Who / Where / When

Four things get *configured* ahead of time; one thing *happens* when they
meet. Useful as the one-paragraph pitch to someone new:

> A **Run** happens when a **Task** (what) is executed against a
> **Repository** (where), following a **Workflow** (how), performed by
> **Workers** (who) — each playing a fixed **Role** (planner, coder,
> reviewer) a step assigns them to.

| Dimension | Entity | Notes |
|---|---|---|
| What | **Task** | The unit of work — description/acceptance intent. Configured once, doesn't change during a Run. |
| How | **Workflow** (Definition) | The DAG of steps a Task is executed against. Hand-authored YAML, checked into git. |
| Who | **Worker**, playing a **Role** | Worker is the concrete `(harness, model, params)` triad — the "who" you configure in the Workers view. Role (`planner`/`coder`/`reviewer`) is the fixed capacity a Worker plays at a given step — the "acting as what" underneath the "who." A Worker doesn't have an inherent Role; a *Workflow's role assignment* gives it one. |
| Where | **Repository** | The repo a Task targets — clone URL, test command, scope rules. |
| When / what happened | **Run** | Not configured ahead of time — the record of one actual attempt: this Task, this Workflow, these Workers, against this Repository, starting now. Retries are new Runs, never mutated ones (docs/01), so "when" is always traceable to one specific attempt. |

The four configured nouns are stable, reusable, and edited independently
of each other (that independence — e.g. changing a Worker's model without
touching the Workflow YAML — is the whole point of the Role/Worker split
in `docs/03`). A Run is the one-time, non-reusable event where a specific
combination of all four actually got exercised.

## How to use this

Each entry is the *one* term to use for a concept — in code, docs, UI
labels, and API routes alike. "Known deviations" are places that
currently say something else; fix them opportunistically when touching
that code, not as a standalone renaming project. New deviations found
later get added here, not silently worked around.

## Core entities

| Canonical term | Definition | Known deviations |
|---|---|---|
| **Task** | A unit of work: target repo, description/acceptance intent, source (`human` / `ticket` / `auto-generated:review-finding`). | None user-facing. `internal/backlog`/`backlog_tasks` is the implementation package/table name — internal, doesn't need to match, but don't let it leak into anything user-facing. |
| **Workflow Definition** | The versioned YAML DAG a Task is executed against. | None currently — "Workflow" alone is used as a short form throughout code/UI (`WorkflowInfo`, `/api/workflows`, nav "Workflows"), which is fine; the full term exists specifically to disambiguate from *Temporal* Workflow (see below), not to replace the short form everywhere. |
| **Run** | One execution attempt of a Workflow Definition against a Task. | None found. |
| **Role** | A named function a step is responsible for — `planner`, `coder`, `reviewer`. Fixed, closed set (`internal/workflowdef.KnownRoles`), tied 1:1 to a state in the Run state machine. | None found post-split (see `docs/03`). |
| **Worker** | The persisted `(harness, model, params)` triad a Role is played by, independent of any Workflow (`internal/workers`). | Collides with two *different*, pre-existing senses of "worker" — see "Overloaded terms" below. Not a naming bug to fix, a distinction to hold onto deliberately. |
| **Repository** | A registered repo: clone URL, test command, default Workflow, scope rules (`internal/repositories`). | `conductor.Repo` is a deliberately smaller subset ("just enough to clone and branch against a repo") — a real, documented convention (short name = trimmed subset type), not drift. Follow this pattern if another subset type is ever needed; don't invent a different shortening convention. |
| **Inbox** | The control-plane view of Runs currently parked at `REVIEW_PENDING`, awaiting a human decision. | None — nav, view header, and card count all say "Inbox" now. Explanatory prose still names the underlying state (`REVIEW_PENDING`) for anyone who wants the mechanism, which is a disambiguation, not a competing label. |

## Overloaded terms (deliberately kept, always disambiguated)

These aren't naming bugs — the short form is genuinely the right word in
its own domain. The rule is to always qualify which one when there's any
chance of confusion, established first by CLAUDE.md for Workflow:

| Short form | Sense A | Sense B | How to disambiguate |
|---|---|---|---|
| Workflow | **Workflow Definition** — the domain YAML DAG | **Temporal Workflow** — the SDK/execution primitive a Run maps onto | "Workflow Definition" / "Temporal Workflow" or "workflow execution" (CLAUDE.md) |
| Worker | **Worker** — the domain `(harness, model, params)` entity (`internal/workers`) | The **worker process** — `cmd/worker`, a Temporal `worker.Worker` (`go.temporal.io/sdk/worker`) that polls a task queue | "a Worker" (domain entity) / "the worker process" or "Temporal worker" (infra) — never bare "worker" when both senses are in play |
| Task | **Task** — the domain entity (`backlog_tasks`) | Temporal's **Task Queue** (`factory-conductor`) — where Activities/Workflow tasks get dispatched from, not ours to rename | "a Task" / "the task queue" — lowercase "task queue" reads as Temporal's term, capitalized "Task" as ours |
| Step | **Step** — one node in a Workflow Definition's DAG | **Activity** — the Temporal primitive a step maps onto | doc05's own section title, "Step type → Activity implementation" — keep using both, never blur them into each other |
| Trigger | A Workflow Definition's optional `trigger:` field | **Automation** — not a separate concept in v1, explicitly just a Workflow with a `trigger:` set (doc00/02) | Say "a Workflow with a trigger," not "an Automation," until/unless doc00's deferral is revisited |
