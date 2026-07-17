package worktree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type RestoreOutcome struct {
	CheckedOut         bool
	StashRef           string
	StashSkippedReason string
}

func Head(ctx context.Context, cwd string) (string, error) {
	head, err := gitOutput(ctx, cwd, "rev-parse", "HEAD^{commit}")
	return strings.TrimSpace(head), err
}

// RestoreCommit preserves dirty work before checking out a session's recorded
// HEAD. Like the reference implementation, a failed local checkout retries
// after fetching origin and reports the outcome instead of hiding the failure.
func RestoreCommit(ctx context.Context, cwd, target, sessionID string) RestoreOutcome {
	current, err := Head(ctx, cwd)
	if err == nil && current == target {
		return RestoreOutcome{CheckedOut: true}
	}
	outcome := RestoreOutcome{}
	status, _ := gitOutput(ctx, cwd, "status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		if reason := inProgressReason(ctx, cwd); reason != "" {
			outcome.StashSkippedReason = reason
		} else {
			message := fmt.Sprintf("grok: pre-restore-code %s %s", sessionID, time.Now().UTC().Format(time.RFC3339))
			if _, err := gitOutput(ctx, cwd, "stash", "push", "--include-untracked", "-m", message); err != nil {
				outcome.StashSkippedReason = "git stash failed: " + err.Error()
			} else if ref, err := gitOutput(ctx, cwd, "rev-parse", "stash@{0}"); err == nil {
				outcome.StashRef = strings.TrimSpace(ref)
			} else {
				outcome.StashSkippedReason = "git rev-parse stash@{0} failed"
			}
		}
	}
	if _, err := gitOutput(ctx, cwd, "checkout", target); err == nil {
		outcome.CheckedOut = true
		return outcome
	}
	if _, err := gitOutput(ctx, cwd, "fetch", "origin"); err == nil {
		_, err = gitOutput(ctx, cwd, "checkout", target)
		outcome.CheckedOut = err == nil
	}
	return outcome
}

func RestoreSummary(target string, outcome RestoreOutcome) (restored bool, summary, degree string) {
	if !outcome.CheckedOut {
		summary = "restore aborted (checkout failed)"
	} else {
		short := target
		if len(short) > 8 {
			short = short[:8]
		}
		summary = fmt.Sprintf("checked out %s (session registry disabled - staged/unstaged/untracked not restored)", short)
		restored, degree = true, "head_only"
	}
	if outcome.StashRef != "" {
		summary += "; saved your dirty changes to stash " + outcome.StashRef
	}
	if outcome.StashSkippedReason != "" {
		summary += "; stash skipped: " + outcome.StashSkippedReason
	}
	return
}

func inProgressReason(ctx context.Context, cwd string) string {
	dir, err := gitOutput(ctx, cwd, "rev-parse", "--git-dir")
	if err != nil {
		return "could not resolve git directory"
	}
	dir = strings.TrimSpace(dir)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(cwd, dir)
	}
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "REBASE_HEAD", "BISECT_LOG"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return "in-progress " + name + " - refusing to stash to preserve operation state"
		}
	}
	return ""
}
