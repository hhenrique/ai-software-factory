# Execution Architecture — Temporal-Backed

Status: decided (Temporal as the execution engine; resilience is the
deciding requirement)
Depends on: 00-vision-and-principles.md, 01-run-state-machine.md,
02-workflow-definition-schema.md, 03-roles-and-harness-contract.md

## Decision

The factory's execution mechanics are built on Temporal (or an
equivalent durable-execution engine), not a bespoke scheduler, and not
plain Bash/cron. The deciding factor is resilience: a Run mid-`REVISING`
when the VM restarts must resume exactly where it left off, without
re-provisioning a worktree or re-running completed steps. Plain
Bash+cron can trigger and execute individual steps, but is materially
weaker at this specific guarantee, and re-provisioning on every crash or
restart is a direct Rule 1 violation (wasted tokens/compute repeating
work that already succeeded).

This decision was reached only after the state machine, budgets, and
role/harness contracts were pinned down (00–03) — implementation
mechanics should satisfy that contract, not shape it. Nothing in 00–04
assumes Temporal specifically; a different durable-execution engine
satisfying the same resumability guarantee is an acceptable substitute.

## Mapping the domain onto Temporal

| Domain concept | Temporal concept |
|---|---|
| Run | Workflow execution (Temporal's "Workflow," not to be confused with our domain "Workflow" definition — see naming note below) |
| A Workflow definition's `type: tool` step | Activity |
| A Workflow definition's `type: agent` step | Activity that calls out to a harness adapter (03-roles-and-harness-contract.md), same Activity semantics as any tool step — the conductor does not need a different execution primitive for agent vs. tool steps, only a different implementation inside the Activity |
| Verify loop / review loop bounded retry | Temporal retry policy, parameterized by the `budgets:` block (max_attempts / max_rounds / max_tokens — Temporal's native retry policy covers attempt count; token budget enforcement is custom logic inside the Activity or a side-effect counter checked before each retry) |
| REVIEW_PENDING | Signal-wait: the Temporal workflow execution blocks awaiting a signal from the control plane (human decision), rather than polling |
| BLOCKED (external wait, e.g. CI) | Signal-wait or a polling Activity with backoff, depending on whether the external system can push a webhook back to the control plane (prefer push/signal where available — fewer wasted polling cycles, Rule 1) |
| Out-of-scope finding → new Task | A side-effect Activity (`task.create`) invoked from within the Run's execution, same as any other tool action |
| Crash / VM restart mid-Run | Temporal's durable execution history replays the workflow to its last completed step automatically — no custom resume logic needed beyond what Temporal provides |

**Implementation note on the retry-policy row above:** Temporal's native `RetryPolicy` retries a single Activity invocation with the same input — it cannot model a loop that alternates between different Activities
(e.g. `verify` → `revise_verify` → `verify`, a different role/action each time) with independent, per-loop counters and branch-dependent routing. Bounded loops of that shape are implemented as an ordinary counter inside
the workflow's own durable state instead: incremented on each re-entry into the budgeted step, checked before invoking that step's Activity, routes to `budget_exhausted` without a call if exceeded — never a literal
`RetryPolicy.MaximumAttempts`. This still gets the crash-resumability guarantee this document opens with, via workflow history replay rather than Activity-internal retry bookkeeping, and it covers every budgeted loop
shape uniformly rather than requiring one execution strategy per shape. `RetryPolicy` remains the right tool for its usual purpose — retrying a single Activity's own transient infra failures — used separately from
budget enforcement.

**Naming note:** "Workflow" is overloaded — our domain's Workflow
(02-workflow-definition-schema.md, a YAML DAG definition) and Temporal's
own "Workflow" (an execution primitive) are different things. Recommend
the codebase consistently uses "Workflow Definition" for the domain
concept and "Temporal Workflow" (or just "workflow execution") for the
Temporal primitive, to avoid this ambiguity leaking into code and docs.

## Step type → Activity implementation

- **`type: tool` Activities** are straightforward: shell out / call an
  API, return a structured result. No LLM involved. This is where git
  operations, test/build/lint execution, and PR creation live.
- **`type: agent` Activities** call the relevant Role's harness adapter
  (03-roles-and-harness-contract.md): normalize input from the Run's
  context, invoke the harness (local or frontier endpoint — the
  Activity does not care which), normalize output against the step's
  `output_schema`, normalize token accounting. Malformed-output handling
  (route per `on_malformed_output`, do not silently retry) is enforced
  here.

## Budget enforcement

Attempt-count budgets are enforced by a workflow-level counter checked
before each re-entry into a budgeted step — see the implementation note
under "Mapping the domain onto Temporal" above for why this is a
workflow counter rather than Temporal's built-in Activity `RetryPolicy`
(the loops these budgets bound span multiple different Activities, which
`RetryPolicy` can't express). Token budgets and oscillation/deadlock
detection (01-run-state-machine.md) are domain logic that must run
*inside* the Activity or as logic evaluated before each retry is
permitted — Temporal does not know about tokens or test-failure-set
comparisons natively. Concretely: before allowing a retry, check (a)
attempts remaining (workflow counter), (b) cumulative tokens spent
against `max_tokens` (custom), (c) oscillation/deadlock condition
(custom) — any of these failing routes to the Workflow Definition's
declared `budget_exhausted` target, not a bare retry.

## Deployment topology

- Single VM, self-hosted, deployed via Docker Compose: the Temporal
  server, a single shared Postgres instance, the conductor/Activity
  workers, the control plane API/UI, and local worktree storage for repos
  being operated on.
- Postgres is shared at the instance level only — Temporal's own
  execution-history persistence and the control plane's projection store
  (below) each get their own database within that instance, not a shared
  schema. Temporal requires a supported persistence backend (Postgres,
  MySQL, or Cassandra); since Postgres is already required for Temporal,
  reuse that same instance for the projection store rather than running a
  second database engine — one Postgres container in the compose file,
  not two.
- Harness adapters make outbound calls (or local-network calls to a
  self-hosted model, per 03-roles-and-harness-contract.md) from the
  Activity workers — no separate hosting requirement.
- The control plane UI/API reads Run/Task state from Temporal's
  workflow history and/or the projection database (its own database in
  the shared Postgres instance, kept in sync via Temporal's event stream)
  — do not make the UI query Temporal's raw history directly for every
  Overview metric; project into the projection store for the Overview
  aggregations in 04-control-plane-mvp-scope.md.

## What NOT to build

- A custom scheduler, retry engine, or crash-recovery mechanism — this
  is exactly the kind of mechanical, well-defined problem Temporal
  already solves; reinventing it is a Rule 2 violation one level up
  (tools before hand-rolled infrastructure).
- A custom workflow DSL execution engine — the YAML schema in
  02-workflow-definition-schema.md is the domain-facing definition
  format; it should compile/map to Temporal Workflow code, not become
  its own interpreter.

