// Package taskintake is the real Task/Run entry point's shared core: given
// a resolved repo and a task description (or a GitHub issue to fetch one
// from), record a Task (internal/backlog.InsertHumanTask) and immediately
// start a real Run against it. There's no decoupled scheduler (docs/00's
// "bespoke scheduling engine" deferral), so submitting a Task and starting
// its Run are one action, not two — see Submit.
//
// Extracted from cmd/submittask so cmd/controlplane's Work (Task) section
// calls the exact same code a human runs from the CLI — two entry points,
// one implementation, never two copies to drift apart. Each caller keeps
// its own UI-specific concerns (CLI flag parsing and error messages for
// submittask; JSON request/response and a real-cost confirm dialog for
// controlplane) and delegates the actual submit-and-run mechanics here.
package taskintake

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	"go.temporal.io/sdk/client"

	"factory/internal/activities/gitops"
	"factory/internal/backlog"
	"factory/internal/conductor"
	"factory/internal/workflowdef"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultWorkflowFile is the fallback when neither a caller-supplied
// workflow file nor a registered repository's default_workflow is set.
// Claude-only because Coder/Reviewer in doc 02's reference definition
// (issue-to-pr-standard) are Codex/Copilot CLI, and this deployment only
// has Claude Code credits — see that file's own doc comment.
const DefaultWorkflowFile = "workflows/issue-to-pr-claude-only.yaml"

// Params is everything Submit needs beyond its Deps. Repo must already be
// fully resolved (name, clone URL, test command) — merging an explicit
// override with a registered repository's defaults is a caller-specific
// concern (see cmd/submittask's resolveRepoConfig), not this package's.
type Params struct {
	Repo         conductor.Repo
	WorkflowFile string // "" means DefaultWorkflowFile

	// Exactly one of Description/GitHubIssue must be set. GitHubIssue
	// fetches the issue's title/body via `gh issue view` and uses that as
	// the task description instead.
	Description string
	GitHubIssue int

	// RunIDOverride replaces the generated Run id when set — mainly for
	// callers with their own naming convention or that need idempotent
	// retries under a fixed id.
	RunIDOverride string
}

// Deps are Submit's infrastructure dependencies — a real Postgres pool
// and Temporal client, not interfaces to fake: this package's whole job
// is doing the real thing, same as internal/backlog and internal/harness.
type Deps struct {
	Pool           *pgxpool.Pool
	TemporalClient client.Client
	TaskQueue      string
	HarnessLimits  map[string]int
}

// Result is what a caller shows a human after a successful Submit.
type Result struct {
	TaskID   string
	RunID    string
	Workflow string

	// AttachRunWarning is non-nil if the Run started successfully but
	// recording its id back onto the Task row failed — the Run is real
	// and running regardless; this only means backlog_tasks won't show
	// its run_id. Callers should surface it as a warning, not an error.
	AttachRunWarning error
}

// Submit records a Task and starts a real Run against it. Costs real API
// credits the moment it returns successfully — every caller is
// responsible for making that cost visible to whoever triggers this
// (cmd/submittask's doc comment; cmd/controlplane's UI confirm dialog).
func Submit(ctx context.Context, deps Deps, params Params) (Result, error) {
	if (params.GitHubIssue == 0) == (params.Description == "") {
		return Result{}, fmt.Errorf("taskintake: exactly one of Description or GitHubIssue is required")
	}
	if params.Repo.TestCommand == "" {
		return Result{}, fmt.Errorf("taskintake: repo %q has no test_command set — VERIFYING needs a real command", params.Repo.Name)
	}

	workflowFile := params.WorkflowFile
	if workflowFile == "" {
		workflowFile = DefaultWorkflowFile
	}

	repoSlug, err := gitops.GitHubSlug(params.Repo.CloneURL)
	if err != nil {
		return Result{}, err
	}

	description := params.Description
	if params.GitHubIssue != 0 {
		description, err = fetchGitHubIssue(repoSlug, params.GitHubIssue)
		if err != nil {
			return Result{}, err
		}
	}

	def, err := parseAndValidateWorkflowFile(workflowFile)
	if err != nil {
		return Result{}, fmt.Errorf("taskintake: %w", err)
	}

	taskID, err := backlog.InsertHumanTask(ctx, deps.Pool, repoSlug, def.Workflow, description)
	if err != nil {
		return Result{}, err
	}

	runID := params.RunIDOverride
	if runID == "" {
		runID = generateRunID(params.Repo.Name, params.GitHubIssue)
	}

	// Fire-and-forget: a real Run (real harness calls, possibly a full
	// verify/review loop) can run long. There's nothing this call needs to
	// do once the Run is durably started — see cmd/runsview for progress,
	// or the Temporal UI directly.
	run, err := deps.TemporalClient.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        runID,
		TaskQueue: deps.TaskQueue,
	}, conductor.RunWorkflow, conductor.RunInput{
		Definition:     *def,
		InitialContext: map[string]any{"task_description": description},
		Repo:           params.Repo,
		HarnessLimits:  deps.HarnessLimits,
	})
	if err != nil {
		return Result{}, fmt.Errorf("taskintake: ExecuteWorkflow: %w", err)
	}

	result := Result{TaskID: taskID, RunID: run.GetID(), Workflow: def.Workflow}
	if err := backlog.AttachRun(ctx, deps.Pool, taskID, run.GetID()); err != nil {
		result.AttachRunWarning = err
	}
	return result, nil
}

// parseAndValidateWorkflowFile reads, parses, and validates a Workflow
// Definition file — the same fail-loud-before-doing-anything-else check
// cmd/submittask ran inline before this package existed.
func parseAndValidateWorkflowFile(path string) (*workflowdef.Definition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read workflow %s: %w", path, err)
	}
	def, err := workflowdef.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse workflow %s: %w", path, err)
	}
	if errs := workflowdef.Validate(def); len(errs) != 0 {
		return nil, fmt.Errorf("workflow %s failed validation:\n%s", path, errs.Error())
	}
	return def, nil
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
