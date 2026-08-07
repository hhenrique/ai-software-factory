# Tracking Integration

Status: decided (design), not yet built
Depends on: 01-run-state-machine.md (the transitions being mirrored),
03-roles-and-harness-contract.md (the structured role output being
mirrored)
Consumed by: none yet — a future doc04 update (a config surface for
tracker credentials) once this is built

## Problem

A PR the factory opens is bare: no plan, no review findings, no
back-and-forth, just a diff. The entire trace of what an agent did lives
only in the internal projection store (`run_events`), reachable only by
someone who knows the control plane exists and goes looking. Doc01
flagged this ("every state transition should also be mirrored into
whatever tool the Task's source integrates with") and doc03 flagged the
matching content question ("each role's output should also carry
structured narrative content") — both explicitly deferred "since there's
no external surface yet for this content to land on." This doc is that
surface, decided.

## Goal, scoped narrowly

Not bidirectional sync, not a general tracker integration. Specifically:
the conductor posts read-only progress commentary into whatever tool a
Task's source or PR lives in, so a human tracking work from GitHub (or
Aha!) sees it without switching to the control plane. The projection
store remains the source of truth — it's what Overview aggregates from
and what a Run's trace/replay is built on. The external mirror is a
second surface for a human, never a second store nothing else reads from.

## Two integration points, not one

- **PR-side**: comments on the PR a Run opens. Always GitHub for the
  foreseeable future — a Repository's `clone_url` is inherently GitHub
  today (doc04: "GitHub is the only managed provider in this release"),
  so wherever the PR lives is not an independent choice.
- **Source-side**: comments on wherever the Task originated. A GitHub
  issue today (the only real intake path — `taskintake.Submit`'s
  `GitHubIssue` param), but could be an Aha! idea once that intake path
  exists. This is the integration point that actually needs the
  abstraction below; the PR-side one has exactly one adapter for now.

## Design: a Tracker adapter, same shape as the Harness adapter (doc03)

```go
type Tracker interface {
    PostComment(ctx context.Context, target Ref, body string) error
}

type Ref struct {
    Kind string // "github_pr" | "github_issue" | "aha_idea"
    Ref  string // PR/issue URL, Aha! idea id
}
```

One adapter per backend, same division of responsibility doc03 already
established for harnesses — the adapter is the only place tool-specific
API/CLI shape lives, never the conductor:

- **GitHub**: shells out to `gh pr comment` / `gh issue comment` — the
  exact established pattern `internal/activities/pr` already uses (`gh`
  CLI, ambient auth, no new credential handling to build).
- **Aha!**: a new adapter, and genuinely new infrastructure — Aha!'s API
  is REST + API-key based, not an ambient-auth CLI like `gh`. Not built
  until there's an actual Aha!-sourced Task intake path to justify it
  (see Sequencing below); building the adapter before that exists is
  credential/infra work with no real caller.

Adapter selection is resolved **per Task**, not per Repository — a
Repository's PR-tracker is always GitHub in this design, but a Task's
source-tracker depends on that Task's `SourceRef.Kind` (see below), the
same shape as `internal/roleassignment` resolving a Worker per
`(workflow, role)` rather than hardcoding one.

## Where this hooks in: the transition-recording choke point, not the Workflow Definition

Explicitly **not** a new `type: tool` step a Workflow author adds to a
Definition's `steps:` list. That would leak tracking concerns into every
workflow's YAML and risks silent drift — a Workflow that forgets the step
just doesn't post, with no validation rule that could catch "forgot to
mirror" the way doc02's rules catch "forgot `on_malformed_output`."

Instead: `internal/conductor.recordTransition` / `recordFailure` — the
existing single choke-point that already guarantees every transition,
including hard failures, gets exactly one `TransitionEvent` recorded —
gains a second, best-effort sink. After an event lands in the projection
store, if the Task has a resolved Tracker (PR-side, source-side, or
both), format a comment from that same event and post it. Every Workflow
Definition stays tracker-agnostic by construction: a Workflow author
cannot forget this or misconfigure it, because it was never theirs to
configure.

## Content: use what already exists, don't wait on doc03's deferred schema

Doc03's "structured tracking content per role" (Planner assessment,
Coder root-cause analysis, Reviewer's reasoning behind each finding) was
deferred specifically because there was no external surface for it to
land on. That's what this doc builds — but the mirror doesn't need to
wait for that schema. v1 formats what agent steps already produce today:
`verdict`, `scope_contract`, `findings`, a diff summary (files touched /
line counts — not the full diff dumped into a comment, which is noise
for a human skimming an issue thread). The richer narrative content is a
v2 upgrade to this same mechanism's content, once a harness adapter is
actually changed to produce it — the mirror and the content it carries
are separable, and this doc is only the former.

## Failure semantics: best-effort, never blocks the Run

A comment-post failure (GitHub API hiccup, an expired Aha! token) must
not fail the Run — commentary is a side-channel for a human, not part of
Run correctness. But best-effort must not mean silent: record the
failure as a queryable fact — the same `run_events`/trace mechanism any
other tool-call failure surfaces through, not a process log line nobody
reads — so a pattern of dropped comments (a dead Aha! token, a revoked
`gh` credential) is itself visible and actionable, not discovered by
accident when a human notices a PR has gone quiet. Same principle doc03
applies to token accounting ("do not let a harness that under-reports
usage silently exceed budgets") applied to a different subsystem: one
component's flakiness shouldn't propagate into a different guarantee
breaking.

## New structured field: `Task.SourceRef`

Today a Task's GitHub issue reference is folded into free-text
`Description` (`taskintake.fetchGitHubIssue`: `"%s\n\n%s\n\nSource: %s"`
— title, body, issue URL) — good for a human or an agent to read, useless
for a tool step to reliably re-target a comment at. Needs a real
structured field on `Task` / `backlog_tasks`, set once at Task creation
(`taskintake.Submit`) and never mutated afterward — the same
immutable-provenance principle already applied everywhere else in this
pipeline (Runs are never mutated, Workflow provenance is a hash recorded
at Run start):

```go
type SourceRef struct {
    Kind string // "github_issue" | "aha_idea" | "" (no known source — a free-text Task)
    Ref  string // issue URL, Aha! idea id/reference
}
```

## The reopened-issue example, clarified

Not an auto-detection mechanism, and nothing new is needed to make it
happen: it's the existing, ordinary manual-triage path. A human (acting
as QA) finds a regression, reopens the GitHub issue, and starts a new
Task against that same issue number through the normal intake path
(`cmd/submittask` or the control plane's Task form) — no "watch for
reopened issues" trigger, no webhook, nothing auto-generated. What was
actually missing wasn't Task creation (already real) — it was
visibility: with `SourceRef` and the source-side Tracker wired up, every
Run against that issue number, first pass and every follow-up, posts its
progress back onto the same issue. A human reading it sees the full
multi-Run history in one place, in order, without needing to know the
control plane exists.

## Sequencing

1. Add `Task.SourceRef` (schema + `taskintake.Submit` population) —
   small, mechanical, no adapter needed yet.
2. `Tracker` interface + GitHub adapter only (`gh pr comment` /
   `gh issue comment`) — reuses `gh`'s ambient auth, same as
   `internal/activities/pr`.
3. Hook into `recordTransition` / `recordFailure`, best-effort, using
   today's existing structured role output as content.
4. Aha! adapter — deferred until there's an actual Aha!-sourced Task
   intake path; see "Aha!" above for why building it earlier has no real
   caller.
5. Richer narrative content (doc03's deferred schema) — a later,
   separable upgrade to step 3's content, once a harness adapter is
   actually changed to produce it.
