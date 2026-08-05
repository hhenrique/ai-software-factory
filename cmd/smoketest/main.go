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
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"go.temporal.io/sdk/client"

	"factory/internal/activities/gitops"
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

	// A real, throwaway local git repo — worktree.create is the real
	// gitops Activity now (see cmd/worker), not the stub no-op, so every
	// scenario needs something real to clone. Local filesystem path, not
	// a network URL: no external dependency, no auth, and it exercises
	// the exact code path a real repo would (git supports cloning from a
	// plain path natively).
	fixtureRepo, cleanupFixture, err := newFixtureRepo()
	if err != nil {
		log.Fatalf("smoketest: create fixture repo: %v", err)
	}
	defer cleanupFixture()

	repo := conductor.Repo{Name: "smoketest-repo", CloneURL: fixtureRepo}

	allPassed := true
	for _, sc := range scenarios() {
		ok := runScenario(c, taskQueue, *def, repo, sc)
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

func runScenario(c client.Client, taskQueue string, def workflowdef.Definition, repo conductor.Repo, sc scenario) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        sc.workflowID,
		TaskQueue: taskQueue,
	}, conductor.RunWorkflow, conductor.RunInput{
		Definition:             def,
		FailVerifyUntilAttempt: sc.failVerifyUntilAttempt,
		Repo:                   repo,
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

	// Proves worktree.create is the real gitops Activity, not the old
	// no-op stub: a real directory must exist on disk, checked out on the
	// factory/<run-id> branch (RunID == Temporal WorkflowID == sc.workflowID).
	worktreePath, _ := result.FinalContext["worktree_path"].(string)
	if worktreePath == "" {
		fmt.Printf("      %s: FinalContext[worktree_path] missing/empty\n", sc.name)
		ok = false
	} else if _, statErr := os.Stat(worktreePath); statErr != nil {
		fmt.Printf("      %s: worktree_path %q does not exist on disk: %v\n", sc.name, worktreePath, statErr)
		ok = false
	}
	wantBranch := gitops.BranchName(sc.workflowID)
	if got, _ := result.FinalContext["branch"].(string); got != wantBranch {
		fmt.Printf("      %s: FinalContext[branch] = %q, want %q\n", sc.name, got, wantBranch)
		ok = false
	}

	return ok
}

// newFixtureRepo creates a throwaway local git repo with one commit on
// "main", usable directly as a gitops CloneURL. Returns its path and a
// cleanup func the caller must defer.
func newFixtureRepo() (string, func(), error) {
	dir, err := os.MkdirTemp("", "smoketest-fixture-repo-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }

	steps := [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "smoketest@example.com"},
		{"config", "user.name", "smoketest"},
	}
	for _, args := range steps {
		if err := runGit(dir, args...); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("smoketest fixture\n"), 0o644); err != nil {
		cleanup()
		return "", nil, err
	}
	if err := runGit(dir, "add", "README.md"); err != nil {
		cleanup()
		return "", nil, err
	}
	if err := runGit(dir, "commit", "-q", "-m", "initial"); err != nil {
		cleanup()
		return "", nil, err
	}
	return dir, cleanup, nil
}

func runGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
