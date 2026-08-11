# factory

An internal control plane for conducting a fleet of coding agents against
a set of repositories. Define versioned Workflow DAGs, run them
concurrently across repos as Runs, observe agent activity, and gate on
human review where needed. Single internal tool, one team, one VM — not
a general-purpose SaaS product.

Full design context lives in [`docs/`](docs/00-vision-and-principles.md);
this file is just how to build, run, and find your way around the code.

## How it fits together

- **Temporal** is the durable execution engine — a Run is a Temporal
  workflow execution, so a crash or restart resumes exactly where a Run
  left off rather than re-provisioning or re-running completed steps.
- **`cmd/worker`** connects to Temporal and does the actual work: git/
  worktree operations, running a repo's tests, opening PRs, invoking
  harness CLIs (Claude Code, Codex, Copilot), posting progress comments
  back to GitHub.
- **`cmd/controlplane`** is the human-facing surface — a small vanilla-JS
  SPA + JSON API for configuring Repositories/Workflows/Workers, watching
  Tasks, and acting on anything parked waiting for a human (Inbox /
  Pending Approvals).
- **Postgres** backs both Temporal's own execution history and the
  control plane's projection store (a separate database in the same
  instance) — one shared instance, not two.
- A **Workflow Definition** is a hand-authored YAML DAG
  ([`workflows/`](workflows/)), checked into git and reviewed like code —
  see [`docs/02`](docs/02-workflow-definition-schema.md) for the schema.

See [`docs/05`](docs/05-architecture-temporal.md) for the full mapping
from these domain concepts onto Temporal primitives.

## Repository layout

```
cmd/
  controlplane/   the real control plane — SPA + API, meant to be kept and extended
  worker/         connects to Temporal, registers Activities, does the work
  submittask/     CLI entry point for submitting a real Task/Run (costs real API credits)
  smoketest/      fixed synthetic scenarios proving the DAG-to-Temporal mapping works
  runsview/       throwaway Run-visibility tool, not the control plane — will be deleted
internal/
  conductor/      the one generic Temporal workflow that interprets a Workflow Definition
  workflowdef/    the YAML schema, parser, and static validator
  activities/     the real tool/agent Activities (gitops, verify, pr, harness, tracker)
  backlog/        Task persistence, including auto-generated review-finding Tasks
  inbox/          Runs parked at REVIEW_PENDING, split into Inbox vs. Pending Approvals
  workers/        the (harness, model, params) Worker entity, and role assignment
  repositories/   registered-repo config (clone URL, test command, default Workflow)
  ...             one package per concern; each has its own doc comment
workflows/        real, deployable Workflow Definitions
deploy/           docker-compose.yaml (Temporal + Postgres) and Postgres init SQL
docs/             the design spec — read 00 through 08, in order
.sketchpad/       gitignored working notes; nothing in here ships
```

## Prerequisites

