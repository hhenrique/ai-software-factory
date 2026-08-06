// Command submittask is the CLI entry point onto internal/taskintake's
// real Task/Run submission — see that package's doc comment for the
// shared mechanics (it's also what cmd/controlplane's Work section calls).
// This file owns only CLI-specific concerns: flag parsing, merging an
// explicit flag with a registered repository's defaults, and error
// messages.
//
// Unlike cmd/smoketest (fixed synthetic scenarios, the stub harness.invoke
// by default), this always uses the real harness.invoke — that's the
// entire point of this command — so every run costs real API credits.
// Point it at real work: -github-issue fetches a GitHub issue's title/body
// via `gh issue view` as the task description, which is how this was
// verified end to end (toy-repo's `agent-ready`-labeled issues).
//
// -repo <identity> looks up a repository registered via cmd/controlplane
// (internal/repositories) instead of spelling out -repo-clone-url/
// -test-command/-workflow every time.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"factory/internal/activities/gitops"
	"factory/internal/conductor"
	"factory/internal/eventlog"
	"factory/internal/harnesslimits"
	"factory/internal/repositories"
	"factory/internal/taskintake"
	"factory/internal/temporalconn"
)

func main() {
	repoLookup := flag.String("repo", "", "look up a registered repository by canonical identity (e.g. github.com/owner/repo) — see cmd/controlplane. Supplies -repo-clone-url/-test-command/-workflow's defaults; any of those flags still overrides its value")
	repoCloneURL := flag.String("repo-clone-url", "", "HTTPS GitHub clone URL of the target repo (required unless -repo is given)")
	repoName := flag.String("repo-name", "", "short name for the repo (default: derived from clone URL)")
	testCommand := flag.String("test-command", "", "shell command run in the worktree for VERIFYING (required unless -repo supplies one)")
	workflowFile := flag.String("workflow", "", "path to the Workflow Definition YAML to run (default: the registered repo's default_workflow, else taskintake.DefaultWorkflowFile)")
	githubIssue := flag.Int("github-issue", 0, "GitHub issue number to use as the task description (mutually exclusive with -description)")
	description := flag.String("description", "", "free-text task description (mutually exclusive with -github-issue)")
	runIDOverride := flag.String("run-id", "", "override the generated Run id (default: derived + timestamped)")
	flag.Parse()

	if *repoLookup == "" && *repoCloneURL == "" {
		log.Fatalf("submittask: either -repo or -repo-clone-url is required")
	}
	if (*githubIssue == 0) == (*description == "") {
		log.Fatalf("submittask: exactly one of -github-issue or -description is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, err := eventlog.NewPool(ctx)
	if err != nil {
		log.Fatalf("submittask: connect to projection store: %v", err)
	}
	defer pool.Close()

	if *repoLookup != "" {
		repo, err := repositories.Get(ctx, pool, *repoLookup)
		if err != nil {
			log.Fatalf("submittask: -repo %q: %v", *repoLookup, err)
		}
		if !repo.Enabled {
			log.Fatalf("submittask: repository %q is disabled", *repoLookup)
		}
		*repoCloneURL, *testCommand, *workflowFile = resolveRepoConfig(repo, *repoCloneURL, *testCommand, *workflowFile)
	}
	if *testCommand == "" {
		log.Fatalf("submittask: -test-command is required — VERIFYING needs a real command for this repo (the registered repository, if any, has none set)")
	}

	repoSlug, err := gitops.GitHubSlug(*repoCloneURL)
	if err != nil {
		log.Fatalf("submittask: %v", err)
	}
	if *repoName == "" {
		_, *repoName, _ = strings.Cut(repoSlug, "/")
	}

	harnessLimits, err := harnesslimits.ParseEnv()
	if err != nil {
		log.Fatalf("submittask: %v", err)
	}

	hostPort := envOr("TEMPORAL_HOST_PORT", "localhost:7233")
	namespace := envOr("TEMPORAL_NAMESPACE", "default")
	taskQueue := envOr("TASK_QUEUE", "factory-conductor")

	c, err := temporalconn.DialWithRetry(ctx, hostPort, namespace, 45, 2*time.Second)
	if err != nil {
		log.Fatalf("submittask: unable to dial Temporal at %s: %v", hostPort, err)
	}
	defer c.Close()

	result, err := taskintake.Submit(ctx, taskintake.Deps{
		Pool:           pool,
		TemporalClient: c,
		TaskQueue:      taskQueue,
		HarnessLimits:  harnessLimits,
	}, taskintake.Params{
		Repo: conductor.Repo{
			Name:        *repoName,
			CloneURL:    *repoCloneURL,
			TestCommand: *testCommand,
		},
		WorkflowFile:  *workflowFile,
		Description:   *description,
		GitHubIssue:   *githubIssue,
		RunIDOverride: *runIDOverride,
	})
	if err != nil {
		log.Fatalf("submittask: %v", err)
	}
	if result.AttachRunWarning != nil {
		log.Printf("submittask: warning: %v", result.AttachRunWarning)
	}

	fmt.Printf("submitted task %s\n", result.TaskID)
	fmt.Printf("run id:        %s\n", result.RunID)
	fmt.Printf("workflow:      %s\n", result.Workflow)
	fmt.Printf("repo:          %s\n", repoSlug)
}

// resolveRepoConfig merges a registered repository's config with explicit
// flag values — an explicit, non-empty flag always wins over the
// registered value, so -repo is a convenience default, not a hard
// override. Extracted from main for testability: main() itself parses
// flags and calls log.Fatalf on error, both awkward to exercise directly
// in a unit test.
func resolveRepoConfig(repo repositories.Repository, cloneURL, testCommand, workflowFile string) (resolvedCloneURL, resolvedTestCommand, resolvedWorkflowFile string) {
	resolvedCloneURL = cloneURL
	if resolvedCloneURL == "" {
		resolvedCloneURL = repo.CloneURL
	}
	resolvedTestCommand = testCommand
	if resolvedTestCommand == "" {
		resolvedTestCommand = repo.TestCommand
	}
	resolvedWorkflowFile = workflowFile
	if resolvedWorkflowFile == "" {
		resolvedWorkflowFile = repo.DefaultWorkflow
	}
	return resolvedCloneURL, resolvedTestCommand, resolvedWorkflowFile
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
