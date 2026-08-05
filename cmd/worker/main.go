// Command worker connects to Temporal, registers the generic conductor
// workflow plus the stub tool/harness Activities, and blocks processing
// tasks on one task queue until interrupted.
package main

import (
	"context"
	"log"
	"os"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"

	"factory/internal/activities/stub"
	"factory/internal/conductor"
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

	w := worker.New(c, taskQueue, worker.Options{})
	w.RegisterWorkflow(conductor.RunWorkflow)
	for name, fn := range stub.Registrations {
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
