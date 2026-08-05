# Run State Machine

Status: decided (v1 shape for the "issue-to-PR, non-trivial change" case)
Depends on: 00-vision-and-principles.md
Consumed by: 02-workflow-definition-schema.md (steps map onto these states),
05-architecture-temporal.md (states map onto Temporal activities/signals)

## Scope of this document

Defines the states, transitions, ownership (tool vs. agent), and budget
rules for a single Run. This is the state machine for the reference
"standard" Workflow (roughly: plan → implement → verify → review →
merge). Simpler Workflows (e.g. a dependency bump) use a subset of these
states — see 02-workflow-definition-schema.md for how a Workflow
declares which steps it actually uses.

A Run is one execution attempt of a Workflow against a Task. Retries are
new Runs, not mutated ones, so every attempt is independently traceable
and replayable.

## State ownership principle

Every state is owned by exactly one of: **tool** (conductor,
deterministic, no LLM call), **agent** (a Role makes an LLM call with
scoped input/output), or **human** (a person acts via the control
plane). This ownership must be visible in the Workflow definition (see
02-workflow-definition-schema.md `type: tool | agent` per step) so it can
be audited against Rule 2.

## States

| State | Owner | Purpose |
|---|---|---|
| QUEUED | tool | Task is waiting to start a Run |
| PROVISIONING | tool | worktree creation, dependency install, index warm-up |
| PLANNING | agent (Planner) | produce a verdict + scope contract, or reject/escalate |
| EXECUTING | agent (Coder) | produce/modify a patch |
| VERIFYING | tool | run build/test/lint, parse structured results |
| REVISING (verify) | agent (Coder) | address a verify failure, given the failure diff |
| REVIEWING | agent (Reviewer) | assess diff against scope contract |
| REVISING (review) | agent (Coder) | address / dispute / escalate / mark out-of-scope a finding |
| REVIEW_PENDING | human | a gate: human decision required |
| MERGING | tool | PR creation/update, CI trigger, rebase if needed |
| COMPLETED | tool | terminal, success |
| FAILED | tool | terminal, unsuccessful |
| CANCELLED | tool | terminal, human/policy triggered |
| BLOCKED | tool | terminal-adjacent: waiting on an external system; resumes to the state it was blocked from |

## PLANNING — a branch point, not a linear step

`PLANNING` exists because not all Tasks are feasible, and catching that
before `EXECUTING`/`VERIFYING` avoids paying for work that was never
going to land (Rule 1).

Output schema (structured, not prose):
```
verdict: proceed | reject | escalate
scope_contract:            # required when verdict = proceed
  acceptance_criteria: [...]
  in_scope_paths: [...]
  non_goals: [...]
```

Routing:
- `proceed` → EXECUTING, with `scope_contract` attached to the Run's
  immutable context (visible to both Coder and Reviewer for the
  remainder of the Run)
- `reject` → FAILED (infeasible and clear — no human gate needed; this
  is a rejection, not a decision)
- `escalate` → REVIEW_PENDING (infeasible-or-risky-and-ambiguous — scope
  bigger than the ticket implies, touches a protected path, multiple
  valid approaches with real tradeoffs)

**PLANNING is single-shot for ambiguity.** It is not retried in the hope
the agent resolves the ambiguity on a second attempt: if the agent could
resolve it, it would not have been ambiguous to begin with. Retrying
does not grant additional resolving capability.

**Malformed output is a distinct failure mode from ambiguity.** If the
Planner fails to produce valid structured output (bad schema, truncated,
non-conformant), route to REVIEW_PENDING rather than auto-retrying.
This is a conservative default: an automatic retry on malformed output
is itself an unvalidated assumption that retrying fixes it rather than
reproducing the same failure or masking a real prompt/schema bug.
Tag the `REVIEW_PENDING` reason as `planning_malformed_output`, distinct
from `planning_escalate`, so this data is separable for future review
(this decision may be revisited once there is real failure-rate data —
do not build a retry policy on a guess).

## EXECUTING ↔ VERIFYING (verify loop)

```
EXECUTING → VERIFYING ─┬─→ REVIEWING              (pass)
                        ├─→ REVISING(verify) → EXECUTING   (fail, budget remains)
                        └─→ FAILED                         (fail, budget exhausted)
```

`VERIFYING` is fully tool-owned: run the repo's declared build/test/lint
commands, parse structured results (pass/fail counts, which tests,
compiler errors). Routing off that result is a deterministic function,
not an inference — no LLM judgment involved in VERIFYING itself.

**Why this loop is allowed to repeat while PLANNING is not:** a failing
test is new information the agent didn't have before (the failure output
is the missing piece), not the same ambiguity resurfacing. `REVISING`
must be handed the structured *diff* of what changed since the passing
baseline (newly-failing tests, compiler error text, stack trace) — not
the full context again. This is Rule 2 applied inward: the conductor
computes the diff deterministically; the agent gets only the delta.

