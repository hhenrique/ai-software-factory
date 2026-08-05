// Command submittask is the MVP's real Task/Run entry point: it records a
// human-submitted Task (internal/backlog.InsertHumanTask) and immediately
// starts a real Run against it — there's no decoupled scheduler yet (see
// docs/00's "bespoke scheduling engine" deferral), so submitting a Task
// and starting its Run are one action for now, not two.
//
// Unlike cmd/smoketest (fixed synthetic scenarios, the stub harness.invoke
// by default), this always uses the real harness.invoke — that's the
// entire point of this command — so every run costs real API credits.
// Point it at real work: -github-issue fetches a GitHub issue's title/body
// via `gh issue view` as the task description, which is how this was
// verified end to end (toy-repo's `agent-ready`-labeled issues).
//
// The default -workflow (workflows/issue-to-pr-claude-only.yaml) exists
// because Coder/Reviewer in the doc 02 reference definition
// (issue-to-pr-standard) are Codex/Copilot CLI, and this deployment only
// has Claude Code credits — see that file's own doc comment.
//
// -repo <identity> looks up a repository registered via cmd/controlplane
// (internal/repositories) instead of spelling out -repo-clone-url/
// -test-command/-workflow every time — the first real consumer of that
// registry, not just its UI.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"go.temporal.io/sdk/client"

	"factory/internal/activities/gitops"
	"factory/internal/backlog"
	"factory/internal/conductor"
	"factory/internal/eventlog"
	"factory/internal/harnesslimits"
	"factory/internal/repositories"
	"factory/internal/temporalconn"
	"factory/internal/workflowdef"
)

func main() {
	repoLookup := flag.String("repo", "", "look up a registered repository by canonical identity (e.g. github.com/owner/repo) — see cmd/controlplane. Supplies -repo-clone-url/-test-command/-workflow's defaults; any of those flags still overrides its value")
	repoCloneURL := flag.String("repo-clone-url", "", "HTTPS GitHub clone URL of the target repo (required unless -repo is given)")
	repoName := flag.String("repo-name", "", "short name for the repo (default: derived from clone URL)")
	testCommand := flag.String("test-command", "", "shell command run in the worktree for VERIFYING (required unless -repo supplies one)")
	workflowFile := flag.String("workflow", "", "path to the Workflow Definition YAML to run (default: the registered repo's default_workflow, else workflows/issue-to-pr-claude-only.yaml)")
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
	if *workflowFile == "" {
		*workflowFile = defaultWorkflowFile
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

	taskDescription := *description
	if *githubIssue != 0 {
		taskDescription, err = fetchGitHubIssue(repoSlug, *githubIssue)
		if err != nil {
			log.Fatalf("submittask: %v", err)
		}
	}

	defBytes, err := os.ReadFile(*workflowFile)
	if err != nil {
		log.Fatalf("submittask: read -workflow %s: %v", *workflowFile, err)
	}
	def, err := workflowdef.Parse(defBytes)
	if err != nil {
		log.Fatalf("submittask: parse -workflow %s: %v", *workflowFile, err)
	}
	if errs := workflowdef.Validate(def); len(errs) != 0 {
		log.Fatalf("submittask: -workflow %s failed validation:\n%s", *workflowFile, errs.Error())
	}

	harnessLimits, err := harnesslimits.ParseEnv()
	if err != nil {
		log.Fatalf("submittask: %v", err)
	}

	taskID, err := backlog.InsertHumanTask(ctx, pool, repoSlug, def.Workflow, taskDescription)
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

	runID := *runIDOverride
	if runID == "" {
		runID = generateRunID(*repoName, *githubIssue)
	}

	repo := conductor.Repo{
		Name:        *repoName,
		CloneURL:    *repoCloneURL,
		TestCommand: *testCommand,
	}

	// Fire-and-forget: a real Run (real harness calls, possibly a full
	// verify/review loop) can run long. There's nothing this process needs
	// to do once the Run is durably started — see runsview for progress,
	// or the Temporal UI directly.
	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        runID,
		TaskQueue: taskQueue,
	}, conductor.RunWorkflow, conductor.RunInput{
		Definition:     *def,
		InitialContext: map[string]any{"task_description": taskDescription},
		Repo:           repo,
		HarnessLimits:  harnessLimits,
	})
	if err != nil {
		log.Fatalf("submittask: ExecuteWorkflow: %v", err)
	}

	if err := backlog.AttachRun(ctx, pool, taskID, run.GetID()); err != nil {
		// The Run is already durably started in Temporal regardless — this
		// only means the backlog_tasks row won't show its run_id, not that
		// the Run itself failed to start.
		log.Printf("submittask: warning: %v", err)
	}

	fmt.Printf("submitted task %s\n", taskID)
	fmt.Printf("run id:        %s\n", run.GetID())
	fmt.Printf("workflow:      %s\n", def.Workflow)
	fmt.Printf("repo:          %s\n", repoSlug)
}

// fetchGitHubIssue shells out to `gh issue view` (same established pattern
// as internal/activities/pr — gh already owns GitHub auth) and folds the
// issue's title/body/URL into one task description string.
func fetchGitHubIssue(repoSlug string, number int) (string, error) {
	out, err := exec.Command("gh", "issue", "view", fmt.Sprint(number),
		"--repo", repoSlug, "--json", "title,body,url").Output()
	if err != nil {
		return "", fmt.Errorf("gh issue view %d --repo %s: %w", number, repoSlug, err)
	}

	var issue struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		URL   string `json:"url"`
	}
	if err := json.Unmarshal(out, &issue); err != nil {
		return "", fmt.Errorf("gh issue view %d: parse JSON: %w", number, err)
	}

	return fmt.Sprintf("%s\n\n%s\n\nSource: %s", issue.Title, issue.Body, issue.URL), nil
}

// defaultWorkflowFile is the fallback when neither -workflow nor a
// registered repository's default_workflow is set.
const defaultWorkflowFile = "workflows/issue-to-pr-claude-only.yaml"

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

// generateRunID builds a readable, collision-resistant Run id — unlike
// cmd/smoketest's fixed scenario IDs (deliberately reused so `make
// smoketest` can wipe and recreate them every run), a real submitted Task
// must never collide with a prior submission, so this always includes a
// timestamp.
func generateRunID(repoName string, githubIssue int) string {
	if githubIssue != 0 {
		return fmt.Sprintf("%s-issue-%d-%d", repoName, githubIssue, time.Now().Unix())
	}
	return fmt.Sprintf("%s-task-%d", repoName, time.Now().Unix())
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
