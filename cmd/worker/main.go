// Command worker connects to Temporal, registers the generic conductor
// workflow plus the tool/harness Activities, and blocks processing tasks
// on one task queue until interrupted. It also registers
// conductor.record_event (internal/eventlog), which RunWorkflow calls
// directly after every step transition to persist a structured event into
// the control plane's projection store (docs/01, docs/04's "Smoke-test
// strategy"/"Worktree storage" sections) — the foundation any future
// Overview read surface projects from, not something a Workflow
// Definition step declares.
//
// Activity registration is stub.Registrations (every Activity) with the
// real implementations layered on top — worktree.create
// (internal/activities/gitops), run.tests_lint_build
// (internal/activities/verify), pr.create_and_link (internal/activities/pr),
// and harness.invoke (internal/activities/harness) are all real by
// default. What's deployed here is exactly what cmd/smoketest exercises —
// no separate "CI-safe" stub path for anything that has a real
// implementation — with exactly one deliberate exception:
// FACTORY_STUB_HARNESS_INVOKE=1 falls back to the stub's harness.invoke.
// Unlike every other real Activity, each harness.invoke call costs real
// API credits — make smoketest sets this so routine dev-loop runs stay
// free (see Makefile), not because harness.invoke is any less "real" than
// the others.
package main

import (
	"context"
	"log"
	"os"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"

	"factory/internal/activities/gitops"
	"factory/internal/activities/harness"
	"factory/internal/activities/pr"
	"factory/internal/activities/stub"
	"factory/internal/activities/verify"
	"factory/internal/backlog"
	"factory/internal/conductor"
	"factory/internal/eventlog"
	"factory/internal/repoconfig"
	"factory/internal/temporalconn"
)

func main() {
	hostPort := envOr("TEMPORAL_HOST_PORT", "localhost:7233")
	namespace := envOr("TEMPORAL_NAMESPACE", "default")
	taskQueue := envOr("TASK_QUEUE", "factory-conductor")

	// Bounded retry, not a blind sleep: temporalio/auto-setup accepts TCP
	// connections before its schema-setup/namespace-registration finishes,
	// so the first dial attempt right after `docker compose up` can fail.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c, err := temporalconn.DialWithRetry(ctx, hostPort, namespace, 45, 2*time.Second)
	if err != nil {
		log.Fatalf("worker: unable to dial Temporal at %s: %v", hostPort, err)
	}
	defer c.Close()

	// The projection store (internal/eventlog) — a third database in the
	// same shared Postgres instance Temporal uses (doc 05), never
	// Temporal's own execution history. No bounded-retry dial needed here
	// the way temporalconn.DialWithRetry is for Temporal: pgxpool connects
	// lazily, so a not-yet-ready Postgres just means the first
	// record-event call retries (eventAO's RetryPolicy in
	// conductor/workflow.go), not a startup failure.
	eventPool, err := eventlog.NewPool(ctx)
	if err != nil {
		log.Fatalf("worker: unable to configure projection store connection: %v", err)
	}
	defer eventPool.Close()
	eventActivities := &eventlog.Activities{Pool: eventPool}
	backlogActivities := &backlog.Activities{Pool: eventPool} // same projection-store instance

	w := worker.New(c, taskQueue, worker.Options{})
	w.RegisterWorkflow(conductor.RunWorkflow)

	gitActivities := &gitops.Activities{Paths: repoconfig.NewEnvProvider()}
	verifyActivities := &verify.Activities{}
	prActivities := &pr.Activities{}
	harnessActivities := &harness.Activities{}

	registrations := make(map[string]any, len(stub.Registrations))
	for name, fn := range stub.Registrations {
		registrations[name] = fn
	}
	real := []map[string]any{
		gitActivities.Registrations(),
		verifyActivities.Registrations(),
		prActivities.Registrations(),
		eventActivities.Registrations(),
		backlogActivities.Registrations(),
	}
	if os.Getenv("FACTORY_STUB_HARNESS_INVOKE") == "" {
		real = append(real, harnessActivities.Registrations())
	} else {
		log.Printf("worker: FACTORY_STUB_HARNESS_INVOKE set — using the stub harness.invoke (no real LLM calls)")
	}
	for _, set := range real {
		for name, fn := range set {
			registrations[name] = fn // overrides the stub's version of the same name
		}
	}
	for name, fn := range registrations {
		w.RegisterActivityWithOptions(fn, activity.RegisterOptions{Name: name})
	}

	log.Printf("worker: starting, namespace=%s taskQueue=%s temporal=%s", namespace, taskQueue, hostPort)
	if err := w.Run(worker.InterruptCh()); err != nil {
		log.Fatalf("worker: run failed: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