**Budget:** track both an attempt cap (e.g. 3) and a token budget
independently. A Worker converging in fewer, larger attempts is fine; a
Worker burning large amounts of tokens per attempt without shrinking the
failure set is not, regardless of attempts remaining.

**Oscillation detection:** if VERIFYING's failing-test-set is a superset
of or equal to a previous attempt's failing set (not shrinking), treat
this as non-convergence and fail fast — do not wait for the attempt cap.

Implementation note: a repo's build/test/lint command is arbitrary shell
(04-control-plane-mvp-scope.md), so there's no structured, tool-agnostic
test-identity list to diff — parsing every test runner's own output format
into real test names isn't a "one correct mechanical answer" (rule 2). The
decided mechanical proxy: treat each non-blank line of the combined
stdout+stderr as one "failing" element and compare line-sets across the
two most recent attempts. Noisier than real test identities (a
timestamp-bearing line looks "new" every attempt, understating
convergence), but directionally right, and a false negative here just
falls through to the attempt cap rather than looping forever — see
`RealBudgetGate.CheckOscillation` (`internal/conductor/budgetgate.go`).

**Flakiness guard:** before routing a failure to REVISING, VERIFYING
should deterministically re-run the failed test(s) in isolation once. A
flaky test looks identical to a real regression from inside this loop,
and an agent asked to "fix" it will mangle surrounding code. This check
belongs in tooling, not agent judgment (Rule 2).

## REVIEWING (Coder/Reviewer loop)

Roles: Coder and Reviewer are distinct Roles (harness + model pairs),
independently configured — see 03-roles-and-harness-contract.md.

```
REVIEWING(Reviewer) ─┬─→ REVIEW_PENDING → MERGING     (approved)
                      ├─→ REVISING(review)(Coder) → VERIFYING → REVIEWING   (changes required, budget remains)
                      └─→ FAILED / REVIEW_PENDING      (budget exhausted, unresolved)
```

**Any code change made in response to a review finding must re-enter
VERIFYING before returning to REVIEWING.** Addressing a finding (e.g.
"extract this into a method") is a real code change that can break
already-passing tests. Do not send unverified changes back to the
Reviewer.

**Two independent bounded counters:** verify-rounds and review-rounds
are tracked separately. They represent different failure modes for
metrics purposes (churn on correctness vs. churn on quality/style) and
must not share a single budget.

### Coder's verdict on a finding

Structured, not inferred from prose:
```
verdict: address | dispute | escalate | out_of_scope
```

- `address` → patch the finding, then VERIFYING (not straight back to
  REVIEWING)
- `dispute` → Coder disagrees the finding is valid → back to REVIEWING
  once, with the dispute reasoning attached, so the Reviewer either
  concedes or holds firm. The exchange is a real conversation: the
  Reviewer sees the Coder's dispute reasoning in each subsequent round
  — it is not a blind re-review.
- `escalate` → Coder cannot resolve it (scope ambiguity, conflicting
  requirement) → REVIEW_PENDING, same pattern as PLANNING's escalate
- `out_of_scope` → see below

### Scope contract enforcement

`PLANNING`'s scope contract (acceptance criteria, in-scope paths,
non-goals) is handed to **both** Coder and Reviewer, not just the Coder.
Without a declared scope, a Reviewer has no way to distinguish "defect
in what was asked for" from "improvement I noticed while looking at the
diff," and can recurse into gold-plating findings on code the Coder just
touched because of a prior finding (observed in practice: one case
exceeded 25 review rounds this way).

Every finding carries a scope classification:
- **in-scope, blocking** — gates approval, routes to REVISING
- **out-of-scope, advisory** — logged, does not gate, does not consume a
  review round

Where the classification is a simple path lookup against
`in_scope_paths`, this is a tool check, not agent judgment (Rule 2).
Only genuinely ambiguous boundary cases require agent judgment.

**`out_of_scope` classification is final for the current Run — not
contestable by the Reviewer.** Allowing a contest re-opens exactly the
kind of discussion loop scope was introduced to prevent.

**Out-of-scope findings are not discarded.** The finding (full context:
file/lines, Reviewer's description, originating Run/Task, timestamp) is
converted into a new Task in the backlog, source-tagged
`auto-generated: review-finding`, `QUEUED`, unprioritized. The current
Run's finding status is recorded as
`rejected(out_of_scope, spawned=<new task id>)` and the review round
proceeds without that finding blocking anything. This preserves the
finding without paying for in-loop resolution.

### Verifying the Coder's claim of "addressed"

When a Coder claims `address` on a finding, the Reviewer must re-examine
that specific finding against the new diff and confirm or reject it —
the conductor must not trust or summarize the self-report. This
mirrors VERIFYING's principle (don't trust an agent's claim that
something is fixed) one level up.

