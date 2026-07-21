package sessionreview

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/gitstatus"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const DefaultGracePeriod = time.Hour

type CommitJob struct {
	BatchID      string `json:"batch_id"`
	Status       string `json:"status"`
	RunSessionID string `json:"run_session_id,omitempty"`
	CommitHash   string `json:"commit_hash,omitempty"`
	Error        string `json:"error,omitempty"`
	UpdatedAt    int64  `json:"updated_at"`
}

type Classification struct {
	SessionID         string     `json:"session_id"`
	Title             string     `json:"title"`
	UpdatedAt         int64      `json:"updated_at"`
	WorktreeBranch    string     `json:"worktree_branch,omitempty"`
	WorktreePath      string     `json:"worktree_path,omitempty"`
	TargetBranch      string     `json:"target_branch,omitempty"`
	Classification    string     `json:"classification"`
	Reason            string     `json:"reason"`
	DirtyCount        int        `json:"dirty_count,omitempty"`
	MissingCommits    int        `json:"missing_commit_count,omitempty"`
	Equivalent        int        `json:"equivalent_commit_count,omitempty"`
	DoneAt            int64      `json:"done_at,omitempty"`
	ArchiveAfter      int64      `json:"archive_after,omitempty"`
	ArchiveReady      bool       `json:"archive_ready"`
	CurrentCheckout   bool       `json:"current_checkout,omitempty"`
	CommitEligible    bool       `json:"commit_eligible,omitempty"`
	IntegrateEligible bool       `json:"integrate_eligible,omitempty"`
	CommitJob         *CommitJob `json:"commit_job,omitempty"`
}

type GitRunner interface {
	Run(ctx context.Context, dir string, args ...string) (string, error)
}

type ExecGitRunner struct{}

func (ExecGitRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", errors.New(strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func Classify(ctx context.Context, runner GitRunner, session pebblestore.SessionSnapshot, now time.Time, grace time.Duration) Classification {
	return ClassifyAgainstTarget(ctx, runner, session, now, grace, "")
}

// ClassifyAgainstTarget compares a managed worktree with an explicit checkout branch when one is available.
// This lets callers opened on a repository's primary checkout review sibling worktrees even when older
// session records do not contain worktree_base_branch metadata.
func ClassifyAgainstTarget(ctx context.Context, runner GitRunner, session pebblestore.SessionSnapshot, now time.Time, grace time.Duration, targetBranch string) Classification {
	worktreePath := strings.TrimSpace(session.WorktreeRootPath)
	result := Classification{
		SessionID:      strings.TrimSpace(session.ID),
		Title:          strings.TrimSpace(session.Title),
		UpdatedAt:      session.UpdatedAt,
		WorktreeBranch: strings.TrimSpace(session.WorktreeBranch),
		WorktreePath:   worktreePath,
		TargetBranch:   firstNonEmpty(targetBranch, session.WorktreeBaseBranch),
	}
	if grace <= 0 {
		grace = DefaultGracePeriod
	}
	if !session.WorktreeEnabled || worktreePath == "" || result.WorktreeBranch == "" || result.TargetBranch == "" {
		result.Classification = "retained"
		result.Reason = "managed_worktree_metadata_missing"
		return result
	}

	snapshot, err := gitstatus.SnapshotForPath(ctx, worktreePath, gitstatus.Options{})
	if err != nil {
		result.Classification = "retained"
		result.Reason = "worktree_unavailable"
		return result
	}
	return classifySnapshotAgainstTarget(ctx, runner, session, snapshot, now, grace, result.TargetBranch)
}

func ClassifySnapshot(ctx context.Context, runner GitRunner, session pebblestore.SessionSnapshot, snapshot gitstatus.Snapshot, now time.Time, grace time.Duration) Classification {
	return ClassifySnapshotAgainstTarget(ctx, runner, session, snapshot, now, grace, session.WorktreeBaseBranch)
}

// ClassifySnapshotAgainstTarget classifies an already-collected snapshot against an
// explicit target branch. Callers that resolve repository/worktree identity in bulk
// can use this to avoid repeating rev-parse queries for every session.
func ClassifySnapshotAgainstTarget(ctx context.Context, runner GitRunner, session pebblestore.SessionSnapshot, snapshot gitstatus.Snapshot, now time.Time, grace time.Duration, targetBranch string) Classification {
	return classifySnapshotAgainstTarget(ctx, runner, session, snapshot, now, grace, targetBranch)
}

