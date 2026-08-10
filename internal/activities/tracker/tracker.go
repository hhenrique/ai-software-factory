// Package tracker holds the Tracker adapter (docs/08-tracking-integration.md):
// posts read-only progress commentary onto whatever tool a Run's PR or a
// Task's source lives in. Same division of responsibility doc03 already
// established for harness adapters — an adapter is the only place
// tool-specific API/CLI shape may live, never internal/conductor.
//
// Activities.PostComment backs conductor.TrackerPostCommentActivityName,
// dispatched from internal/conductor's recordTransition/recordFailure
// choke point (docs/08's step 3) as a second, best-effort sink alongside
// event recording — never from a Workflow Definition step.
package tracker

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"factory/internal/conductor"
)

// Ref identifies where a comment should be posted: a PR, a source issue,
// or (once the Aha! adapter exists) an Aha! idea.
type Ref struct {
	Kind string // "github_pr" | "github_issue" | "aha_idea"
	Ref  string // PR/issue URL, Aha! idea id
}

// Tracker posts a read-only progress comment onto some external tool. One
// implementation per backend, same shape as the harness adapter contract
// (doc03) — never bidirectional, never anything beyond PostComment.
type Tracker interface {
	PostComment(ctx context.Context, target Ref, body string) error
}

// GitHubTracker shells out to `gh pr comment` / `gh issue comment` — the
// same established pattern internal/activities/pr already uses: gh already
// owns GitHub auth, so there's no new credential handling to build. Both
// subcommands accept a full PR/issue URL as the target, so no --repo flag
// or working directory is needed — target.Ref alone is enough context.
type GitHubTracker struct{}

// PostComment posts body as a comment on target, dispatching to the gh
// subcommand target.Kind names.
func (GitHubTracker) PostComment(ctx context.Context, target Ref, body string) error {
	var args []string
	switch target.Kind {
	case "github_pr":
		args = []string{"pr", "comment", target.Ref, "--body", body}
	case "github_issue":
		args = []string{"issue", "comment", target.Ref, "--body", body}
	default:
		return fmt.Errorf("tracker: github: unsupported target kind %q", target.Kind)
	}

	cmd := exec.CommandContext(ctx, "gh", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Activities holds the projection-store connection pool — needed only to
// record a post failure as a queryable fact (doc08: "best-effort must not
// mean silent"), never to post the comment itself.
type Activities struct {
	Pool *pgxpool.Pool
}

// Registrations maps conductor.TrackerPostCommentActivityName to this
// struct's method — called directly by RunWorkflow's recordTransition,
// not dispatched via a Workflow Definition step, same as
// internal/eventlog's RecordEvent.
func (a *Activities) Registrations() map[string]any {
	return map[string]any{
		conductor.TrackerPostCommentActivityName: a.PostComment,
	}
}

// PostComment posts in.Body via GitHubTracker (the only backend built so
// far — an unresolvable in.TargetKind, e.g. "aha_idea", surfaces as the
// same kind of error a real posting failure would). On failure, records a
// row in tracker_comment_failures before returning the error so a pattern
// of dropped comments (a revoked gh credential, ...) is queryable, not
// only visible as a Temporal worker log line — the caller (recordTransition)
// never fails the Run over this error regardless.
func (a *Activities) PostComment(ctx context.Context, in conductor.TrackerCommentInput) error {
	postErr := GitHubTracker{}.PostComment(ctx, Ref{Kind: in.TargetKind, Ref: in.TargetRef}, in.Body)
	if postErr == nil {
		return nil
	}

	if _, err := a.Pool.Exec(ctx, `
		INSERT INTO tracker_comment_failures (run_id, target_kind, target_ref, error)
		VALUES ($1, $2, $3, $4)
	`, in.RunID, in.TargetKind, in.TargetRef, postErr.Error()); err != nil {
		return fmt.Errorf("%w (also failed to record tracker failure: %v)", postErr, err)
	}
	return postErr
}
