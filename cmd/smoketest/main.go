// Command smoketest runs the reference dependency-bump-minimal Workflow
// Definition against a real Temporal server through 3 fixed scenarios,
// proving the DAG-to-Temporal mapping and the budget-counter loop are
// genuinely wired, not mocked. It uses fixed, predictable workflow IDs
// (not a timestamp/UUID suffix) — reproducibility comes from `make
// smoketest` wiping Temporal/Postgres state before every run, and fixed
// IDs make a run easy to find by name in the Temporal UI while debugging.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"reflect"
	"time"

	"go.temporal.io/sdk/client"

	"factory/internal/conductor"
	"factory/internal/temporalconn"
	"factory/internal/workflowdef"
)

type scenario struct {
	name                   string
	workflowID             string
	failVerifyUntilAttempt int
	wantFinalState         string
	wantStepsVisited       []string
	wantVerifyRoundsSpent  int
}

func scenarios() []scenario {
	return []scenario{
		{
			name:                   "happy path",
			workflowID:             "smoketest-happy-path",
			failVerifyUntilAttempt: 0,
			wantFinalState:         "COMPLETED",
			wantStepsVisited:       []string{"provision", "execute", "verify", "merge"},
			wantVerifyRoundsSpent:  1,
		},
		{
			name:                   "loop then pass",
			workflowID:             "smoketest-loop-then-pass",
			failVerifyUntilAttempt: 1,
			wantFinalState:         "COMPLETED",
			wantStepsVisited:       []string{"provision", "execute", "verify", "revise_verify", "verify", "merge"},
			wantVerifyRoundsSpent:  2,
		},
		{
			name:                   "budget exhausted",
			workflowID:             "smoketest-budget-exhausted",
			failVerifyUntilAttempt: 99,
			wantFinalState:         "FAILED",
			wantStepsVisited:       []string{"provision", "execute", "verify", "revise_verify", "verify", "revise_verify"},
			wantVerifyRoundsSpent:  3,
		},
	}
}

func main() {
	def, err := workflowdef.Parse(workflowdef.DependencyBumpMinimalYAML)
	if err != nil {
		log.Fatalf("smoketest: parse dependency-bump-minimal: %v", err)
	}
	// Doubles as a validator regression guard: if a future edit to the
	// embedded fixture or the validator itself breaks this reference
	// definition, the smoke test fails loud here rather than deep inside
	// a live Temporal run.
	if errs := workflowdef.Validate(def); len(errs) != 0 {
		log.Fatalf("smoketest: dependency-bump-minimal failed validation:\n%s", errs.Error())
	}

	hostPort := envOr("TEMPORAL_HOST_PORT", "localhost:7233")
	namespace := envOr("TEMPORAL_NAMESPACE", "default")
	taskQueue := envOr("TASK_QUEUE", "factory-conductor")

	dialCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c, err := temporalconn.DialWithRetry(dialCtx, hostPort, namespace, 45, 2*time.Second)
	if err != nil {
		log.Fatalf("smoketest: unable to dial Temporal at %s: %v", hostPort, err)
	}
	defer c.Close()

	allPassed := true
	for _, sc := range scenarios() {
		ok := runScenario(c, taskQueue, *def, sc)
		if ok {
			fmt.Printf("PASS  %s\n", sc.name)
		} else {
			fmt.Printf("FAIL  %s\n", sc.name)
			allPassed = false
		}
	}

	if !allPassed {
		os.Exit(1)
	}
}

func runScenario(c client.Client, taskQueue string, def workflowdef.Definition, sc scenario) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        sc.workflowID,
		TaskQueue: taskQueue,
	}, conductor.RunWorkflow, conductor.RunInput{
		Definition:             def,
		FailVerifyUntilAttempt: sc.failVerifyUntilAttempt,
	})
	if err != nil {
		fmt.Printf("      %s: ExecuteWorkflow: %v\n", sc.name, err)
		return false
	}

	var result conductor.RunResult
	if err := run.Get(ctx, &result); err != nil {
		fmt.Printf("      %s: workflow execution error: %v\n", sc.name, err)
		return false
	}

	ok := true
	if result.FinalState != sc.wantFinalState {
		fmt.Printf("      %s: FinalState = %q, want %q\n", sc.name, result.FinalState, sc.wantFinalState)
		ok = false
	}
	if !reflect.DeepEqual(result.StepsVisited, sc.wantStepsVisited) {
		fmt.Printf("      %s: StepsVisited = %v, want %v\n", sc.name, result.StepsVisited, sc.wantStepsVisited)
		ok = false
	}
	if got := result.BudgetSpent["verify_rounds"]; got != sc.wantVerifyRoundsSpent {
		fmt.Printf("      %s: BudgetSpent[verify_rounds] = %d, want %d\n", sc.name, got, sc.wantVerifyRoundsSpent)
		ok = false
	}
	return ok
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