Each finding therefore has its own small status machine, independent of
the others in the same Run:
```
raised → addressed(claimed) → verified | rejected
raised → rejected(out_of_scope, spawned=<task id>)   [terminal]
raised → rejected(disputed-and-held)                  [Reviewer holds firm after one contested round]
```
Only `raised` and `rejected(disputed-and-held)` items are open for the
next round; `verified` and `out_of_scope` items are closed and are not
replayed into subsequent rounds. Because the loop is a genuine
conversation, prune the record to open items when constructing context
for the next round rather than replaying the full transcript — token
cost per round grows with round count if the full history is replayed
every time (round 3 re-ingests findings 1–2, both responses, and the new
diff), so pruning closed items is a direct Rule 1 mitigation. Do not,
however, collapse an "addressed" claim into a trusted summary — the
Reviewer's re-check of that specific item is a genuine judgment, not
avoidable context replay.

### Deadlock / tie-break

If the Reviewer raises materially the same finding twice with the same
Coder dispute reasoning both times, treat this as a deadlock and
escalate to REVIEW_PENDING before the round cap is reached, not after —
same logic as the oscillation check in the verify loop.

Implementation note: `coder_response` (the Coder's dispute) carries no
budget of its own in `issue-to-pr-standard` — only `review` does — so its
output isn't in the history `RealBudgetGate.CheckOscillation` compares
across rounds; today's check is "the Reviewer's findings set didn't shrink
round over round," without also confirming the Coder's dispute reasoning
repeated. In practice a repeated identical findings set already means the
round produced no progress regardless of what the Coder said, which is the
signal that actually matters for failing fast — but if `dispute` reasoning
text needs to factor in later (e.g. two different findings disputed with
the same boilerplate non-answer), it would need its own producer field
threaded through `conversation_open_items` and into this comparison.

Hitting the review-round cap with an unresolved dispute routes to
REVIEW_PENDING (not FAILED): a human arbitrates with full context from
both sides, the same as an ordinary code-review escalation to a lead/
senior developer. The state machine's job is only to recognize *when*
this trigger fires (cap reached, or deadlock detected) and hand the
human the finding plus both sides' reasoning — it does not adjudicate.

## MERGING

Tool-owned: PR creation, linking to the source Task, applying labels,
triggering CI, rebasing if the branching policy requires it. No agent
involvement.

## Events

Every state transition emits a structured event: Task ID, Run ID,
from-state, to-state, timestamp, token delta, tool-calls-made. Log this
at the state-machine level for every transition, including Runs that
fail before any model call — this is the raw feed for Overview
aggregation (time-to-green, cost-per-Run, agent-step token ratio; see
04-control-plane-mvp-scope.md).

**Near future, not built yet:** every state transition should also be
mirrored into whatever tool the Task's source integrates with — e.g. a
comment on the originating GitHub issue/PR — not just the internal
projection store. The projection store remains the source of truth (it's
what Overview aggregates from and what a Run's trace/replay is built on);
the external mirror is for a human tracking the work from wherever they
already live, not a second store. See 03-roles-and-harness-contract.md's
matching note on what each role's recorded content should actually
contain, since "record every transition" and "record something worth
reading" are different bars.

## Resuming or cancelling a REVIEW_PENDING Run

Doc 05's signal-wait needs a payload contract; this is it, at the domain
level (see 05-architecture-temporal.md for how it maps onto a Temporal
signal).

A human (or whatever control-plane surface acts on their behalf) resolves
a Run parked at `REVIEW_PENDING` with one of two decisions:

- **resume** — names the state/step to continue the Run from, plus an
  optional hint (free text, not structured/validated by the state
  machine). There is no automatic resume target: different escalation
  reasons plausibly need different resume points (a disputed finding
  might resume at `REVIEWING` with a hint resolving the dispute; a
  Planner escalation might resume at `EXECUTING` with clarified scope).
  Guessing the target automatically risks resuming into the wrong part of
  the loop; naming it explicitly is simpler and doesn't need the state
  machine to infer which escalation implies which resume point.
- **cancel** — terminates the Run as `CANCELLED`.

## Budget reset on human-resumed Runs

When a human resumes a Run from `REVIEW_PENDING` with a hint, **all** of the
Run's budget counters reset to zero-spent — not just the one tied to
whichever loop triggered the escalation (e.g. a hint given after
review-rounds exhausted also resets verify_rounds, even if that counter
still had budget left). A hint can change the trajectory of the whole Run,
not just the loop that escalated it, so a partial reset would leave stale
counters in loops the hint never touched.

This is a deliberate trade-off: repeatedly resetting via hints could in
principle be used to route around oscillation/deadlock detection
indefinitely. It's accepted because a human has to spend attention on each
reset — this is not a free retry, unlike an automatic budget bump would be.