- Go 1.26+
- Docker (with Compose) — runs Temporal and Postgres locally
- [`gh`](https://cli.github.com/) — used for PR creation and tracker comments; ambient-auth, `gh auth login` first
- `git`
- Optional, only needed for real (non-stub) agent steps: the `claude`,
  `codex`, and/or `copilot` CLIs, authenticated

## Getting started

Bring up Temporal + Postgres:

```
docker compose -f deploy/docker-compose.yaml up -d --wait
```

Temporal UI: `http://localhost:8080`. Postgres: `localhost:5432`
(`temporal`/`temporal`, see `deploy/docker-compose.yaml`).

Build and run the worker (registers the conductor workflow and every
Activity, then blocks processing Runs):

```
go build -o bin/worker ./cmd/worker
./bin/worker
```

Build and run the control plane:

```
go build -o bin/controlplane ./cmd/controlplane
./bin/controlplane
```

Open `http://localhost:8082`.

Submit a real Task against a registered Repository (real API/harness
cost — see `cmd/submittask`'s doc comment):

```
go build -o bin/submittask ./cmd/submittask
./bin/submittask -repo <identity> -github-issue <number>
```

### Smoke test

`make smoketest` wipes local Temporal/Postgres state, brings the stack
up, and runs the reference `dependency-bump-minimal` Workflow Definition
through 3 fixed scenarios with the stub harness (no API cost) — proves
the DAG-to-Temporal mapping and budget-counter loop are genuinely wired.
Requires `SMOKETEST_REPO_CLONE_URL`/`SMOKETEST_REPO_NAME` (a disposable
repo you're fine getting test PRs against — `pr.create_and_link` needs a
real GitHub-hosted repo):

```
export SMOKETEST_REPO_CLONE_URL=https://github.com/<you>/toy-repo.git
export SMOKETEST_REPO_NAME=<you>/toy-repo
make smoketest
```

## Configuration

Everything is a plain env var, read once at process startup (tracked as
tech debt in [`docs/04`](docs/04-control-plane-mvp-scope.md) — not
control-plane-editable yet):

| Variable | Used by | Default | Purpose |
|---|---|---|---|
| `TEMPORAL_HOST_PORT` | worker, controlplane | `localhost:7233` | Temporal server address |
| `TEMPORAL_NAMESPACE` | worker, controlplane | `default` | Temporal namespace |
| `TASK_QUEUE` | worker, controlplane | `factory-conductor` | Temporal task queue name |
| `PROJECTION_STORE_DSN` | worker, controlplane | matches `deploy/docker-compose.yaml` | control plane's Postgres connection |
| `CONTROLPLANE_ADDR` | controlplane | `:8082` | HTTP listen address |
| `CONTROLPLANE_WORKFLOWS_DIR` | controlplane | `workflows` | where Workflow Definitions are scanned from |
| `FACTORY_ROOT` | worker | `/var/lib/factory` | repo clones + Run worktrees live here |
| `FACTORY_STUB_HARNESS_INVOKE` | worker | unset | route agent steps to a canned stub instead of a real (billed) harness CLI call |
| `FACTORY_HARNESS_TOKEN_LIMITS` | controlplane, submittask, smoketest | unset | per-(harness, model, effort) token circuit-breaker limits, resolved at Run-submission time |
| `SMOKETEST_REPO_CLONE_URL` / `SMOKETEST_REPO_NAME` | smoketest | — (required) | the real repo `pr.create_and_link` is exercised against |

## Testing

```
go build ./...
go vet ./...
go test ./...
```

Integration-style tests skip themselves if Postgres/Temporal aren't
reachable rather than failing.

## Docs

Read in order — each depends on the ones before it:

| Doc | Covers |
|---|---|
| [`00-vision-and-principles.md`](docs/00-vision-and-principles.md) | What this is, the cardinal rules (minimize tokens, tools before agents), core entities |
| [`01-run-state-machine.md`](docs/01-run-state-machine.md) | The Run state machine: plan → execute → verify → review → PR, budgets, the mandatory plan-approval gate |
| [`02-workflow-definition-schema.md`](docs/02-workflow-definition-schema.md) | The YAML DAG schema and its static validation rules |
| [`03-roles-and-harness-contract.md`](docs/03-roles-and-harness-contract.md) | Role vs. Worker, the harness adapter contract, required structured outputs per role |
| [`04-control-plane-mvp-scope.md`](docs/04-control-plane-mvp-scope.md) | What the control plane UI/API covers, and current build state per section |
| [`05-architecture-temporal.md`](docs/05-architecture-temporal.md) | How the domain model maps onto Temporal, deployment topology |
| [`06-workflow-visualizations.md`](docs/06-workflow-visualizations.md) | The graph-visualization spike and its decision |
| [`07-glossary.md`](docs/07-glossary.md) | Canonical terms — check here before introducing a new name for something |
| [`08-tracking-integration.md`](docs/08-tracking-integration.md) | Mirroring Run progress onto GitHub issues/PRs |

## Conventions

- **Tools before agents.** An LLM call is justified only for a step that
  requires judgment about code or intent — everything else (git ops, PR
  creation, running tests, diff/scope checks) is deterministic code. See
  `docs/00`'s Rule 2.
- **Minimize tokens.** Never make an agent re-derive information a prior
  step already computed; bound every loop with an attempt cap *and* a
  token budget; detect non-convergence and fail fast. See `docs/00`'s
  Rule 1.
- Full contributor/agent guidance lives in `CLAUDE.md`.