// ClassifyCurrentCheckout handles regular sessions that run directly in the checkout being
// reviewed. They have no sibling worktree to integrate, so cleanliness is the archive gate.
// Dirty checkouts remain retained and may be committed only through the separate Git commit API.
func ClassifyCurrentCheckout(session pebblestore.SessionSnapshot, snapshot gitstatus.Snapshot, now time.Time, grace time.Duration) Classification {
	result := Classification{
		SessionID:       strings.TrimSpace(session.ID),
		Title:           strings.TrimSpace(session.Title),
		UpdatedAt:       session.UpdatedAt,
		WorktreeBranch:  strings.TrimSpace(snapshot.Branch),
		WorktreePath:    strings.TrimSpace(snapshot.WorkspacePath),
		TargetBranch:    strings.TrimSpace(snapshot.Branch),
		CurrentCheckout: true,
	}
	if grace <= 0 {
		grace = DefaultGracePeriod
	}
	if !snapshot.HasGit || strings.TrimSpace(snapshot.HeadOID) == "" {
		result.Classification = "retained"
		result.Reason = "worktree_unavailable"
		return result
	}
	result.DirtyCount = snapshot.DirtyCount
	if !snapshot.Clean {
		result.Classification = "retained"
		result.Reason = "current_checkout_uncommitted_work"
		result.CommitEligible = snapshot.DirtyCount > 0 && snapshot.ConflictCount == 0
		return result
	}
	result.Classification = "done"
	result.Reason = "current_checkout_clean"
	result.ArchiveAfter = session.UpdatedAt + grace.Milliseconds()
	result.ArchiveReady = now.UnixMilli() >= result.ArchiveAfter
	return result
}

func classifySnapshotAgainstTarget(ctx context.Context, runner GitRunner, session pebblestore.SessionSnapshot, snapshot gitstatus.Snapshot, now time.Time, grace time.Duration, targetBranch string) Classification {
	result := Classification{
		SessionID:      strings.TrimSpace(session.ID),
		Title:          strings.TrimSpace(session.Title),
		UpdatedAt:      session.UpdatedAt,
		WorktreeBranch: strings.TrimSpace(session.WorktreeBranch),
		WorktreePath:   strings.TrimSpace(session.WorktreeRootPath),
		TargetBranch:   strings.TrimSpace(targetBranch),
	}
	if grace <= 0 {
		grace = DefaultGracePeriod
	}
	if !session.WorktreeEnabled || strings.TrimSpace(session.WorktreeRootPath) == "" || result.WorktreeBranch == "" || result.TargetBranch == "" {
		result.Classification = "retained"
		result.Reason = "managed_worktree_metadata_missing"
		return result
	}
	if !snapshot.HasGit || strings.TrimSpace(snapshot.HeadOID) == "" {
		result.Classification = "retained"
		result.Reason = "worktree_unavailable"
		return result
	}
	result.DirtyCount = snapshot.DirtyCount
	if !snapshot.Clean {
		result.Classification = "retained"
		result.Reason = "uncommitted_work"
		result.CommitEligible = snapshot.DirtyCount > 0 && snapshot.ConflictCount == 0
		return result
	}

	cherry, err := runner.Run(ctx, session.WorktreeRootPath, "cherry", result.TargetBranch, snapshot.HeadOID)
	if err != nil {
		result.Classification = "retained"
		result.Reason = "target_branch_unavailable"
		return result
	}
	for _, line := range strings.Split(strings.TrimSpace(cherry), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if fields[0] == "-" {
			result.Equivalent++
		} else if fields[0] == "+" {
			result.MissingCommits++
		}
	}
	if result.MissingCommits > 0 {
		result.Classification = "retained"
		result.Reason = "commits_missing_from_target"
		result.IntegrateEligible = true
		return result
	}

	result.Classification = "done"
	result.Reason = "clean_and_integrated"
	result.ArchiveAfter = session.UpdatedAt + grace.Milliseconds()
	result.ArchiveReady = now.UnixMilli() >= result.ArchiveAfter
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func ParseGraceHours(raw string) time.Duration {
	hours, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || hours < 1 {
		return DefaultGracePeriod
	}
	if hours > 24*30 {
		hours = 24 * 30
	}
	return time.Duration(hours) * time.Hour
}
