// Command smoketest runs the reference dependency-bump-minimal Workflow
// Definition against a real Temporal server through 3 fixed scenarios,
// proving the DAG-to-Temporal mapping and the budget-counter loop are
// genuinely wired, not mocked. It uses fixed, predictable workflow IDs
// (not a timestamp/UUID suffix) — reproducibility comes from `make
// smoketest` wiping Temporal/Postgres state before every run, and fixed
// IDs make a run easy to find by name in the Temporal UI while debugging.
//
// Every Activity cmd/worker registers is real (see cmd/worker) except
// harness.invoke — including pr.create_and_link, which needs a real
// GitHub-hosted repo to push/PR against (a "Pull Request" isn't a git
// object, so there's no local-only equivalent to fall back to). That
// target is SMOKETEST_REPO_CLONE_URL/SMOKETEST_REPO_NAME, required env
// vars: dev machines point them at a personal disposable-PR repo (e.g.
// toy-repo); this intentionally isn't hardcoded or defaulted here since
// it's account-specific, not something to check in.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"reflect"
	"time"

	"go.temporal.io/sdk/client"

	"factory/internal/activities/gitops"
	"factory/internal/conductor"
	"factory/internal/harnesslimits"
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

	cloneURL := os.Getenv("SMOKETEST_REPO_CLONE_URL")
	if cloneURL == "" {
		log.Fatalf("smoketest: SMOKETEST_REPO_CLONE_URL is required — pr.create_and_link needs a real " +
			"GitHub-hosted repo to push/PR against (a Pull Request isn't a git object, so there's no " +
			"local-only fixture that can stand in for one). Point it at a repo you're fine getting " +
			"disposable test PRs, e.g. https://github.com/<you>/toy-repo.git")
	}
	repoSlug, err := gitops.GitHubSlug(cloneURL)
	if err != nil {
		log.Fatalf("smoketest: %v", err)
	}

	// Resolved once here, outside RunWorkflow (which must stay a
	// deterministic function of its input — see internal/harnesslimits'
	// doc comment), and threaded into every scenario's RunInput below.
	harnessLimits, err := harnesslimits.ParseEnv()
	if err != nil {
		log.Fatalf("smoketest: %v", err)
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

	repo := conductor.Repo{
		Name:     envOr("SMOKETEST_REPO_NAME", "smoketest-repo"),
		CloneURL: cloneURL,
		// run.tests_lint_build is the real verify Activity (see
		// cmd/worker), so this needs to be an actual shell command, not a
		// FailVerifyUntilAttempt Go-side switch. It reproduces the exact
		// same threshold behavior for real, off the FACTORY_ATTEMPT_NUMBER/
		// FACTORY_FAIL_VERIFY_UNTIL_ATTEMPT env vars verify.Activities sets
		// on every invocation.
		TestCommand: `[ "$FACTORY_ATTEMPT_NUMBER" -le "${FACTORY_FAIL_VERIFY_UNTIL_ATTEMPT:-0}" ] && { echo "simulated failure at attempt $FACTORY_ATTEMPT_NUMBER"; exit 1; } || { echo pass; exit 0; }`,
	}

	// Self-healing, same spirit as the Makefile's `docker compose down -v`
	// before every run: close+delete any PR/branch a previous run left on
	// the real target repo for these fixed scenario names, so repeated
	// dev-loop runs don't accumulate stale PRs. Best-effort — there's
	// nothing to clean up on a first run.
	for _, sc := range scenarios() {
		cleanupPR(repoSlug, gitops.BranchName(sc.workflowID))
	}

	allPassed := true
	for _, sc := range scenarios() {
		ok := runScenario(c, taskQueue, *def, repo, harnessLimits, sc)
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

func runScenario(c client.Client, taskQueue string, def workflowdef.Definition, repo conductor.Repo, harnessLimits map[string]int, sc scenario) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        sc.workflowID,
		TaskQueue: taskQueue,
	}, conductor.RunWorkflow, conductor.RunInput{
		Definition:             def,
		FailVerifyUntilAttempt: sc.failVerifyUntilAttempt,
		Repo:                   repo,
		HarnessLimits:          harnessLimits,
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

	// Scenarios that reach merge must have opened a real PR — proves
	// pr.create_and_link is the real Activity, not the old no-op stub.
	if sc.wantFinalState == "COMPLETED" {
		prURL, _ := result.FinalContext["pr_url"].(string)
		if prURL == "" {
			fmt.Printf("      %s: FinalContext[pr_url] missing/empty\n", sc.name)
			ok = false
		}
	}

	return ok
}

func cleanupPR(repoSlug, branch string) {
	cmd := exec.Command("gh", "pr", "close", branch, "--repo", repoSlug, "--delete-branch")
	_ = cmd.Run() // best-effort: "no such PR" on a first run is expected, not an error
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
