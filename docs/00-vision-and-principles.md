# Software Factory — Vision & Principles

Status: decided
Audience: coding agent implementing the MVP; future maintainers
Companion docs: 01-run-state-machine.md, 02-workflow-definition-schema.md,
03-roles-and-harness-contract.md, 04-control-plane-mvp-scope.md,
05-architecture-temporal.md

## What this is

An internal control plane for conducting a fleet of large-scale coding
agents against a set of repositories ("the factory"). It lets a small
internal team:

1. Define repeatable workflows for how work moves from "task" to "merged PR"
2. Run many of those workflows concurrently, across many repos, without a
   human driving each one by hand
3. Observe what agents are doing, in enough detail to debug and trust them
4. Intervene at defined gates when human judgment is required

This is not a general-purpose SaaS product. It is a single internal tool
run for one team, on one VM, against known repositories. Scope decisions
throughout these docs favor simplicity over generality — see
"Explicitly deferred" below and in 04-control-plane-mvp-scope.md.

## Cardinal rules (non-negotiable, apply to every design decision)

### Rule 1 — The factory itself should use as few tokens as possible

Every design decision defaults to the cheapest correct mechanism.
Concretely:

- Prefer deterministic computation over an LLM call wherever the outcome
  has one correct mechanical answer.
- Track token spend per Run and per step type; treat token spend as a
  first-class metric, not an afterthought.
- Never let an agent re-derive information a prior step already computed
  (e.g. don't ask an agent to re-read a full conversation transcript when
  only the open/unresolved items matter — see 01-run-state-machine.md,
  Coder/Reviewer loop).
- Bound every loop with both an attempt cap AND a token budget. Attempt
  count alone is not a sufficient governor (a Worker can burn far more
  tokens per attempt without increasing attempts).
- Detect non-convergence (oscillation, repeated identical findings) and
  fail fast rather than exhausting the budget passively.

### Rule 2 — Tools before agents

An LLM call is justified only for a step whose input/output cannot be
produced by a deterministic function — i.e., a step that requires
judgment about code or intent. Everything else is control flow and
belongs in code:

- git clone / worktree creation / branch naming / rebase / push → tool
- PR creation, labeling, linking to the source task → tool
- Running tests/lint/build and parsing results → tool
- Polling CI status, retry/backoff → tool
- Diff-size checks, protected-path checks, scope-boundary checks (when
  scope is expressed as file paths) → tool
- Producing a patch given failing test output and scoped context → agent
- Assessing whether a diff meets acceptance criteria → agent
- Judging whether a review finding is valid/in-scope when the boundary
  itself is ambiguous → agent (the *lookup* against declared scope is a
  tool check; only genuine ambiguity escalates to agent judgment)

A useful test when defining any new step: "does this step have exactly
one correct mechanical answer, or does it require judgment?" One correct
answer → tool. Judgment → agent, and only agent.

## Core entities

- **Repository** — a repo the factory can operate on, with its own build/
  test/lint commands, scope-affecting path rules, and default Workflow.
- **Task** — a unit of work: target repo, description/acceptance intent,
  source (human-authored, ticket-derived, or auto-generated from a review
  finding — see 01-run-state-machine.md, out-of-scope handling).
- **Workflow** — a versioned* DAG definition (steps, roles, budgets,
  routing) that a Task is executed against. (*See MVP scope: v1 keeps a
  single active definition per Workflow, not full versioning.)
- **Run** — one execution attempt of a Workflow against a Task. A Task
  may have multiple Runs (retries are new Runs, not mutations, to
  preserve trace/replay). All state-machine mechanics live at the Run
  level — see 01-run-state-machine.md.
- **Worker / Role** — a role is a harness + model pair (e.g. Planner =
  Claude /plan + Sonnet 5 medium; Coder = Codex + a coding model;
  Reviewer = a review-focused harness + model). Roles are deployment-
  target-agnostic: the backing model may be a frontier API or a
  self-hosted local model — the factory does not care which. See
  03-roles-and-harness-contract.md.

## Explicitly deferred for MVP (do not build these first)

- Workflow versioning / A-B comparison of workflow variants
- A visual DAG editor (workflows are hand-authored YAML, reviewed like
  code, checked into git)
- A bespoke execution/scheduling engine (use Temporal — see
  05-architecture-temporal.md)
- Automations as a separate product surface (a trigger config is a field
  on a Workflow, not its own section)
- Custom RBAC (inherit access control from existing repo/VM/SSO access)
- Per-model granular cost dashboards (start with aggregate token/cost
  metrics; break down further only once there's a real need)

## Deployment shape

The factory runs as a service on a VM the team has access to. It is not
tied to any specific model host. A Role's `harness`/`model` config points
at whatever endpoint serves that role — local network call to a
self-hosted model, or an outbound API call to a frontier provider. The
factory's own hosting has no dependency on where any given role's model
lives.
