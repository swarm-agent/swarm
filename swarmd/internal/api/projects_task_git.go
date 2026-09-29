package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/gitstatus"
	"swarm/packages/swarmd/internal/sessionreview"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// inspectTaskGitState shares the sidebar's patch-equivalence and resolved-integration
// classification. Only the captured source checkout is an admissible destination.
func inspectTaskGitState(task pebblestore.ProjectTaskRecord, db *pebblestore.SessionStore) taskGitState {
	res := taskGitState{worktreeBranch: task.WorktreeBranch, worktreeName: task.WorktreeName, gitStatus: "unknown"}
	if db == nil || task.SessionID == "" || task.Archived || task.Status == "pending_approval" || task.Status == "planning" || task.Status == "queued" || task.Agent == "image" || task.Agent == "video" || task.Agent == "sound" || task.Agent == "audio" {
		return res
	}
	session, found, err := db.GetSession(task.SessionID)
	if err != nil || !found || !session.WorktreeEnabled || session.WorktreeRootPath == "" || session.WorktreeBranch == "" || session.WorktreeBaseBranch == "" || (task.AccountID != "" && task.AccountID != session.AccountScopeID) || (task.WorktreeBranch != "" && task.WorktreeBranch != session.WorktreeBranch) || (task.BaseBranch != "" && task.BaseBranch != session.WorktreeBaseBranch) {
		return res
	}
	res.worktreeBranch = session.WorktreeBranch
	res.worktreeName = strings.TrimPrefix(strings.TrimPrefix(res.worktreeBranch, "agent/"), "worktree/")
	source := strings.TrimSpace(sessionsV3MetadataString(session.Metadata, "swarm_v3_source_workspace_path"))
	base := strings.TrimSpace(sessionsV3MetadataString(session.Metadata, "base_commit"))
	if source == "" || base == "" || source == session.WorktreeRootPath || (task.SourceWorkspace.Path != "" && task.SourceWorkspace.Path != source) || (task.BaseCommit != "" && task.BaseCommit != base) {
		return res
	}
	res.baseBranch = session.WorktreeBaseBranch
	res.baseCommit = base
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	checkout, err := gitstatus.SnapshotForPath(ctx, source, gitstatus.Options{})
	if err != nil || !checkout.HasGit || checkout.Branch != res.baseBranch || checkout.HeadOID == "" {
		return res
	}
	sourceWatch, err := gitstatus.ResolveWatchPaths(ctx, source)
	if err != nil {
		return res
	}
	childWatch, err := gitstatus.ResolveWatchPaths(ctx, session.WorktreeRootPath)
	if err != nil || gitstatus.NormalizePath(sourceWatch.CommonDir) != gitstatus.NormalizePath(childWatch.CommonDir) || gitstatus.NormalizePath(sourceWatch.RepoRoot) == gitstatus.NormalizePath(childWatch.RepoRoot) {
		return res
	}
	child, err := gitstatus.SnapshotForPath(ctx, session.WorktreeRootPath, gitstatus.Options{})
	if err != nil || !child.HasGit || child.Branch != session.WorktreeBranch || child.HeadOID == "" {
		return res
	}
	res.dirtyCount = child.DirtyCount
	res.isDirty = !child.Clean
	if child.HeadOID == base {
		res.gitStatus = "clean"
		if res.isDirty {
			res.gitStatus = "dirty"
			res.actionNeeded = fmt.Sprintf("Action Needed: %d modified file(s) waiting to be committed.", res.dirtyCount)
		}
		return res
	}
	classification := sessionreview.ClassifySnapshotAgainstTarget(ctx, sessionreview.ExecGitRunner{}, session, child, time.Now(), sessionreview.DefaultGracePeriod, res.baseBranch)
	switch classification.Reason {
	case "clean_and_integrated":
		res.gitStatus = "clean"
		res.isIntegrated = true
		res.actionNeeded = fmt.Sprintf("No Action Required: Integrated into %s", res.baseBranch)
	case "commits_missing_from_target":
		res.gitStatus = "diverged"
		res.unintegratedCommits = classification.MissingCommits
		res.actionNeeded = fmt.Sprintf("Action Needed: %d unintegrated commit(s) on %s ready to integrate.", res.unintegratedCommits, res.worktreeBranch)
	case "uncommitted_work":
		res.gitStatus = "dirty"
		res.actionNeeded = fmt.Sprintf("Action Needed: %d modified file(s) waiting to be committed.", res.dirtyCount)
	}
	return res
}

// reconcileTaskGitState projects verified Git observations into the response only.
// Reads must never publish project.updated and invalidate their own consumers.
func reconcileTaskGitState(db *pebblestore.SessionStore, task *pebblestore.ProjectTaskRecord) error {
	if task == nil || db == nil || task.SessionID == "" || task.Archived || task.Status == "pending_approval" || task.Status == "planning" || task.Status == "queued" || task.Agent == "image" || task.Agent == "video" || task.Agent == "sound" || task.Agent == "audio" {
		return nil
	}
	state := inspectTaskGitState(*task, db)
	if state.gitStatus == "unknown" {
		// An unavailable checkout must not confirm stale integration or pending work.
		task.GitStatus = "unknown"
		task.IsIntegrated = false
		task.UnintegratedCommits = 0
		return nil
	}
	if state.isIntegrated && (task.TaskProgramID != "" || task.TaskProgram != nil) {
		programID := task.TaskProgramID
		if programID == "" && task.TaskProgram != nil {
			programID = task.TaskProgram.ID
		}
		program, found, err := db.GetTaskProgram(task.SessionID, programID)
		if err != nil || !found || program.State != pebblestore.TaskProgramStateCompleted {
			state.isIntegrated = false
			state.gitStatus = "unknown"
			state.actionNeeded = "Task Program is not fully completed and promoted to the captured target"
		}
	}
	apply := func(t *pebblestore.ProjectTaskRecord) {
		t.WorktreeBranch, t.WorktreeName, t.BaseBranch, t.BaseCommit = state.worktreeBranch, state.worktreeName, state.baseBranch, state.baseCommit
		t.GitStatus, t.UnintegratedCommits, t.IsIntegrated = state.gitStatus, state.unintegratedCommits, state.isIntegrated
		t.IsDirty, t.DirtyCount = state.isDirty, state.dirtyCount
		t.BehindCommits, t.SyncWarning, t.DiffSummary = state.behindCommits, state.syncWarning, state.diffSummary
		if state.actionNeeded != "" || t.IsIntegrated || t.Integration != nil {
			t.ActionNeeded = state.actionNeeded
		}
		if t.IsIntegrated && t.Status != "rejected" && t.Status != "in_progress" && task.Status != "in_progress" {
			t.Status = "completed"
		} else if !t.IsIntegrated && t.Status == "completed" && t.Integration != nil {
			t.Status = "needs_review"
		}
	}
	apply(task)
	return nil
}
