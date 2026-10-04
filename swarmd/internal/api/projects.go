package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodel"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	taskrouter "swarm/packages/swarmd/internal/taskrouter"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/uisettings"
	"swarm/packages/swarmd/internal/videogen"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

const ProjectsPath = "/v3/projects"

func (s *Server) resolveProjectThemeID(accountScopeID, themeID string) (string, error) {
	if strings.TrimSpace(themeID) == "" {
		return "", nil
	}
	if s.uiSettings == nil {
		return "", errors.New("account theme catalog is unavailable")
	}
	settings, err := s.uiSettings.GetForAccount(accountScopeID)
	if err != nil {
		return "", fmt.Errorf("read account theme catalog: %w", err)
	}
	return uisettings.ResolveProjectThemeID(settings, themeID)
}

type taskGitState struct {
	deliveryAssessment  *pebblestore.TaskDeliveryAssessment
	worktreeBranch      string
	worktreeName        string
	baseBranch          string
	baseCommit          string
	gitStatus           string
	unintegratedCommits int
	behindCommits       int
	isIntegrated        bool
	diffSummary         string
	isDirty             bool
	dirtyCount          int
	actionNeeded        string
	syncWarning         string
}

// inspectTaskGitStateLegacy is retained only for historical reference; task reads use
// the captured-checkout classifier in projects_task_git.go.
func inspectTaskGitStateLegacy(task pebblestore.ProjectTaskRecord, db *pebblestore.SessionStore) taskGitState {
	res := taskGitState{
		worktreeBranch: task.WorktreeBranch,
		worktreeName:   task.WorktreeName,
		baseBranch:     task.BaseBranch,
		gitStatus:      "unknown",
	}
	if res.worktreeBranch == "" || res.worktreeBranch == "main" || res.worktreeBranch == "dev" || res.worktreeBranch == "master" {
		res.worktreeBranch, res.worktreeName = pebblestore.MakeWorktreeBranch(task.Title, task.Description)
	}
	if res.worktreeName == "" {
		res.worktreeName = strings.TrimPrefix(res.worktreeBranch, "agent/")
		res.worktreeName = strings.TrimPrefix(res.worktreeName, "worktree/")
	}
	if res.baseBranch == "" {
		res.baseBranch = "main"
	}

	// 1. Don't show or inspect git state for pending approval, planning, or queued tasks
	if task.Status == "pending_approval" || task.Status == "planning" || task.Status == "queued" {
		return res
	}
	// 2. Media tasks don't have git tracking
	if task.Agent == "image" || task.Agent == "video" || task.Agent == "sound" || task.Agent == "audio" {
		return res
	}

	// 3. Resolve session and target path
	var sess *pebblestore.SessionSnapshot
	if task.SessionID != "" && db != nil {
		if s, found, _ := db.GetSession(task.SessionID); found {
			sess = &s
		}
	}

	if sess != nil && sess.WorktreeEnabled {
		if strings.TrimSpace(sess.WorktreeBranch) != "" {
			res.worktreeBranch = strings.TrimSpace(sess.WorktreeBranch)
			res.worktreeName = strings.TrimPrefix(strings.TrimPrefix(res.worktreeBranch, "agent/"), "worktree/")
		}
		if strings.TrimSpace(sess.WorktreeBaseBranch) != "" {
			res.baseBranch = strings.TrimSpace(sess.WorktreeBaseBranch)
		}
	}

	targetPath := strings.TrimSpace(task.WorkspacePath)
	if sess != nil && sess.WorktreeEnabled && strings.TrimSpace(sess.WorktreeRootPath) != "" {
		targetPath = strings.TrimSpace(sess.WorktreeRootPath)
	}
	if targetPath == "" {
		return res
	}
	if fi, err := os.Stat(targetPath); err != nil || !fi.IsDir() {
		return res
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// 4. Resolve base branch
	if sess != nil && strings.TrimSpace(sess.WorktreeBaseBranch) != "" {
		res.baseBranch = strings.TrimSpace(sess.WorktreeBaseBranch)
	} else {
		// Test if dev exists
		if err := exec.CommandContext(ctx, "git", "-C", targetPath, "rev-parse", "--verify", "dev").Run(); err == nil {
			res.baseBranch = "dev"
		} else if err := exec.CommandContext(ctx, "git", "-C", targetPath, "rev-parse", "--verify", "origin/dev").Run(); err == nil {
			res.baseBranch = "dev"
		}
	}

	// 5. Inspect current HEAD branch and commit in targetPath
	cmdHead := exec.CommandContext(ctx, "git", "-C", targetPath, "rev-parse", "--abbrev-ref", "HEAD")
	headBranch := ""
	if out, err := cmdHead.Output(); err == nil {
		headBranch = strings.TrimSpace(string(out))
	}
	cmdHeadCommit := exec.CommandContext(ctx, "git", "-C", targetPath, "rev-parse", "HEAD")
	headCommit := ""
	if out, err := cmdHeadCommit.Output(); err == nil {
		headCommit = strings.TrimSpace(string(out))
	}

	isTaskWorktree := (sess != nil && sess.WorktreeEnabled && sess.WorktreeRootPath == targetPath) ||
		(headBranch != "" && headBranch != "main" && headBranch != "dev" && headBranch != "master" && (headBranch == res.worktreeBranch || strings.HasPrefix(headBranch, "agent/")))

	if !isTaskWorktree {
		// targetPath is the main repo or non-worktree checkout — do NOT attribute its dirty status or diff to this task!
		return res
	}

	// 6. Dirty status
	cmdDirty := exec.CommandContext(ctx, "git", "-C", targetPath, "status", "--porcelain")
	if out, err := cmdDirty.Output(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		for _, l := range lines {
			if strings.TrimSpace(l) != "" {
				res.dirtyCount++
			}
		}
		res.isDirty = res.dirtyCount > 0
	}

	// 7. Resolve base commit of worktree (fork commit)
	baseCommit := strings.TrimSpace(res.baseCommit)
	if baseCommit == "" && sess != nil && sess.Metadata != nil {
		if bc, ok := sess.Metadata["base_commit"].(string); ok {
			baseCommit = strings.TrimSpace(bc)
		}
	}
	if baseCommit == "" {
		// Check reflog to find where the branch was created
		cmdReflog := exec.CommandContext(ctx, "git", "-C", targetPath, "log", "-g", "--format=%H", "--reverse", fmt.Sprintf("refs/heads/%s", res.worktreeBranch))
		if out, err := cmdReflog.Output(); err == nil {
			fields := strings.Fields(strings.TrimSpace(string(out)))
			if len(fields) > 0 {
				baseCommit = fields[0]
			}
		}
	}
	if baseCommit == "" {
		// Fallback to merge-base between baseBranch and HEAD
		cmdMb := exec.CommandContext(ctx, "git", "-C", targetPath, "merge-base", res.baseBranch, "HEAD")
		if out, err := cmdMb.Output(); err == nil && len(bytes.TrimSpace(out)) > 0 {
			baseCommit = strings.TrimSpace(string(out))
		}
	}
	res.baseCommit = baseCommit

	// 8. Ahead / behind relative to baseBranch
	cmdRevList := exec.CommandContext(ctx, "git", "-C", targetPath, "rev-list", "--left-right", "--count", res.baseBranch+"...HEAD")
	if out, err := cmdRevList.Output(); err == nil {
		fields := strings.Fields(strings.TrimSpace(string(out)))
		if len(fields) >= 2 {
			fmt.Sscanf(fields[0], "%d", &res.behindCommits)
			fmt.Sscanf(fields[1], "%d", &res.unintegratedCommits)
		}
	} else {
		cmdRevOrigin := exec.CommandContext(ctx, "git", "-C", targetPath, "rev-list", "--left-right", "--count", "origin/"+res.baseBranch+"...HEAD")
		if out, err := cmdRevOrigin.Output(); err == nil {
			fields := strings.Fields(strings.TrimSpace(string(out)))
			if len(fields) >= 2 {
				fmt.Sscanf(fields[0], "%d", &res.behindCommits)
				fmt.Sscanf(fields[1], "%d", &res.unintegratedCommits)
			}
		}
	}

	// 9. Diff summary
	if res.unintegratedCommits > 0 || res.isDirty {
		cmdDiff := exec.CommandContext(ctx, "git", "-C", targetPath, "diff", "--shortstat", res.baseBranch+"...HEAD")
		if out, err := cmdDiff.Output(); err == nil && len(bytes.TrimSpace(out)) > 0 {
			res.diffSummary = string(bytes.TrimSpace(out))
		}
	}

	// 10. True Git Integration Check:
	// A worktree branch is ONLY integrated if:
	// a) It is clean (not dirty), AND
	// b) It actually made commits beyond its fork base commit (HEAD != baseCommit), AND
	// c) All those commits are ancestors of baseBranch (merge-base --is-ancestor HEAD baseBranch == 0),
	//    meaning unintegratedCommits == 0.
	// If the branch NEVER made any commits (HEAD == baseCommit), it is NOT integrated.
	hasBranchCommits := false
	if headCommit != "" && baseCommit != "" && headCommit != baseCommit {
		hasBranchCommits = true
	} else if headCommit != "" {
		// Double check via reflog if commits were made on the branch
		cmdRefLogCommits := exec.CommandContext(ctx, "git", "-C", targetPath, "log", "-g", "--format=%gs", fmt.Sprintf("refs/heads/%s", res.worktreeBranch))
		if out, err := cmdRefLogCommits.Output(); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "commit") {
					hasBranchCommits = true
					break
				}
			}
		}
	}

	if !res.isDirty && hasBranchCommits && res.unintegratedCommits == 0 && headCommit != "" {
		// Verify ancestry: is HEAD an ancestor of baseBranch?
		cmdAncestor := exec.CommandContext(ctx, "git", "-C", targetPath, "merge-base", "--is-ancestor", headCommit, res.baseBranch)
		if cmdAncestor.Run() == nil {
			res.isIntegrated = true
		} else {
			cmdAncestorOrigin := exec.CommandContext(ctx, "git", "-C", targetPath, "merge-base", "--is-ancestor", headCommit, "origin/"+res.baseBranch)
			if cmdAncestorOrigin.Run() == nil {
				res.isIntegrated = true
			}
		}
	}

	// 11. Sync Warning check (behind or out of sync)
	if res.behindCommits > 0 {
		res.syncWarning = fmt.Sprintf("Worktree is behind %s by %d commit(s) — rebase may be needed.", res.baseBranch, res.behindCommits)
	}

	// Also check if base branch in root workspace is behind upstream remote
	parentWs := task.WorkspacePath
	if sess != nil && sess.Metadata != nil {
		if src, ok := sess.Metadata["swarm_v3_source_workspace_path"].(string); ok && src != "" {
			parentWs = src
		}
	}
	if parentWs != "" && parentWs != targetPath {
		cmdBranchStatus := exec.CommandContext(ctx, "git", "-C", parentWs, "status", "--porcelain=v2", "--branch")
		if out, err := cmdBranchStatus.Output(); err == nil {
			outStr := string(out)
			for _, line := range strings.Split(outStr, "\n") {
				if strings.HasPrefix(line, "# branch.ab ") {
					var ahead, behind int
					if _, scanErr := fmt.Sscanf(line, "# branch.ab +%d -%d", &ahead, &behind); scanErr == nil {
						if behind > 0 {
							if res.syncWarning != "" {
								res.syncWarning = fmt.Sprintf("%s branch could be out of sync (behind upstream by %d commit(s)), and worktree is behind %s by %d commit(s).", res.baseBranch, behind, res.baseBranch, res.behindCommits)
							} else {
								res.syncWarning = fmt.Sprintf("%s branch could be out of sync (behind upstream remote by %d commit(s)).", res.baseBranch, behind)
							}
						}
					}
					break
				}
			}
		}
	}

	// 12. Action Needed
	if res.unintegratedCommits > 0 {
		res.actionNeeded = fmt.Sprintf("Action Needed: %d unintegrated commit(s) on %s ready to integrate.", res.unintegratedCommits, res.worktreeBranch)
	} else if res.isDirty {
		res.actionNeeded = fmt.Sprintf("Action Needed: %d modified file(s) waiting to be committed.", res.dirtyCount)
	} else if res.isIntegrated {
		res.actionNeeded = fmt.Sprintf("No Action Required: Integrated into %s", res.baseBranch)
	} else {
		res.actionNeeded = ""
	}

	// 13. Resolved Git Status
	if res.isDirty {
		res.gitStatus = "dirty"
	} else if res.unintegratedCommits > 0 {
		res.gitStatus = "diverged"
	} else if res.isIntegrated {
		res.gitStatus = "clean"
	} else {
		res.gitStatus = "clean"
	}

	return res
}

// syncTaskSessionState checks the live V3 session and plan for a task and transitions
// in_progress tasks to needs_review when the agent finishes execution, ensuring tasks
// never just flip to complete without review/integration, and allowing reopening.
type projectTaskLifecycleReader interface {
	pebblestore.ProjectTaskExecutionReader
	CurrentTaskProgram(*pebblestore.ProjectTaskRecord) (pebblestore.TaskProgramRecord, bool)
	ProjectTaskExecuting(*pebblestore.ProjectTaskRecord) bool
	ProjectTaskPlanUnfinished(*pebblestore.ProjectTaskRecord) bool
	ListRunIntents(string, int) ([]pebblestore.V3SessionRunIntent, error)
	ListPlans(string, int) ([]pebblestore.SessionPlanSnapshot, error)
}

func syncTaskSessionState(task *pebblestore.ProjectTaskRecord, db projectTaskLifecycleReader) {
	if task == nil || db == nil || task.Archived {
		return
	}
	// Direct video generation intentionally has no chat session. Its persisted
	// generation lifecycle is authoritative; router warnings are not failures.
	if task.Agent == "video" && task.SessionID == "" && task.TaskProgramID == "" && task.TaskProgram == nil {
		return
	}
	// Preserve manual task approval and queue ownership. Generated tasks
	// alone may reconcile their queue state from the worker execution session.
	if task.Status == "pending_approval" || (task.Status == "queued" && task.WorkerID == "") {
		return
	}

	// Live execution outranks workflow and Git labels, including nested programs.
	// Keep blocker/review detail available while the owning session repairs it.
	if prog, ok := db.CurrentTaskProgram(task); ok {
		task.TaskProgramStatus = &prog
	}
	if db.ProjectTaskExecuting(task) {
		task.Status = "in_progress"
		return
	}

	// Git delivery is an independent observation, never an execution result.
	// 2. If task was explicitly marked completed and not reopened, preserve completed
	if task.Status == "completed" {
		return
	}
	if task.Status == "rejected" {
		return
	}

	// 2a. Planning task check: check if planning session produced an active plan or concluded with failure
	if task.Status == "planning" {
		if task.SessionID != "" {
			// Publication owns pending_approval and its exact receipt binding.
			// Hydration must not manufacture a review from an unrelated active plan.
			runState, runFound, _ := db.GetV3SessionRunState(task.SessionID)
			if runFound && runState.AccountScopeID == task.AccountID && runState.RunID == task.ExecutionRunID() && !runState.Active && runState.Status != pebblestore.V3RunIntentPendingExecutor && runState.Status != pebblestore.V3RunIntentRunning {
				if pebblestore.IsPausedTaskPlanningRun(runState.Status, runState.BlockedReason) {
					return
				}
				switch runState.Status {
				case pebblestore.V3RunIntentCancelled, pebblestore.V3RunIntentFailed, pebblestore.V3RunIntentExpired, pebblestore.V3RunIntentInterrupted, pebblestore.V3RunIntentDispatchBlocked:
					task.Status = "failed"
					if runState.BlockedReason != "" {
						task.LastError = runState.BlockedReason
						task.ActionNeeded = fmt.Sprintf("Action Needed: Planning run failed (%s). Retry task.", runState.BlockedReason)
					}
					return
				case pebblestore.V3RunIntentCompleted:
					task.Status = "failed"
					task.LastError = "Planning run ended without publishing a durable task plan"
					task.ActionNeeded = "Action Needed: Planning run ended without a published plan. Retry planning."
					return
				}
			}
		}
		return
	}

	// A terminal owning-run failure outranks a successful or blocked subprogram.
	if state, found, err := db.GetV3SessionRunState(task.SessionID); err == nil && found && state.AccountScopeID == task.AccountID && (task.TaskProgramID != "" || task.TaskProgram != nil) {
		switch state.Status {
		case pebblestore.V3RunIntentFailed, pebblestore.V3RunIntentCancelled, pebblestore.V3RunIntentExpired, pebblestore.V3RunIntentInterrupted, pebblestore.V3RunIntentDispatchBlocked:
			task.Status = "failed"
			if state.BlockedReason != "" {
				task.LastError = state.BlockedReason
			}
			return
		}
	}

	// 2b. If task has a Task Program, sync status from TaskProgramRecord
	if task.TaskProgramID != "" || task.TaskProgram != nil {
		progID := task.TaskProgramID
		if progID == "" && task.TaskProgram != nil {
			progID = task.TaskProgram.ID
		}
		if progID != "" && task.SessionID != "" {
			if prog, ok := db.CurrentTaskProgram(task); ok {
				task.TaskProgramStatus = &prog
				if task.Status == "completed" || task.Status == "rejected" {
					return
				}
				switch prog.State {
				case pebblestore.TaskProgramStateRunning:
					// Scheduling state alone cannot prove a session is executing.
				case pebblestore.TaskProgramStateCompleted:
					if db.ProjectTaskPlanUnfinished(task) {
						break
					}
					if !task.IsIntegrated {
						task.Status = "needs_review"
						if task.ActionNeeded == "" || strings.HasPrefix(task.ActionNeeded, "Action Needed: 0") {
							task.ActionNeeded = "Action Needed: All task program jobs finished. Verify promotion into the captured target."
						}
					} else {
						task.Status = "completed"
					}
					return
				case pebblestore.TaskProgramStateBlocked:
					task.Status = "needs_review"
					if prog.Blocker != nil && prog.Blocker.Message != "" {
						task.ActionNeeded = prog.Blocker.Message
						task.LastError = prog.Blocker.Message
					}
					return
				case pebblestore.TaskProgramStateFailed, pebblestore.TaskProgramStateCancelled:
					task.Status = "failed"
					if prog.Blocker != nil && prog.Blocker.Message != "" {
						task.LastError = prog.Blocker.Message
					}
					return
				}
			}
		}
	}

	if task.SessionID == "" {
		if (task.TaskProgramID == "" && task.TaskProgram == nil) && (task.Status == "in_progress" || task.Status == "planning") {
			if task.RouterAlert != "" {
				task.Status = "failed"
				if task.LastError == "" {
					task.LastError = task.RouterAlert
				}
				task.ActionNeeded = fmt.Sprintf("Action Needed: Task routing failed (%s). Retry or archive task.", task.RouterAlert)
			}
		}
		return
	}
	sess, found, err := db.GetSession(task.SessionID)
	if err != nil || !found {
		return
	}

	// Account boundary: verify task and session belong to matching account scope
	if task.AccountID != "" && sess.AccountScopeID != "" && task.AccountID != sess.AccountScopeID {
		return
	}

	// Hydrate worktree metadata from session when unpopulated on task
	if sess.WorktreeEnabled {
		if (task.WorkspacePath == "" || task.WorkspacePath == ".") && strings.TrimSpace(sess.WorktreeRootPath) != "" {
			task.WorkspacePath = strings.TrimSpace(sess.WorktreeRootPath)
		}
		if task.WorktreeBranch == "" && strings.TrimSpace(sess.WorktreeBranch) != "" {
			task.WorktreeBranch = strings.TrimSpace(sess.WorktreeBranch)
		}
		if task.WorktreeName == "" && task.WorktreeBranch != "" {
			task.WorktreeName = strings.TrimPrefix(strings.TrimPrefix(task.WorktreeBranch, "agent/"), "worktree/")
		}
		if task.BaseBranch == "" && strings.TrimSpace(sess.WorktreeBaseBranch) != "" {
			task.BaseBranch = strings.TrimSpace(sess.WorktreeBaseBranch)
		}
		if task.BaseCommit == "" && sess.Metadata != nil {
			if bc, ok := sess.Metadata["base_commit"].(string); ok && strings.TrimSpace(bc) != "" {
				task.BaseCommit = strings.TrimSpace(bc)
			}
		}
	}

	// 3. Inspect canonical V3 run authority (active/terminal run intent in Pebble)
	runState, runFound, _ := db.GetV3SessionRunState(task.SessionID)
	if runFound && task.AccountID != "" && runState.AccountScopeID != "" && task.AccountID != runState.AccountScopeID {
		runFound = false
		runState = pebblestore.V3SessionRunState{}
	}
	if (!runFound || strings.TrimSpace(runState.RunID) == "") && db != nil {
		if intents, err := db.ListRunIntents(task.SessionID, 10); err == nil && len(intents) > 0 {
			latest := intents[len(intents)-1]
			for _, intent := range intents {
				if intent.UpdatedAt > latest.UpdatedAt || intent.EventSeq > latest.EventSeq {
					latest = intent
				}
			}
			if task.AccountID == "" || latest.AccountScopeID == "" || task.AccountID == latest.AccountScopeID {
				runState = pebblestore.V3SessionRunState{
					SessionID:      latest.SessionID,
					AccountScopeID: latest.AccountScopeID,
					RunID:          latest.RunID,
					Status:         latest.Status,
					Active:         latest.Status == pebblestore.V3RunIntentPendingExecutor || latest.Status == pebblestore.V3RunIntentRunning,
					BlockedReason:  latest.BlockedReason,
				}
				runFound = true
			}
		}
	}

	if runFound && strings.TrimSpace(runState.RunID) != "" {
		if runState.Status == pebblestore.V3RunIntentRunning {
			task.Status = "in_progress"
		} else if runState.Status == pebblestore.V3RunIntentPendingExecutor {
			// Preparation is not active execution, even if the previous card was running.
			task.Status = "queued"
		} else {
			// Run concluded:
			switch runState.Status {
			case pebblestore.V3RunIntentCompleted:
				if projectTaskDeclaredBlocker(task, sess, runState) {
					return
				}
				if task.PlanBinding != nil && task.PlanBinding.PlanID != "" {
					plan, found, err := db.GetPlan(task.SessionID, task.PlanBinding.PlanID)
					if err != nil || !found || plan.Document == nil || len(plan.Document.Checkpoints) == 0 {
						if task.Status == "in_progress" {
							task.Status = "blocked"
						}
						return
					}
					for _, checkpoint := range plan.Document.Checkpoints {
						if checkpoint.Status != "completed" {
							if task.Status == "in_progress" {
								task.Status = "blocked"
							}
							return
						}
					}
				}
				// In Swarm V3 orchestration, when an agent finishes execution, the task
				// transitions to needs_review for user review and git integration,
				// NEVER directly to completed! A successful explicit retry may finish
				// before a reader observes its intermediate in_progress state.
				if task.Status == "in_progress" || task.Status == "failed" || task.Status == "blocked" || (task.Status == "needs_review" && task.ActionNeeded == "Launching task-linked Swarm follow-up") {
					if task.Integration == nil || task.Integration.Error == "" {
						task.LastError = ""
					}
					task.Status = "needs_review"
					if task.ActionNeeded == "" || strings.HasPrefix(task.ActionNeeded, "Action Needed: 0") || task.ActionNeeded == "Executing reopened task" || task.ActionNeeded == "Launching task-linked Swarm follow-up" {
						if task.UnintegratedCommits > 0 {
							baseBranch := task.BaseBranch
							if baseBranch == "" {
								baseBranch = "captured target"
							}
							task.ActionNeeded = fmt.Sprintf("Action Needed: Review changes and integrate %d commit(s) into %s", task.UnintegratedCommits, baseBranch)
						} else {
							task.ActionNeeded = "Action Needed: Review agent deliverables and verify outcomes"
						}
					}
				}
			case pebblestore.V3RunIntentFailed, pebblestore.V3RunIntentCancelled, pebblestore.V3RunIntentExpired, pebblestore.V3RunIntentInterrupted, pebblestore.V3RunIntentDispatchBlocked:
				if task.Status == "in_progress" {
					task.Status = "failed"
					if runState.BlockedReason != "" && task.LastError == "" {
						task.LastError = runState.BlockedReason
					}
					if task.ActionNeeded == "" || strings.HasPrefix(task.ActionNeeded, "Action Needed: 0") || task.ActionNeeded == "Executing reopened task" || task.ActionNeeded == "Launching task-linked Swarm follow-up" {
						if runState.Status == pebblestore.V3RunIntentCancelled {
							task.ActionNeeded = "Action Needed: Run was cancelled. Retry or reassign task."
						} else if runState.BlockedReason != "" {
							task.ActionNeeded = fmt.Sprintf("Action Needed: Run failed (%s). Retry task.", runState.BlockedReason)
						} else {
							task.ActionNeeded = "Action Needed: Run failed. Review error and retry task."
						}
					}
				}
			}
		}
	} else if sess.Lifecycle != nil {
		// Legacy session lifecycle fallback
		if sess.Lifecycle.Active && (sess.Lifecycle.Phase == "running" || (sess.Lifecycle.Phase != "queued" && sess.Lifecycle.Phase != "preparing" && task.Status != "queued")) {
			task.Status = "in_progress"
		} else if sess.Lifecycle.Active {
			// In queued preparation; keep task queued.
		} else if sess.Lifecycle.Phase == "failed" || sess.Lifecycle.Phase == "error" {
			if task.Status == "in_progress" {
				task.Status = "failed"
				if sess.Lifecycle.Error != "" && task.LastError == "" {
					task.LastError = sess.Lifecycle.Error
				}
				if task.ActionNeeded == "" || strings.HasPrefix(task.ActionNeeded, "Action Needed: 0") || task.ActionNeeded == "Executing reopened task" || task.ActionNeeded == "Launching task-linked Swarm follow-up" {
					task.ActionNeeded = "Action Needed: Run failed. Review error and retry task."
				}
			}
		} else if sess.Lifecycle.Phase == "completed" || sess.Lifecycle.EndedAt > 0 || (!sess.Lifecycle.Active && sess.Lifecycle.Phase != "failed" && sess.Lifecycle.Phase != "error" && sess.MessageCount > 1) {
			if task.Status == "in_progress" {
				task.Status = "needs_review"
				if task.ActionNeeded == "" || strings.HasPrefix(task.ActionNeeded, "Action Needed: 0") || task.ActionNeeded == "Executing reopened task" || task.ActionNeeded == "Launching task-linked Swarm follow-up" {
					if task.UnintegratedCommits > 0 {
						baseBranch := task.BaseBranch
						if baseBranch == "" {
							baseBranch = "captured target"
						}
						task.ActionNeeded = fmt.Sprintf("Action Needed: Review changes and integrate %d commit(s) into %s", task.UnintegratedCommits, baseBranch)
					} else {
						task.ActionNeeded = "Action Needed: Review agent deliverables and verify outcomes"
					}
				}
			}
		}
	}
	// If neither canonical run state nor sess.Lifecycle indicates execution,
	// no execution has occurred yet: preserve current task.Status (e.g. in_progress) without guessing from MessageCount.

	// 4. Inspect session plans for waiting_review or needs_review status
	plans, _ := db.ListPlans(task.SessionID, 1)
	if len(plans) > 0 {
		plan := plans[0]
		if plan.Document != nil {
			if plan.Document.ExecutionState != nil && plan.Document.ExecutionState.Status == "waiting_review" {
				if task.Status == "in_progress" {
					task.Status = "needs_review"
				}
			}
			for _, cp := range plan.Document.Checkpoints {
				if cp.Status == "needs_review" && task.Status == "in_progress" {
					task.Status = "needs_review"
					break
				}
			}
		}
	}
}

// TaskRouteResult represents the synthesized plan, tier, and workspace scope for a task.
type TaskRouteResult = pebblestore.TaskRouteResult

// TaskModelPreview represents the authoritative agent and model preview for a project task.
type TaskModelPreview struct {
	TaskID              string                       `json:"task_id,omitempty"`
	Agent               string                       `json:"agent"`
	ResolvedAgent       string                       `json:"resolved_agent"`
	FeatureSize         string                       `json:"feature_size,omitempty"`
	TaskModelOverride   string                       `json:"task_model_override,omitempty"`
	ResolvedModel       *pebblestore.ModelPreference `json:"resolved_model,omitempty"`
	ModelSource         string                       `json:"model_source"` // "task_override" | "account_settings" | "account_default"
	AccountDefaultModel *pebblestore.ModelPreference `json:"account_default_model,omitempty"`
	AccountSettingsPath string                       `json:"account_settings_path"`
	ResolutionError     string                       `json:"resolution_error,omitempty"`
}

func (s *Server) resolveTaskModelPreference(p identity.Principal, task *pebblestore.ProjectTaskRecord) (pebblestore.ModelPreference, string, error) {
	if task == nil {
		return pebblestore.ModelPreference{}, "", errors.New("task is required")
	}
	targetAgent := strings.ToLower(strings.TrimSpace(task.Agent))
	if targetAgent == "" {
		targetAgent = "swarm"
	}
	isPlan := targetAgent == "plan"

	// Resolve default Swarm preference for account
	var defaultSwarmPref pebblestore.ModelPreference
	if s.agentModelSettings != nil && p.AccountScopeID != "" {
		if settings, err := s.agentModelSettings.GetForAccount(p.AccountScopeID); err == nil {
			swarmAssignment := settings.Swarm.Action
			if isPlan && strings.TrimSpace(settings.Swarm.Plan.Model) != "" {
				swarmAssignment = settings.Swarm.Plan
			}
			defaultSwarmPref = pebblestore.ModelPreference{
				Provider:    strings.TrimSpace(swarmAssignment.Provider),
				Model:       strings.TrimSpace(swarmAssignment.Model),
				Thinking:    strings.TrimSpace(swarmAssignment.Thinking),
				ServiceTier: strings.TrimSpace(swarmAssignment.ServiceTier),
				ContextMode: strings.TrimSpace(swarmAssignment.ContextMode),
			}
		}
	}
	if (defaultSwarmPref.Provider == "" || defaultSwarmPref.Model == "") && s.model != nil {
		if def, err := s.model.ResolvePreference(pebblestore.ModelPreference{}); err == nil {
			defaultSwarmPref = def.Preference
		}
	}

	// 1. Task-level model override takes precedence
	hasOverride := strings.TrimSpace(task.Model) != "" || strings.TrimSpace(task.Provider) != ""
	if hasOverride {
		overridePref := pebblestore.ModelPreference{
			Provider:    strings.TrimSpace(task.Provider),
			Model:       strings.TrimSpace(task.Model),
			Thinking:    strings.TrimSpace(task.Thinking),
			ServiceTier: strings.TrimSpace(task.ServiceTier),
			ContextMode: strings.TrimSpace(task.ContextMode),
		}

		if targetAgent == "image" || targetAgent == "video" || targetAgent == "sound" || targetAgent == "audio" {
			if targetAgent == "image" && s.imageGen != nil && overridePref.Model != "" {
				sel, err := s.imageGen.ResolveModelSelection(overridePref.Model)
				if err != nil {
					return pebblestore.ModelPreference{}, "", fmt.Errorf("invalid image model override %q: %w", overridePref.Model, err)
				}
				overridePref.Model = sel.ID
			}
			if targetAgent == "video" && overridePref.Model == "" {
				return pebblestore.ModelPreference{}, "", errors.New("video model override is empty")
			}
			if (targetAgent == "sound" || targetAgent == "audio") && overridePref.Model == "" {
				return pebblestore.ModelPreference{}, "", errors.New("audio model override is empty")
			}
			return overridePref, "task_override", nil
		}

		if s.model != nil {
			resolved, err := s.model.ResolvePreference(overridePref)
			if err != nil {
				return pebblestore.ModelPreference{}, "", fmt.Errorf("invalid task model override %q: %w", overridePref.Model, err)
			}
			if resolved.Preference.Model == "" {
				return pebblestore.ModelPreference{}, "", fmt.Errorf("task model override %q resolved to empty model", overridePref.Model)
			}
			return resolved.Preference, "task_override", nil
		}
		return overridePref, "task_override", nil
	}

	// 2. Media agents: strictly media models from uiSettings or canonical defaults (NEVER Swarm LLM!)
	if targetAgent == "image" || targetAgent == "video" || targetAgent == "sound" || targetAgent == "audio" {
		if s.uiSettings != nil && strings.TrimSpace(p.AccountScopeID) != "" {
			if uiSet, err := s.uiSettings.GetForAccount(p.AccountScopeID); err == nil {
				var mediaModel string
				switch targetAgent {
				case "image":
					mediaModel = strings.TrimSpace(uiSet.Tools.Image.DefaultModel)
				case "video":
					mediaModel = strings.TrimSpace(uiSet.Tools.Video.DefaultModel)
				case "sound", "audio":
					mediaModel = strings.TrimSpace(uiSet.Tools.Audio.DefaultModel)
				}
				if mediaModel != "" {
					return pebblestore.ModelPreference{Model: mediaModel}, "account_settings", nil
				}
			}
		}
		var defaultMediaModel string
		switch targetAgent {
		case "image":
			if s.imageGen != nil {
				if sels, err := s.imageGen.GoogleImageModelSelections(); err == nil && len(sels) > 0 {
					defaultMediaModel = sels[0].ID
				}
			}
			if defaultMediaModel == "" {
				defaultMediaModel = "imagen-3.0-generate-002"
			}
		case "video":
			defaultMediaModel = "veo-3.1-generate-preview"
		case "sound", "audio":
			defaultMediaModel = "lyria-3.5"
		}
		return pebblestore.ModelPreference{Model: defaultMediaModel}, "account_default", nil
	}

	// 3. Canonical subagents (coder, finder, designer, compact)
	if canonicalID, isCanonical := agentruntime.CanonicalSystemAgentID(targetAgent); isCanonical && canonicalID != agentruntime.SwarmAgentID {
		resolvedModel, _, err := agentmodel.ResolveSystemAgent(s.model, s.agents, s.agentModelSettings, p.AccountScopeID, canonicalID, "")
		if err == nil && resolvedModel.Preference.Model != "" {
			return resolvedModel.Preference, "account_settings", nil
		}
		// If system agent resolution failed or unconfigured, fall back to Swarm default with warning
		return defaultSwarmPref, "account_default", nil
	}

	// 4. Swarm / Plan
	return defaultSwarmPref, "account_default", nil
}

func (s *Server) buildTaskModelPreview(p identity.Principal, task *pebblestore.ProjectTaskRecord) TaskModelPreview {
	preview := TaskModelPreview{
		AccountSettingsPath: "/v3/agents/model-settings",
	}
	if task == nil {
		return preview
	}
	preview.TaskID = task.ID
	preview.Agent = task.Agent
	preview.ResolvedAgent = task.Agent
	preview.FeatureSize = task.FeatureSize
	preview.TaskModelOverride = strings.TrimSpace(task.Model)

	// Resolve default Swarm model (action or plan depending on feature size/agent)
	isPlan := strings.ToLower(strings.TrimSpace(task.Agent)) == "plan"
	if s.agentModelSettings != nil && p.AccountScopeID != "" {
		if settings, err := s.agentModelSettings.GetForAccount(p.AccountScopeID); err == nil {
			swarmAssignment := settings.Swarm.Action
			if isPlan && strings.TrimSpace(settings.Swarm.Plan.Model) != "" {
				swarmAssignment = settings.Swarm.Plan
			}
			preview.AccountDefaultModel = &pebblestore.ModelPreference{
				Provider:    strings.TrimSpace(swarmAssignment.Provider),
				Model:       strings.TrimSpace(swarmAssignment.Model),
				Thinking:    strings.TrimSpace(swarmAssignment.Thinking),
				ServiceTier: strings.TrimSpace(swarmAssignment.ServiceTier),
				ContextMode: strings.TrimSpace(swarmAssignment.ContextMode),
			}
		}
	}
	if (preview.AccountDefaultModel == nil || preview.AccountDefaultModel.Model == "") && s.model != nil {
		if def, err := s.model.ResolvePreference(pebblestore.ModelPreference{}); err == nil {
			preview.AccountDefaultModel = &def.Preference
		}
	}

	pref, source, err := s.resolveTaskModelPreference(p, task)
	if err != nil {
		preview.ResolutionError = err.Error()
		return preview
	}
	preview.ResolvedModel = &pref
	preview.ModelSource = source
	return preview
}

func truncateString(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// SynthesizeProjectContext reads AGENTS.md and README.md from the provided workspace paths
// and synthesizes an authoritative project.md document.
func SynthesizeProjectContext(projectName string, wsPaths []string) string {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		projectName = "Project"
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# %s Architecture & Project Charter\n\n", projectName))
	sb.WriteString("## Bound Workspaces & Architecture\n")

	for _, p := range wsPaths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		base := filepath.Base(p)
		sb.WriteString(fmt.Sprintf("- **`%s`** (`%s`)\n", base, p))

		// Scan for AGENTS.md and README.md
		var docFound bool
		for _, docName := range []string{"AGENTS.md", "README.md"} {
			docPath := filepath.Join(p, docName)
			if fi, err := os.Stat(docPath); err == nil && !fi.IsDir() {
				if data, err := os.ReadFile(docPath); err == nil {
					lines := strings.Split(string(data), "\n")
					var extracted []string
					for _, l := range lines {
						trimmed := strings.TrimSpace(l)
						if trimmed == "" {
							continue
						}
						// Skip markdown headers
						if strings.HasPrefix(trimmed, "#") {
							continue
						}
						// Collect relevant instruction / architecture lines
						if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || len(trimmed) > 20 {
							extracted = append(extracted, trimmed)
							if len(extracted) >= 4 {
								break
							}
						}
					}
					if len(extracted) > 0 {
						docFound = true
						sb.WriteString(fmt.Sprintf("  - *Source (%s)*:\n", docName))
						for _, ex := range extracted {
							sb.WriteString(fmt.Sprintf("    > %s\n", ex))
						}
						break
					}
				}
			}
		}
		if !docFound {
			sb.WriteString("  - *Role*: General repository module.\n")
		}
	}

	sb.WriteString("\n## System Architecture & Integration Boundary\n")
	sb.WriteString("- Coordinated by Swarm Project Orchestrator (`system-orchestrator`).\n")
	sb.WriteString("- Delegated execution runs on isolated worktrees via `coder`, `designer`, `finder`, and `swarm` waves.\n")
	sb.WriteString("- Durable V3 session contracts and Pebble persistence.\n\n")
	sb.WriteString("## Operational Invariants\n")
	sb.WriteString("- Local-first operation; zero external credential leaks.\n")
	sb.WriteString("- Minimal high-craft diffs with automated parent verification.\n")
	sb.WriteString("- Review-first delivery: finished tasks transition to `needs_review` before completion.\n")

	return sb.String()
}

// isDirectMediaTask checks whether a task is handled via direct media execution rather than an agent chat session.
func isDirectMediaTask(task *pebblestore.ProjectTaskRecord) bool {
	if task == nil {
		return false
	}
	return task.Agent == "image" || task.Agent == "video" || task.Agent == "sound" || task.Agent == "audio" || (task.Agent == "designer" && (task.Tier == "swarm" || len(task.Deliverables) > 1 || task.OutcomeType == "media_bundle"))
}

// deployProjectTaskExecution handles direct media generation for images/videos or
// creates and enqueues a canonical V3 session with compiled agent profile and RunIntent for agent tasks.
func (s *Server) deployProjectTaskExecution(p identity.Principal, proj *pebblestore.ProjectRecord, task *pebblestore.ProjectTaskRecord, taskStatus string, prompt string) error {
	if !p.Valid() || p.Type != identity.PrincipalTypeUser || strings.TrimSpace(p.UserID) == "" || strings.TrimSpace(p.AccountScopeID) == "" {
		return errors.New("user id is required")
	}
	if task == nil {
		return errors.New("task is required")
	}

	// 1. Direct Media Generation: image, generative video, audio, and designer swarm tasks do NOT spin up chat agent sessions.
	// They directly generate media deliverables and transition to needs_review.
	isDirectMedia := isDirectMediaTask(task)
	if isDirectMedia {
		task.SessionID = ""
		if taskStatus == "in_progress" {
			now := time.Now().UnixMilli()
			if task.Agent == "image" {
				count := task.VariantCount
				if count <= 0 {
					count = len(task.Deliverables)
				}
				if count <= 0 {
					count = 1
				}
				ar := task.AspectRatio
				if ar == "" {
					ar = "1:1"
				}
				var delivs []pebblestore.ProjectTaskDeliverable
				for i := 1; i <= count; i++ {
					delivs = append(delivs, pebblestore.ProjectTaskDeliverable{
						ID:          fmt.Sprintf("deliv_img_%d_%d", now, i),
						Title:       fmt.Sprintf("%s (Variant %d, %s)", task.Title, i, ar),
						Kind:        "image",
						Status:      "queued",
						Description: fmt.Sprintf("Autonomous deliverable for %s in aspect ratio %s", task.Title, ar),
					})
				}
				if len(task.Deliverables) == 0 {
					task.Deliverables = delivs
				} else if len(task.Deliverables) != count {
					return errors.New("image deliverable count does not match variant count")
				}
				for i := range task.Deliverables {
					if task.Deliverables[i].Status != "ready" && task.Deliverables[i].Status != "accepted" {
						task.Deliverables[i].Status = "queued"
					}
				}
				task.VariantCount = count
				if err := validateProjectMediaTaskSettings(s, task, p); err != nil {
					return err
				}
				task.Status = "in_progress"
				task.ActionNeeded = fmt.Sprintf("Generating %d deliverable variant(s)...", count)
				task.WhatDidDo = []string{"Approved mission", "Generating media assets"}
			} else if task.Agent == "video" {
				if task.OutcomeType == "video_story" || len(task.Scenes) > 1 {
					if err := validateVideoScenes(task.Scenes, task.Operation, task.VariantCount, task.Soundtrack); err != nil {
						return err
					}
				}
				videoModel := strings.TrimSpace(task.Model)
				if videoModel == "" && s.uiSettings != nil && strings.TrimSpace(p.AccountScopeID) != "" {
					if uiSet, err := s.uiSettings.GetForAccount(p.AccountScopeID); err == nil {
						videoModel = strings.TrimSpace(uiSet.Tools.Video.DefaultModel)
					}
				}
				if videoModel == "" {
					return errors.New("no default video model configured for account; select a model or configure one in Settings")
				}
				vOpts := s.getVideoModelOptions(videoModel)
				ar := task.AspectRatio
				if ar == "" && vOpts != nil && vOpts.DefaultRatio != "" {
					ar = vOpts.DefaultRatio
				}
				count := task.VariantCount
				if count <= 0 {
					count = len(task.Deliverables)
				}
				if count <= 0 {
					count = 1
				}
				if count > 8 {
					return fmt.Errorf("video variant count %d exceeds maximum allowed (8)", count)
				}
				resTag := task.Resolution
				if resTag == "" && vOpts != nil && vOpts.DefaultRes != "" {
					resTag = vOpts.DefaultRes
				}
				durSec := task.DurationSeconds
				if videogen.IsOmniModel(videoModel) {
					durSec = 0
				} else if durSec <= 0 && vOpts != nil && vOpts.DefaultDur > 0 {
					durSec = vOpts.DefaultDur
				}
				durStr := ""
				if durSec > 0 {
					durStr = fmt.Sprintf("%ds", durSec)
				}

				var delivs []pebblestore.ProjectTaskDeliverable
				if count > 1 {
					for i := 1; i <= count; i++ {
						delivs = append(delivs, pebblestore.ProjectTaskDeliverable{
							ID:          fmt.Sprintf("deliv_vid_%d_%d", now, i),
							Title:       fmt.Sprintf("%s (Take %d, %s)", task.Title, i, ar),
							Kind:        "video",
							Status:      "generating",
							Thumbnail:   "video",
							Duration:    durStr,
							Description: fmt.Sprintf("Video variation %d of %d (%s, %s, %s) generating with %s: %s", i, count, ar, resTag, durStr, videoModel, task.Title),
						})
					}
				} else {
					delivs = append(delivs, pebblestore.ProjectTaskDeliverable{
						ID:          fmt.Sprintf("deliv_vid_%d", now),
						Title:       fmt.Sprintf("%s (Single Video, %s)", task.Title, ar),
						Kind:        "video",
						Status:      "generating",
						Thumbnail:   "video",
						Duration:    durStr,
						Description: fmt.Sprintf("Single video clip (%s, %s, %s) generating with %s: %s", ar, resTag, durStr, videoModel, task.Title),
					})
				}
				task.Deliverables = delivs
				task.Status = "in_progress"
				task.ActionNeeded = fmt.Sprintf("Rendering %d video deliverable(s)...", count)
				task.WhatDidDo = []string{"Approved mission", fmt.Sprintf("Rendering %d video clip(s) with %s", count, videoModel)}
			} else if task.Agent == "sound" || task.Agent == "audio" {
				soundModel := strings.TrimSpace(task.Model)
				if soundModel == "" {
					soundModel = "lyria-3.5"
				}
				durSeconds := task.DurationSeconds
				if durSeconds <= 0 {
					durSeconds = 30
				}
				var delivs []pebblestore.ProjectTaskDeliverable
				delivs = append(delivs, pebblestore.ProjectTaskDeliverable{
					ID:          fmt.Sprintf("deliv_snd_%d", now),
					Title:       fmt.Sprintf("%s (Audio Clip)", task.Title),
					Kind:        "audio",
					Status:      "generating",
					Thumbnail:   "sound",
					Duration:    fmt.Sprintf("%ds", durSeconds),
					Description: fmt.Sprintf("Generated %ds audio soundtrack using %s: %s", durSeconds, soundModel, task.Title),
				})
				task.Deliverables = delivs
				task.Status = "in_progress"
				task.ActionNeeded = "Generating audio soundtrack..."
				task.WhatDidDo = []string{"Approved mission", "Generating audio track"}
			}

			// Persist task before starting execution goroutine to eliminate the persistence vs goroutine race.
			if s.sessions == nil || s.sessions.Store() == nil {
				return errors.New("media task store is unavailable")
			}
			if err := s.sessions.Store().PutProjectTask(p.AccountScopeID, task); err != nil {
				return fmt.Errorf("persist media task slots: %w", err)
			}

			taskCopy := *task
			if len(task.Deliverables) > 0 {
				taskCopy.Deliverables = append([]pebblestore.ProjectTaskDeliverable(nil), task.Deliverables...)
			}
			if len(task.AttachedMedia) > 0 {
				taskCopy.AttachedMedia = append([]pebblestore.ProjectTaskMediaRef(nil), task.AttachedMedia...)
			}
			if len(task.Scenes) > 0 {
				taskCopy.Scenes = append([]pebblestore.ProjectTaskScene(nil), task.Scenes...)
			}
			if len(task.WhatDidDo) > 0 {
				taskCopy.WhatDidDo = append([]string(nil), task.WhatDidDo...)
			}
			if len(task.WhatNotDone) > 0 {
				taskCopy.WhatNotDone = append([]string(nil), task.WhatNotDone...)
			}
			go s.executeDirectMediaTask(p, proj, &taskCopy)
		}
		return nil
	}

	// 2. Agent Tasks: coder, finder, designer, swarm, plan.
	// Must create a canonical V3 session with compiled agent_profile, seed message, and RunIntent.
	if err := s.revalidateProjectTaskSource(p, proj, task); err != nil {
		return err
	}
	wsPath := task.SourceWorkspace.Path
	if existing, ok, err := s.sessions.Store().GetSession(task.SessionID); err != nil {
		return err
	} else if ok {
		return s.reconcileProjectTaskSession(p, proj, task, existing, taskStatus)
	}

	if task.WorkspacePath != wsPath {
		return errors.New("reserved source differs from execution path without an owned session")
	}
	targetAgent, mode := projectTaskExecutionAgent(task)

	// Resolve canonical default Swarm preference for fallback or primary Swarm task
	var defaultSwarmPref pebblestore.ModelPreference
	if s.agentModelSettings != nil && p.AccountScopeID != "" {
		if settings, err := s.agentModelSettings.GetForAccount(p.AccountScopeID); err == nil {
			swarmAssignment := settings.Swarm.Action
			if mode == sessionruntime.ModePlan && strings.TrimSpace(settings.Swarm.Plan.Model) != "" {
				swarmAssignment = settings.Swarm.Plan
			}
			defaultSwarmPref = pebblestore.ModelPreference{
				Provider:    strings.TrimSpace(swarmAssignment.Provider),
				Model:       strings.TrimSpace(swarmAssignment.Model),
				Thinking:    strings.TrimSpace(swarmAssignment.Thinking),
				ServiceTier: strings.TrimSpace(swarmAssignment.ServiceTier),
				ContextMode: strings.TrimSpace(swarmAssignment.ContextMode),
			}
		}
	}
	if defaultSwarmPref.Provider == "" || defaultSwarmPref.Model == "" {
		if s.model != nil {
			if def, err := s.model.ResolvePreference(pebblestore.ModelPreference{}); err == nil {
				defaultSwarmPref = def.Preference
			}
		}
	}

	var resolvedPref pebblestore.ModelPreference
	var agentProfile pebblestore.AgentProfile
	var fallbackAlert string

	pref, source, prefErr := s.resolveTaskModelPreference(p, task)
	if prefErr != nil {
		return fmt.Errorf("task model resolution failed: %w", prefErr)
	}
	resolvedPref = pref

	if canonicalID, isCanonical := agentruntime.CanonicalSystemAgentID(targetAgent); isCanonical && canonicalID != agentruntime.SwarmAgentID {
		if source == "account_default" {
			fallbackAlert = fmt.Sprintf("Configured model for agent %q could not be resolved or was unconfigured; fell back to Swarm default (%s/%s).", targetAgent, resolvedPref.Provider, resolvedPref.Model)
		}
		if s.agents != nil {
			agentProfile, _ = s.agents.ResolveSystemAgent(canonicalID, pebblestore.AgentProfile{
				Provider:        resolvedPref.Provider,
				Model:           resolvedPref.Model,
				Thinking:        resolvedPref.Thinking,
				AutoServiceTier: resolvedPref.ServiceTier,
				ContextMode:     resolvedPref.ContextMode,
			})
		}
	} else if strings.EqualFold(targetAgent, "swarm") {
		if s.agents != nil {
			agentProfile, _ = s.agents.ResolveSystemAgent(agentruntime.SwarmAgentID, pebblestore.AgentProfile{
				Provider:        resolvedPref.Provider,
				Model:           resolvedPref.Model,
				Thinking:        resolvedPref.Thinking,
				AutoServiceTier: resolvedPref.ServiceTier,
				ContextMode:     resolvedPref.ContextMode,
			})
		}
	} else {
		// Custom / saved agent profile
		var profileFound bool
		if s.agents != nil {
			if p.AccountScopeID != "" {
				agentProfile, profileFound, _ = s.agents.GetProfileForAccount(p.AccountScopeID, targetAgent)
			}
			if !profileFound {
				agentProfile, profileFound, _ = s.agents.GetProfile(targetAgent)
			}
		}
		if profileFound && agentProfile.Model != "" {
			resolvedPref = pebblestore.ModelPreference{
				Provider:    agentProfile.Provider,
				Model:       agentProfile.Model,
				Thinking:    agentProfile.Thinking,
				ServiceTier: agentProfile.AutoServiceTier,
				ContextMode: agentProfile.ContextMode,
			}
		} else {
			fallbackAlert = fmt.Sprintf("Custom agent profile %q not found or has no model; fell back to Swarm default (%s/%s).", targetAgent, resolvedPref.Provider, resolvedPref.Model)
			if !profileFound && s.agents != nil {
				agentProfile, _ = s.agents.ResolveSystemAgent(agentruntime.SwarmAgentID, pebblestore.AgentProfile{
					Provider:        resolvedPref.Provider,
					Model:           resolvedPref.Model,
					Thinking:        resolvedPref.Thinking,
					AutoServiceTier: resolvedPref.ServiceTier,
				})
			}
		}
	}

	if agentProfile.Name == "" {
		trueVal := true
		agentProfile = pebblestore.AgentProfile{
			Name:                targetAgent,
			Mode:                "primary",
			RuntimeMode:         pebblestore.AgentRuntimeModePlanAuto,
			DefaultSessionMode:  "auto",
			ExitPlanModeEnabled: &trueVal,
			Provider:            resolvedPref.Provider,
			Model:               resolvedPref.Model,
			Thinking:            resolvedPref.Thinking,
			Enabled:             true,
			ToolContract:        &pebblestore.AgentToolContract{Preset: "full"},
		}
	}

	if fallbackAlert != "" {
		if task.RouterAlert == "" {
			task.RouterAlert = fallbackAlert
		} else if !strings.Contains(task.RouterAlert, fallbackAlert) {
			task.RouterAlert = task.RouterAlert + " | " + fallbackAlert
		}
		task.WhatDidDo = append(task.WhatDidDo, fallbackAlert)
	}

	now := time.Now().UnixMilli()
	sessionID := strings.TrimSpace(task.SessionID)
	if sessionID == "" {
		sessionID = sessionruntime.NewSessionID()
	}
	metadata := map[string]any{
		"project_id":           task.ProjectID,
		"task_id":              task.ID,
		"task_title":           task.Title,
		"task_attempt_id":      task.ActiveAttemptID,
		"agent_name":           agentProfile.Name,
		"resolved_agent_name":  agentProfile.Name,
		"agent_mode":           agentProfile.Mode,
		"runtime_mode":         agentProfile.RuntimeMode,
		"default_session_mode": pebblestore.AgentProfileDefaultSessionMode(agentProfile),
		"agent_profile":        cloneSessionsV3AgentProfile(agentProfile),
		"role":                 "project_task",
		"workspaces_involved":  task.WorkspacesInvolved,
		"context_pool_summary": task.ContextPoolSummary,
		"plan_summary":         task.PlanSummary,
		"full_plan_markdown":   task.FullPlanMarkdown,
		"tier":                 task.Tier,
		"revision":             task.Revision,
	}
	if task.OriginSessionID != "" {
		metadata["parent_session_id"] = task.OriginSessionID
	}
	if agentProfile.ExitPlanModeEnabled != nil {
		metadata["exit_plan_mode_enabled"] = *agentProfile.ExitPlanModeEnabled
	}
	if agentProfile.ToolContract != nil && agentProfile.ToolContract.Preset != "" {
		metadata["tool_contract_preset"] = agentProfile.ToolContract.Preset
	}
	if task.Agent == "plan" {
		metadata["default_session_mode"] = "plan"
	}

	worktreeBranch := strings.TrimSpace(task.WorktreeBranch)
	if worktreeBranch == "" || worktreeBranch == "main" || worktreeBranch == "dev" || worktreeBranch == "master" {
		worktreeBranch, _ = pebblestore.MakeWorktreeBranch(task.Title, prompt)
	}
	task.WorktreeBranch = worktreeBranch
	task.WorktreeName = strings.TrimPrefix(worktreeBranch, "agent/")
	task.WorktreeName = strings.TrimPrefix(task.WorktreeName, "worktree/")

	avail := true
	taskWsID := ""
	taskWsID = task.SourceWorkspace.WorkspaceID
	metadata["swarm_v3_source_workspace_id"] = taskWsID
	metadata["swarm_v3_source_workspace_generation"] = task.SourceWorkspace.WorkspaceGeneration
	metadata["swarm_v3_source_workspace_path"] = wsPath
	metadata["swarm_v3_source_workspace_provenance"] = task.SourceWorkspace.Provenance
	grants := []pebblestore.WorkspaceGrant{
		{Kind: pebblestore.WorkspaceGrantPrimary, WorkspaceID: taskWsID, WorkspaceGeneration: task.SourceWorkspace.WorkspaceGeneration, Path: wsPath, Name: filepath.Base(wsPath), Available: &avail},
	}
	seenSources := map[string]bool{wsPath: true}
	sources := append([]pebblestore.ProjectTaskSource(nil), task.ProgramSources...)
	for _, assignment := range task.CoderAssignments {
		sources = append(sources, assignment.SourceWorkspace)
	}
	for _, source := range sources {
		if !seenSources[source.Path] {
			grants = append(grants, pebblestore.WorkspaceGrant{Kind: pebblestore.WorkspaceGrantAdditional, WorkspaceID: source.WorkspaceID, WorkspaceGeneration: source.WorkspaceGeneration, Path: source.Path, Name: filepath.Base(source.Path), Available: &avail})
			seenSources[source.Path] = true
		}
	}
	projName := "Project"
	if proj != nil && proj.Name != "" {
		projName = proj.Name
	}
	if node, ok, err := s.swarmLocalNode(); err != nil {
		return err
	} else if ok && strings.TrimSpace(node.SwarmID) != "" {
		metadata["swarm_v3_runtime_swarm_id"] = strings.TrimSpace(node.SwarmID)
		metadata["swarm_v3_authority_host_swarm_id"] = strings.TrimSpace(node.SwarmID)
	}
	sessionSnapshot := pebblestore.SessionSnapshot{
		ID:              sessionID,
		UserID:          p.UserID,
		AccountScopeID:  p.AccountScopeID,
		WorkspacePath:   wsPath,
		WorkspaceName:   filepath.Base(wsPath),
		Title:           fmt.Sprintf("[%s] %s", projName, task.Title),
		Mode:            mode,
		Preference:      resolvedPref,
		Metadata:        metadata,
		WorkspaceGrants: grants,
		WorkspaceUsage:  pebblestore.WorkspaceUsageFromGrants(grants),
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	if isProjectTaskFollowup(task) {
		binding, err := s.projectTaskSessionBinding(p, task)
		if err != nil {
			return err
		}
		metadata = projectTaskBindingMetadata(metadata, binding)
		sessionSnapshot.Metadata = metadata
	}

	// Validate the complete batch before allocating a worktree or creating a session.
	attachmentPlan, attachmentBytes, err := s.prepareProjectTaskAttachments(context.Background(), p, sessionSnapshot, task.AttachedMedia)
	if err != nil {
		return err
	}

	var admission *pebblestore.WorktreeAdmissionEvidence
	if targetAgent == "coder" || mode == sessionruntime.ModePlan || task.OutcomeType == "code_pr" || task.OutcomeType == "bug_patch" || task.ActiveAttemptID != "" && task.ActiveAttemptID != "initial" {
		if s.worktrees == nil {
			return errors.New("worktree service is not configured; coding task requires worktree isolation")
		}
		var alloc worktreeruntime.Allocation
		var err error
		if task.ActiveAttemptID != "" && task.ActiveAttemptID != "initial" {
			allocator, ok := s.worktrees.(interface {
				AllocateProjectTaskFollowup(identity.Principal, string, string, string, string, string) (worktreeruntime.Allocation, error)
			})
			if !ok {
				return errors.New("durable follow-up allocator unavailable")
			}
			if source := task.ActiveAttempt().Recovery; source != nil {
				if err := s.validateProjectTaskRecovery(p, task, source); err != nil {
					return fmt.Errorf("follow-up source validation: %w", err)
				}
			}
			head := task.ActiveAttempt().AllocationHead
			if head == "" {
				// Historical reservations may predate AllocationHead. A retained
				// source pins allocation independently of the integration delta base.
				head = task.BaseCommit
				if source := task.ActiveAttempt().Recovery; source != nil {
					head = source.HeadCommit
					if source.PreparedHead != "" {
						head = source.PreparedHead
					}
				}
			}
			alloc, err = allocator.AllocateProjectTaskFollowup(p, wsPath, sessionID, worktreeBranch, head, task.BaseBranch)
			if err == nil && task.ActiveAttempt().Recovery != nil {
				alloc.BaseCommit = projectTaskRepairBase(task.ActiveAttempt().Recovery)
			}
		} else {
			alloc, err = s.worktrees.AllocateDetachedWorkspaceRequestedForPrincipal(p, wsPath, sessionID, "", worktreeBranch)
		}
		if err != nil {
			return fmt.Errorf("worktree allocation failed for coder task: %w", err)
		}
		if alloc.WorkspacePath == "" {
			return errors.New("worktree allocation returned empty workspace path")
		}
		sessionSnapshot.WorktreeEnabled = true
		sessionSnapshot.WorktreeRootPath = strings.TrimSpace(alloc.WorkspacePath)
		sessionSnapshot.WorktreeBaseBranch = strings.TrimSpace(alloc.BaseBranch)
		sessionSnapshot.WorktreeBranch = strings.TrimSpace(alloc.BranchName)
		sessionSnapshot.WorkspacePath = alloc.WorkspacePath
		task.WorkspacePath = alloc.WorkspacePath
		task.WorktreeBranch = alloc.BranchName
		task.BaseBranch = alloc.BaseBranch
		task.BaseCommit = alloc.BaseCommit
		task.WorktreeName = strings.TrimPrefix(alloc.BranchName, "agent/")
		sourcePath := wsPath
		if allocRepoRoot := strings.TrimSpace(alloc.RepoRoot); allocRepoRoot != "" && allocRepoRoot != wsPath {
			return errors.New("worktree allocation source does not match reserved workspace")
		}
		metadata["base_commit"] = alloc.BaseCommit
		metadata["swarm_v3_source_workspace_path"] = sourcePath
		metadata["swarm_v3_runtime_workspace_path"] = alloc.WorkspacePath
		metadata["swarm_v3_worktree_owner_session_id"] = sessionID
		sessionSnapshot.Metadata = metadata
		available := true
		sessionSnapshot.WorkspaceGrants = append(sessionSnapshot.WorkspaceGrants, pebblestore.WorkspaceGrant{
			Kind: pebblestore.WorkspaceGrantWorktree, WorkspaceID: taskWsID, WorkspaceGeneration: task.SourceWorkspace.WorkspaceGeneration, Path: alloc.WorkspacePath, Available: &available,
		})
		sessionSnapshot.WorkspaceUsage = pebblestore.WorkspaceUsageFromGrants(sessionSnapshot.WorkspaceGrants)
		admission = &pebblestore.WorktreeAdmissionEvidence{
			Kind:                 "allocated",
			Path:                 alloc.WorkspacePath,
			SourcePath:           sourcePath,
			OwnerSessionID:       sessionID,
			Branch:               alloc.BranchName,
			DelegatedCoder:       targetAgent == "coder",
			AllocatedRuntimeRoot: true, // Every allocation above uses its owned runtime path, including Swarm coding tasks.
		}
	}

	createKey := fmt.Sprintf("project-task:create:%s:%s:%s", task.ProjectID, task.ID, task.SessionID)
	_, createErr := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		WorktreeAdmission: admission,
		SessionID:         sessionID,
		UserID:            p.UserID,
		AccountScopeID:    p.AccountScopeID,
		ClientRequestID:   createKey,
		IdempotencyKey:    createKey,
		PayloadHash:       createKey,
		RequestHash:       createKey,
		Kind:              sessionruntime.SessionMutationCreateSession,
		Session:           &sessionSnapshot,
		NowUnixMs:         now,
	})
	if createErr != nil {
		return createErr
	}
	task.SessionID = sessionID

	attachmentRefs, err := s.retainProjectTaskAttachments(attachmentPlan, attachmentBytes)
	if err != nil {
		return err
	}
	var tr taskrouter.Service
	seedMsg := tr.BuildAgentSeedPrompt(task, proj)
	if mode == sessionruntime.ModePlan {
		seedMsg += "\n\n## Planning phase\nInvestigate only as needed, then submit a complete executable structured plan using exit_plan_mode. Include ordered checkpoints, concrete tasks and acceptance criteria. This project task must show the submitted plan for user approval before any implementation. Do not write implementation files or execute the task in this phase. For coding deliverables, include a checkpoint task_program with a Coder job, explicit workspace-relative owned_scope, implementation instructions, deliverable, acceptance_criteria and dependency_evidence. The approved checkpoint must launch that program rather than implementing directly in the planner workspace. Preserve every user requirement, including committing changes. Complete the checkpoint through the plan lifecycle only after verifying the returned deliverable; completing subtasks alone is not checkpoint completion."
	}
	msgID := fmt.Sprintf("msg_%s_seed", sessionID)
	seedMsg += projectTaskFollowupContext(task)
	msg := pebblestore.MessageSnapshot{
		ID:             msgID,
		SessionID:      sessionID,
		UserID:         p.UserID,
		AccountScopeID: p.AccountScopeID,
		Role:           "user",
		Content:        seedMsg,
		Media:          attachmentRefs,
		Metadata: map[string]any{
			"role":                 "project_context_seed",
			"task_id":              task.ID,
			"project_id":           task.ProjectID,
			"context_pool_summary": task.ContextPoolSummary,
		},
		CreatedAt: now,
	}

	var runIntent *pebblestore.V3SessionRunIntent
	runID := ""
	if taskStatus == "in_progress" {
		runID = task.ExecutionRunID()
		parentSessionID := task.OriginSessionID
		if len(task.CoderAssignments) > 0 {
			parentSessionID = "" // Swarm remains the delegation parent.
		}
		runIntent = &pebblestore.V3SessionRunIntent{
			SessionID:       sessionID,
			RunID:           runID,
			EpochID:         "epoch-00000000000000000001",
			UserID:          p.UserID,
			AccountScopeID:  p.AccountScopeID,
			ParentSessionID: parentSessionID,
			SourceMessageID: msgID,
			Status:          pebblestore.V3RunIntentPendingExecutor,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
	}

	msgKey := fmt.Sprintf("project-task:seed:%s:%s:%s", task.ProjectID, task.ID, task.SessionID)
	_, appendErr := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          p.UserID,
		AccountScopeID:  p.AccountScopeID,
		ClientRequestID: msgKey,
		IdempotencyKey:  msgKey,
		PayloadHash:     msgKey,
		RequestHash:     msgKey,
		Kind:            pebblestore.V3SessionMutationAppendMessage,
		Message:         &msg,
		RunIntent:       runIntent,
		NowUnixMs:       now,
	})
	if appendErr != nil {
		return appendErr
	}

	if taskStatus == "in_progress" && runIntent != nil {
		parentSessionID := task.OriginSessionID
		if len(task.CoderAssignments) > 0 {
			parentSessionID = "" // Swarm remains the delegation parent.
		}
		if task.ActiveAttemptID != "" && task.ActiveAttemptID != "initial" {
			if err := s.enqueueProjectTaskFollowup(p, sessionID, runID, parentSessionID); err != nil {
				return err
			}
		} else {
			s.EnqueueSessionRun(p, sessionID, runID, parentSessionID)
		}
	}

	return nil
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := PrincipalFromRequest(r)
	if !ok || !p.Valid() {
		writeError(w, http.StatusUnauthorized, errors.New("trusted user required"))
		return
	}
	if p.Type != "user" {
		writeError(w, http.StatusForbidden, errors.New("explicit user required"))
		return
	}
	if s.sessions == nil || s.sessions.Store() == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("session service unavailable"))
		return
	}

	db := s.sessions.Store()
	rawPath := strings.TrimPrefix(r.URL.Path, ProjectsPath)
	trimmed := strings.Trim(rawPath, "/")

	// 1. Root /v3/projects
	if trimmed == "" {
		if r.Method == http.MethodGet {
			if !s.requireScopeAny(w, r, "projects:read", "sessions:read") {
				return
			}
			q := r.URL.Query()
			limit := 100
			if q.Get("limit") != "" {
				if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
					limit = n
				}
			}
			records, err := db.ListProjects(p.AccountScopeID, limit)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			if records == nil {
				records = []pebblestore.ProjectRecord{}
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"projects": records,
				"count":    len(records),
			})
			return
		}

		if r.Method == http.MethodPost {
			if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
				return
			}
			var rec pebblestore.ProjectRecord
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2*1024*1024))
			if err != nil {
				writeError(w, http.StatusBadRequest, errors.New("cannot read request body"))
				return
			}
			if err := json.Unmarshal(body, &rec); err != nil {
				writeError(w, http.StatusBadRequest, errors.New("invalid JSON payload"))
				return
			}

			var themeInput map[string]json.RawMessage
			if err := json.Unmarshal(body, &themeInput); err != nil {
				writeError(w, http.StatusBadRequest, errors.New("invalid project payload"))
				return
			}
			if raw, present := themeInput["icon_png_data_url"]; present && string(raw) == "null" {
				writeError(w, http.StatusBadRequest, errors.New("icon_png_data_url must be a string (empty clears the icon)"))
				return
			}
			if raw, present := themeInput["theme_id"]; present {
				var id string
				if err := json.Unmarshal(raw, &id); err != nil || string(raw) == "null" {
					writeError(w, http.StatusBadRequest, errors.New("theme_id must be a string (empty clears the selection)"))
					return
				}
			}
			if rec.ThemeID, err = s.resolveProjectThemeID(p.AccountScopeID, rec.ThemeID); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if err := db.PutProject(p.AccountScopeID, &rec); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}

			writeJSON(w, http.StatusCreated, map[string]any{
				"project": rec,
			})
			return
		}

		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}

	segments := strings.Split(trimmed, "/")
	if len(segments) == 2 && segments[1] == "designs" {
		s.handleProjectDesigns(w, r, p, segments[0])
		return
	}

	// 2. Synthesize context: POST /v3/projects/synthesize-context
	if len(segments) == 1 && segments[0] == "synthesize-context" {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if !s.requireScopeAny(w, r, "projects:read", "sessions:read", "projects:write", "sessions:write") {
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("cannot read request body"))
			return
		}
		var req struct {
			Name       string   `json:"name"`
			Workspaces []string `json:"workspaces"`
		}
		if len(body) > 0 {
			_ = json.Unmarshal(body, &req)
		}
		ctxText := SynthesizeProjectContext(req.Name, req.Workspaces)
		writeJSON(w, http.StatusOK, map[string]any{
			"project_context": ctxText,
		})
		return
	}

	projectID := segments[0]
	if len(segments) == 5 && segments[1] == "tasks" && segments[3] == "deliverables" {
		s.handleProjectDeliverableContent(w, r, p, projectID, segments[2], segments[4])
		return
	}
	if len(segments) >= 2 && segments[1] == "sessions" {
		s.handleProjectConversations(w, r, p, projectID, segments[2:])
		return
	}

	// 3. Single project resource: /v3/projects/{id}
	if len(segments) == 1 {
		if r.Method == http.MethodGet {
			if !s.requireScopeAny(w, r, "projects:read", "sessions:read") {
				return
			}
			rec, found, err := db.GetProject(p.AccountScopeID, projectID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			if !found || rec == nil {
				writeError(w, http.StatusNotFound, errors.New("project not found"))
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"project": rec,
			})
			return
		}

		if r.Method == http.MethodPatch {
			if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
				return
			}
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2*1024*1024))
			if err != nil {
				writeError(w, http.StatusBadRequest, errors.New("cannot read request body"))
				return
			}
			var patch map[string]any
			if err := json.Unmarshal(body, &patch); err != nil {
				writeError(w, http.StatusBadRequest, errors.New("invalid JSON payload"))
				return
			}

			if raw, present := patch["icon_png_data_url"]; present {
				if _, ok := raw.(string); !ok {
					writeError(w, http.StatusBadRequest, errors.New("icon_png_data_url must be a string (empty clears the icon)"))
					return
				}
			}
			var themeID string
			if raw, present := patch["theme_id"]; present {
				value, ok := raw.(string)
				if !ok {
					writeError(w, http.StatusBadRequest, errors.New("theme_id must be a string (empty clears the selection)"))
					return
				}
				themeID, err = s.resolveProjectThemeID(p.AccountScopeID, value)
				if err != nil {
					writeError(w, http.StatusBadRequest, err)
					return
				}
			}
			updated, err := db.UpdateProject(p.AccountScopeID, projectID, func(p *pebblestore.ProjectRecord) error {
				if icon, present := patch["icon_png_data_url"].(string); present {
					p.IconPNGDataURL = icon
				}
				if _, present := patch["theme_id"]; present {
					p.ThemeID = themeID
				}
				if v, ok := patch["name"].(string); ok && strings.TrimSpace(v) != "" {
					p.Name = strings.TrimSpace(v)
				}
				if v, ok := patch["description"].(string); ok {
					p.Description = strings.TrimSpace(v)
				}
				if v, ok := patch["project_context"].(string); ok {
					p.ProjectContext = v
				}
				if v, ok := patch["primary_session_id"].(string); ok {
					p.PrimarySessionID = strings.TrimSpace(v)
				}
				if workspacesRaw, ok := patch["workspaces"]; ok {
					rawBytes, err := json.Marshal(workspacesRaw)
					if err == nil {
						var ws []pebblestore.ProjectWorkspaceRef
						if err := json.Unmarshal(rawBytes, &ws); err == nil {
							p.Workspaces = ws
						}
					}
				}
				if tasksRaw, ok := patch["active_task_ids"]; ok {
					rawBytes, err := json.Marshal(tasksRaw)
					if err == nil {
						var tasks []string
						if err := json.Unmarshal(rawBytes, &tasks); err == nil {
							p.ActiveTaskIDs = tasks
						}
					}
				}
				if autosRaw, ok := patch["automation_ids"]; ok {
					rawBytes, err := json.Marshal(autosRaw)
					if err == nil {
						var autos []string
						if err := json.Unmarshal(rawBytes, &autos); err == nil {
							p.AutomationIDs = autos
						}
					}
				}
				return nil
			})
			if err != nil {
				if strings.Contains(err.Error(), "not found") {
					writeError(w, http.StatusNotFound, err)
					return
				}
				writeError(w, http.StatusBadRequest, err)
				return
			}

			writeJSON(w, http.StatusOK, map[string]any{
				"project": updated,
			})
			return
		}

		if r.Method == http.MethodDelete {
			if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
				return
			}
			if err := db.DeleteProject(p.AccountScopeID, projectID); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"status": "deleted",
				"id":     projectID,
			})
			return
		}

		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}

	// 3.4 Clear orchestrator context: POST /v3/projects/{id}/orchestrator:clear-context or /v3/projects/{id}/clear-context
	if (len(segments) == 2 && (segments[1] == "orchestrator:clear-context" || segments[1] == "clear-context" || segments[1] == "orchestrator-clear-context")) || (len(segments) == 3 && segments[1] == "orchestrator" && segments[2] == "clear-context") {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
			return
		}
		proj, found, err := db.GetProject(p.AccountScopeID, projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found || proj == nil {
			writeError(w, http.StatusNotFound, errors.New("project not found"))
			return
		}

		// Retained for old clients only. New conversations use /sessions and
		// must not destroy history or retarget the legacy primary pointer.
		writeError(w, http.StatusGone, errors.New("context reset is retired; create a project conversation through /sessions"))
		return
	}

	// 3.5 Media sub-resource: /v3/projects/{id}/media and /v3/projects/{id}/media/{media_id}
	if len(segments) >= 2 && segments[1] == "media" {
		proj, found, err := db.GetProject(p.AccountScopeID, projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found || proj == nil {
			writeError(w, http.StatusNotFound, errors.New("project not found"))
			return
		}

		if len(segments) == 2 {
			if r.Method == http.MethodGet {
				if !s.requireScopeAny(w, r, "projects:read", "sessions:read") {
					return
				}
				list := proj.UploadedMedia
				if list == nil {
					list = []pebblestore.ProjectTaskMediaRef{}
				}
				writeJSON(w, http.StatusOK, map[string]any{
					"media": list,
					"count": len(list),
				})
				return
			}
			if r.Method == http.MethodPost {
				if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
					return
				}
				targetSessionID := strings.TrimSpace(proj.PrimarySessionID)
				if targetSessionID == "" {
					targetSessionID = projectID
				}
				var item pebblestore.ProjectTaskMediaRef
				ct := strings.ToLower(r.Header.Get("Content-Type"))
				if strings.Contains(ct, "application/json") {
					body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024*1024))
					if err != nil {
						writeError(w, http.StatusBadRequest, errors.New("cannot read request body"))
						return
					}
					if err := json.Unmarshal(body, &item); err != nil {
						writeError(w, http.StatusBadRequest, errors.New("invalid JSON payload"))
						return
					}
					trimmedURL := strings.TrimSpace(item.URL)
					stagingID := ""
					if strings.HasPrefix(item.ID, "stg_") {
						stagingID = item.ID
					} else if idx := strings.Index(trimmedURL, "/media/staging/stg_"); idx >= 0 {
						sub := trimmedURL[idx+len("/media/staging/"):]
						if end := strings.IndexAny(sub, "/?#"); end >= 0 {
							stagingID = sub[:end]
						} else {
							stagingID = sub
						}
					} else if idx := strings.Index(trimmedURL, "/v3/media-staging/stg_"); idx >= 0 {
						sub := trimmedURL[idx+len("/v3/media-staging/"):]
						if end := strings.IndexAny(sub, "/?#"); end >= 0 {
							stagingID = sub[:end]
						} else {
							stagingID = sub
						}
					}
					if stagingID != "" {
						if s.mediaStaging == nil {
							writeError(w, http.StatusServiceUnavailable, errors.New("media staging is unavailable"))
							return
						}
						stgRecord, stgBytes, readErr := s.mediaStaging.Read(p.AccountScopeID, stagingID, time.Now().UnixMilli())
						if readErr != nil || len(stgBytes) == 0 {
							writeError(w, http.StatusBadRequest, errors.New("cannot resolve staged media"))
							return
						}
						if readErr == nil && len(stgBytes) > 0 {
							asset, _, putErr := s.sessions.PutSessionMediaAsset(pebblestore.PutSessionMediaAssetInput{
								AccountScopeID:   p.AccountScopeID,
								SessionID:        targetSessionID,
								Modality:         routedSessionModality(stgRecord.DetectedMIMEType),
								DeclaredMIMEType: stgRecord.DeclaredMIMEType,
								FileType:         stgRecord.DetectedMIMEType,
								FileName:         stgRecord.FileName,
								Reader:           bytes.NewReader(stgBytes),
							})
							if putErr != nil {
								writeError(w, http.StatusBadRequest, fmt.Errorf("retain project media: %w", putErr))
								return
							}
							if putErr == nil {
								_, _, bindErr := s.mediaStaging.Bind(pebblestore.BindMediaStagingInput{
									AccountScopeID: p.AccountScopeID,
									SessionID:      targetSessionID,
									Bindings: []pebblestore.MediaStagingBinding{{
										StagingID:        stgRecord.ID,
										AuthorityAssetID: asset.ID,
										DigestSHA256:     asset.DigestSHA256,
									}},
								})
								if bindErr != nil {
									writeError(w, http.StatusBadRequest, fmt.Errorf("bind project media: %w", bindErr))
									return
								}
								item.URL = fmt.Sprintf("/v3/sessions/%s/media/%s", targetSessionID, asset.ID)
								item.ID = asset.ID
								item.DigestSHA256 = asset.DigestSHA256
								item.SizeBytes = asset.Size
								item.MediaType = asset.DetectedMIMEType
								item.Kind = asset.Modality
								item.Filename = asset.FileName
								item.Data = ""
							}
						}
					} else if strings.HasPrefix(trimmedURL, "data:") || strings.HasPrefix(item.Data, "data:") || len(item.Data) > 0 {
						dataPayload, dataMIME, decodeErr := s.resolveProjectUploadBytes(r.Context(), p, item)
						if decodeErr != nil || len(dataPayload) == 0 {
							writeError(w, http.StatusBadRequest, errors.New("cannot decode project media source"))
							return
						}
						if decodeErr == nil && len(dataPayload) > 0 && s.sessions != nil {
							asset, _, putErr := s.sessions.PutSessionMediaAsset(pebblestore.PutSessionMediaAssetInput{
								AccountScopeID:   p.AccountScopeID,
								SessionID:        targetSessionID,
								DeclaredMIMEType: dataMIME,
								FileName:         item.Filename,
								Reader:           bytes.NewReader(dataPayload),
							})
							if putErr != nil {
								writeError(w, http.StatusBadRequest, fmt.Errorf("retain project media: %w", putErr))
								return
							}
							if putErr == nil {
								item.URL = fmt.Sprintf("/v3/sessions/%s/media/%s", targetSessionID, asset.ID)
								item.ID = asset.ID
								item.DigestSHA256 = asset.DigestSHA256
								item.SizeBytes = asset.Size
								item.MediaType = asset.DetectedMIMEType
								item.Kind = asset.Modality
								item.Filename = asset.FileName
								item.Data = ""
							}
						}
					}
				} else {
					fn := strings.TrimSpace(r.Header.Get("X-Swarm-Media-Filename"))
					if fn == "" {
						if _, params, err := mime.ParseMediaType(r.Header.Get("Content-Disposition")); err == nil {
							fn = params["filename"]
						}
					}
					declMIME := strings.TrimSpace(r.Header.Get("Content-Type"))
					asset, _, putErr := s.sessions.PutSessionMediaAsset(pebblestore.PutSessionMediaAssetInput{
						AccountScopeID:   p.AccountScopeID,
						SessionID:        targetSessionID,
						DeclaredMIMEType: declMIME,
						FileName:         fn,
						Reader:           r.Body,
					})
					if putErr != nil {
						writeError(w, http.StatusBadRequest, putErr)
						return
					}
					item = pebblestore.ProjectTaskMediaRef{
						ID:           asset.ID,
						Title:        asset.FileName,
						Filename:     asset.FileName,
						Kind:         asset.Modality,
						MediaType:    asset.DetectedMIMEType,
						URL:          fmt.Sprintf("/v3/sessions/%s/media/%s", targetSessionID, asset.ID),
						SizeBytes:    asset.Size,
						DigestSHA256: asset.DigestSHA256,
						CreatedAt:    time.Now().UnixMilli(),
					}
				}
				if strings.TrimSpace(item.ID) == "" {
					item.ID = fmt.Sprintf("med_%d", time.Now().UnixNano())
				}
				if item.CreatedAt == 0 {
					item.CreatedAt = time.Now().UnixMilli()
				}
				if item.Title == "" {
					item.Title = item.Filename
				}
				if item.Filename != "" {
					item.Filename = pebblestore.SanitizeMediaFilename(item.Filename, item.ID, "")
				}
				updatedList := []pebblestore.ProjectTaskMediaRef{}
				_, err = db.UpdateProject(p.AccountScopeID, projectID, func(p *pebblestore.ProjectRecord) error {
					// Retention is content-addressed. A retry after a lost response
					// must return the existing shelf entry, not append it again.
					for _, existing := range p.UploadedMedia {
						if existing.ID != item.ID {
							continue
						}
						if existing.URL != item.URL || existing.Data != item.Data || existing.DigestSHA256 != item.DigestSHA256 || existing.SizeBytes != item.SizeBytes || existing.MediaType != item.MediaType || existing.Kind != item.Kind {
							return errors.New("project media identity conflicts with an existing attachment")
						}
						item = existing
						updatedList = p.UploadedMedia
						return nil
					}
					p.UploadedMedia = append(p.UploadedMedia, item)
					updatedList = p.UploadedMedia
					return nil
				})
				if err != nil {
					writeError(w, http.StatusInternalServerError, err)
					return
				}
				writeJSON(w, http.StatusCreated, map[string]any{
					"media":          item,
					"uploaded_media": updatedList,
				})
				return
			}
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}

		if len(segments) == 3 {
			mediaID := segments[2]
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				if !s.requireScopeAny(w, r, "projects:read", "sessions:read") {
					return
				}
				var targetMedia *pebblestore.ProjectTaskMediaRef
				for _, m := range proj.UploadedMedia {
					if m.ID == mediaID {
						item := m
						targetMedia = &item
						break
					}
				}
				if targetMedia == nil {
					writeError(w, http.StatusNotFound, errors.New("media not found"))
					return
				}
				bytesPayload, mediaType, err := s.resolveSourceMediaBytes(r.Context(), p, *targetMedia, "")
				if err != nil || len(bytesPayload) == 0 {
					writeError(w, http.StatusNotFound, errors.New("media file is unavailable"))
					return
				}
				if mediaType == "" {
					mediaType = targetMedia.MediaType
				}
				if mediaType == "" {
					mediaType = "application/octet-stream"
				}
				filename := targetMedia.Filename
				if filename == "" {
					filename = targetMedia.Title
				}
				if filename == "" {
					filename = targetMedia.ID
				}
				disposition := mime.FormatMediaType("inline", map[string]string{"filename": filename})
				if disposition == "" {
					disposition = "inline"
				}
				w.Header().Set("Content-Type", mediaType)
				w.Header().Set("Content-Disposition", disposition)
				w.Header().Set("X-Content-Type-Options", "nosniff")
				w.Header().Set("Accept-Ranges", "bytes")
				w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src data: blob:; media-src 'self' data: blob:; style-src 'unsafe-inline'; font-src data:; frame-ancestors 'self'")
				w.Header().Set("Referrer-Policy", "no-referrer")
				modTime := time.UnixMilli(targetMedia.CreatedAt)
				if targetMedia.CreatedAt == 0 {
					modTime = time.Now()
				}
				http.ServeContent(w, r, filename, modTime, bytes.NewReader(bytesPayload))
				return
			}
			if r.Method == http.MethodDelete {
				if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
					return
				}
				updatedList := []pebblestore.ProjectTaskMediaRef{}
				_, err = db.UpdateProject(p.AccountScopeID, projectID, func(p *pebblestore.ProjectRecord) error {
					for _, m := range p.UploadedMedia {
						if m.ID != mediaID {
							updatedList = append(updatedList, m)
						}
					}
					p.UploadedMedia = updatedList
					return nil
				})
				if err != nil {
					writeError(w, http.StatusInternalServerError, err)
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{
					"removed":        true,
					"media_id":       mediaID,
					"uploaded_media": updatedList,
				})
				return
			}
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
	}

	// 4b. Task preview: POST /v3/projects/{id}/tasks:preview
	if len(segments) == 2 && (segments[1] == "tasks:preview" || segments[1] == "tasks-preview") {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if !s.requireScopeAny(w, r, "projects:read", "sessions:read") {
			return
		}
		body, ok := readProjectMediaRequest(w, r)
		if !ok {
			return
		}
		var previewReq struct {
			Prompt              string `json:"prompt"`
			Title               string `json:"title"`
			Intent              string `json:"intent"`
			FeatureSize         string `json:"feature_size"`
			Agent               string `json:"agent"`
			OutcomeType         string `json:"outcome_type"`
			Tier                string `json:"tier"`
			Model               string `json:"model"`
			Provider            string `json:"provider"`
			Thinking            string `json:"thinking"`
			ServiceTier         string `json:"service_tier"`
			ContextMode         string `json:"context_mode"`
			WorkspacePath       string `json:"workspace_path"`
			WorkspaceID         string `json:"workspace_id"`
			WorkspaceGeneration int64  `json:"workspace_generation"`
		}
		if err := json.Unmarshal(body, &previewReq); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		proj, found, err := db.GetProject(p.AccountScopeID, projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found || proj == nil {
			writeError(w, http.StatusNotFound, errors.New("project not found"))
			return
		}
		if proj.AccountID != "" && proj.AccountID != p.AccountScopeID {
			writeError(w, http.StatusForbidden, errors.New("cross-account project preview forbidden"))
			return
		}
		requiresRepo := previewReq.Agent != "image" && previewReq.Agent != "video" && previewReq.Agent != "sound" && previewReq.Agent != "audio"
		var source pebblestore.ProjectTaskSource
		if previewReq.Agent != "image" && previewReq.Intent != "image" && (previewReq.WorkspacePath != "" || previewReq.WorkspaceID != "" || previewReq.WorkspaceGeneration != 0) {
			source, err = s.resolveProjectTaskSource(p, proj, previewReq.WorkspacePath, previewReq.WorkspaceID, previewReq.WorkspaceGeneration, requiresRepo)
		}
		// Workspace readiness is a separate diagnostic, never model authority.
		workspaceDiagnostic := ""
		if err != nil {
			workspaceDiagnostic = err.Error()
			source = pebblestore.ProjectTaskSource{}
		}
		projectContext := proj.ProjectContext
		prompt := strings.TrimSpace(previewReq.Prompt)
		if prompt == "" {
			prompt = strings.TrimSpace(previewReq.Title)
		}
		routed, rErr := pebblestore.RouteAndPlanProjectTaskWithOptions(pebblestore.TaskPlanOptions{
			Prompt:             prompt,
			RequestedWorkspace: source.Path,
			ProjectContext:     projectContext,
			// Preview has no authority to infer an execution workspace.
			Workspaces:  nil,
			Intent:      previewReq.Intent,
			FeatureSize: previewReq.FeatureSize,
			Agent:       previewReq.Agent,
			OutcomeType: previewReq.OutcomeType,
			Tier:        previewReq.Tier,
		})
		if rErr != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid task preview configuration: %w", rErr))
			return
		}
		dummyTask := &pebblestore.ProjectTaskRecord{
			ProjectID:   projectID,
			Agent:       routed.Agent,
			Model:       previewReq.Model,
			Provider:    previewReq.Provider,
			Thinking:    previewReq.Thinking,
			ServiceTier: previewReq.ServiceTier,
			ContextMode: previewReq.ContextMode,
			FeatureSize: previewReq.FeatureSize,
		}
		modelPrev := s.buildTaskModelPreview(p, dummyTask)
		writeJSON(w, http.StatusOK, map[string]any{
			"task_plan":            routed,
			"source_workspace":     source,
			"model_preview":        modelPrev,
			"workspace_diagnostic": workspaceDiagnostic,
		})
		return
	}

	// 4. Project Tasks collection: /v3/projects/{id}/tasks
	if len(segments) == 2 && segments[1] == "tasks" {
		if r.Method == http.MethodGet {
			if !s.requireScopeAny(w, r, "projects:read", "sessions:read") {
				return
			}
			archiveView := r.URL.Query().Get("view")
			if archiveView != "" && archiveView != "archived" {
				writeError(w, http.StatusBadRequest, errors.New("unsupported task view"))
				return
			}
			started := time.Now()
			var reconcileElapsed time.Duration
			var summaries = make(map[string]projectTaskBoardRelated)
			tasks, stats, err := db.ReadProjectTaskBoard(p.AccountScopeID, projectID, archiveView == "archived", func(rows []pebblestore.ProjectTaskRecord, reader *pebblestore.ProjectTaskBoardReader) {
				reconcileStart := time.Now()
				for i := range rows {
					// Delivery is deliberately unassessed on collection reads.
					rows[i].IsIntegrated, rows[i].UnintegratedCommits = false, 0
					reader.BindTask(&rows[i])
					syncTaskSessionState(&rows[i], reader)
					summaries[rows[i].ID] = projectTaskBoardSummary(&rows[i], reader)
					rows[i].TaskProgramStatus, rows[i].PlanDocument = nil, nil
				}
				reconcileElapsed = time.Since(reconcileStart)
			})
			if err != nil {
				if errors.Is(err, pebblestore.ErrProjectTaskSummariesNotReady) {
					w.Header().Set("Retry-After", "1")
					writeError(w, http.StatusServiceUnavailable, err)
				} else {
					writeError(w, http.StatusInternalServerError, err)
				}
				return
			}
			workerOnly := r.URL.Query().Get("worker_only") == "true"
			targetWorkerID := strings.TrimSpace(r.URL.Query().Get("worker_id"))
			if workerOnly || targetWorkerID != "" {
				filtered := make([]pebblestore.ProjectTaskRecord, 0, len(tasks))
				for _, t := range tasks {
					if t.WorkerID == "" {
						continue
					}
					if targetWorkerID != "" && t.WorkerID != targetWorkerID {
						continue
					}
					filtered = append(filtered, t)
				}
				tasks = filtered
			}
			for i := range tasks {
				if archiveView != "archived" && tasks[i].WorktreeName == "" {
					tasks[i].WorktreeName = strings.TrimPrefix(tasks[i].WorktreeBranch, "agent/")
					tasks[i].WorktreeName = strings.TrimPrefix(tasks[i].WorktreeName, "worktree/")
				}
				// A collection response cannot advertise old delivery facts as current.
				tasks[i].IsIntegrated, tasks[i].UnintegratedCommits = false, 0
				tasks[i].DeliveryAssessment = &pebblestore.TaskDeliveryAssessment{
					AccountID: tasks[i].AccountID, TaskID: tasks[i].ID, TaskRevision: tasks[i].Revision,
					SessionID: tasks[i].SessionID, AttemptID: tasks[i].ActiveAttemptID,
					WorkspaceID: tasks[i].SourceWorkspace.WorkspaceID, WorkspaceGeneration: tasks[i].SourceWorkspace.WorkspaceGeneration,
					BaseOID: tasks[i].BaseCommit, SourceBranch: tasks[i].WorktreeBranch, TargetBranch: tasks[i].BaseBranch,
					State: "unavailable", ReasonCode: "not_assessed", Reason: "Fetch task detail for a current delivery assessment", Freshness: "stale", AllowedActions: []string{},
				}
				// Collection GET does not run expensive git subprocesses to avoid CPU churn.
				// Mark Git status as explicit "unknown" (or "stale" if previously recorded)
				// so callers know git state has not been freshly verified, without disabling operations.
				if tasks[i].Agent != "image" && tasks[i].Agent != "video" && tasks[i].Agent != "sound" && tasks[i].Agent != "audio" &&
					tasks[i].Status != "pending_approval" && tasks[i].Status != "planning" && tasks[i].Status != "queued" {
					if tasks[i].GitStatus == "" {
						tasks[i].GitStatus = "unknown"
					} else {
						tasks[i].GitStatus = "stale"
					}
				}
				// Lifecycle was reconciled from the same snapshot as these rows.
			}
			sanitizedTasks := make([]pebblestore.ProjectTaskRecord, len(tasks))
			for i := range tasks {
				sanitizedTasks[i] = *sanitizeProjectTaskForClient(&tasks[i])
			}
			board := make([]projectTaskBoardRow, len(sanitizedTasks))
			for i := range sanitizedTasks {
				board[i] = projectTaskBoardRow{ProjectTaskRecord: sanitizedTasks[i], BoardSummary: summaries[sanitizedTasks[i].ID]}
			}
			writeProjectTaskBoard(w, board, stats, started, reconcileElapsed)
			return
		}

		if r.Method == http.MethodPost {
			if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
				return
			}
			body, ok := readProjectMediaRequest(w, r)
			if !ok {
				return
			}
			var req struct {
				ID                  string                                   `json:"id,omitempty"`
				Title               string                                   `json:"title"`
				Description         string                                   `json:"description,omitempty"`
				Status              string                                   `json:"status,omitempty"`
				Agent               string                                   `json:"agent,omitempty"`
				WorkerID            string                                   `json:"worker_id,omitempty"`
				WorkerName          string                                   `json:"worker_name,omitempty"`
				WorkerRunID         string                                   `json:"worker_run_id,omitempty"`
				AutomationID        string                                   `json:"automation_id,omitempty"`
				OutcomeType         string                                   `json:"outcome_type,omitempty"`
				Operation           string                                   `json:"operation,omitempty"`
				WorkspacePath       string                                   `json:"workspace_path,omitempty"`
				WorkspaceID         string                                   `json:"workspace_id,omitempty"`
				WorkspaceGeneration int64                                    `json:"workspace_generation,omitempty"`
				ClientRequestID     string                                   `json:"client_request_id,omitempty"`
				WorktreeBranch      string                                   `json:"worktree_branch,omitempty"`
				GitStatus           string                                   `json:"git_status,omitempty"`
				UnintegratedCommits int                                      `json:"unintegrated_commits,omitempty"`
				DiffSummary         string                                   `json:"diff_summary,omitempty"`
				IsDirty             bool                                     `json:"is_dirty,omitempty"`
				ActionNeeded        string                                   `json:"action_needed,omitempty"`
				WhatDidDo           []string                                 `json:"what_did_do,omitempty"`
				WhatNotDone         []string                                 `json:"what_not_done,omitempty"`
				PipelineStages      []string                                 `json:"pipeline_stages,omitempty"`
				CurrentStageIndex   int                                      `json:"current_stage_index,omitempty"`
				Deliverables        []pebblestore.ProjectTaskDeliverable     `json:"deliverables,omitempty"`
				WorkspacesInvolved  []string                                 `json:"workspaces_involved,omitempty"`
				PlanSummary         string                                   `json:"plan_summary,omitempty"`
				FullPlanMarkdown    string                                   `json:"full_plan_markdown,omitempty"`
				Tier                string                                   `json:"tier,omitempty"`
				Revision            int                                      `json:"revision,omitempty"`
				LastError           string                                   `json:"last_error,omitempty"`
				DeploySession       bool                                     `json:"deploy_session,omitempty"`
				Prompt              string                                   `json:"prompt,omitempty"`
				Intent              string                                   `json:"intent,omitempty"`
				FeatureSize         string                                   `json:"feature_size,omitempty"`
				VideoType           string                                   `json:"video_type,omitempty"`
				EnhancePrompt       *bool                                    `json:"enhance_prompt,omitempty"`
				AspectRatio         string                                   `json:"aspect_ratio,omitempty"`
				Resolution          string                                   `json:"resolution,omitempty"`
				Model               string                                   `json:"model,omitempty"`
				Provider            string                                   `json:"provider,omitempty"`
				Thinking            string                                   `json:"thinking,omitempty"`
				ServiceTier         string                                   `json:"service_tier,omitempty"`
				ContextMode         string                                   `json:"context_mode,omitempty"`
				VariantCount        int                                      `json:"variant_count,omitempty"`
				DeliverableCount    int                                      `json:"deliverable_count,omitempty"`
				ScenesCount         int                                      `json:"scenes_count,omitempty"`
				Scenes              []pebblestore.ProjectTaskScene           `json:"scenes,omitempty"`
				Soundtrack          string                                   `json:"soundtrack,omitempty"`
				DurationSeconds     int                                      `json:"duration_seconds,omitempty"`
				AutoApprove         bool                                     `json:"auto_approve,omitempty"`
				AttachedMedia       []pebblestore.ProjectTaskMediaRef        `json:"attached_media,omitempty"`
				CoderAssignments    []pebblestore.ProjectTaskCoderAssignment `json:"coder_assignments,omitempty"`
				TaskProgram         *pebblestore.TaskProgramDefinition       `json:"task_program,omitempty"`
				TaskProgramID       string                                   `json:"task_program_id,omitempty"`
				Document            *pebblestore.SessionPlanDocument         `json:"document,omitempty"`
				PlanDocument        *pebblestore.SessionPlanDocument         `json:"plan_document,omitempty"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				writeError(w, http.StatusBadRequest, errors.New("invalid JSON payload"))
				return
			}
			if strings.TrimSpace(req.WorkerID) != "" || strings.TrimSpace(req.WorkerRunID) != "" || strings.TrimSpace(req.AutomationID) != "" {
				writeError(w, http.StatusBadRequest, errors.New("worker identity fields (worker_id, worker_run_id, automation_id) are server-managed receipts and cannot be specified by client"))
				return
			}

			isMediaRequest := req.Agent == "image" || req.Agent == "video" || req.Agent == "sound" || req.Agent == "audio" || req.Intent == "image" || req.Intent == "video" || req.Intent == "sound" || req.Intent == "audio" || req.Operation != ""
			if isMediaRequest {
				count, err := projectMediaBatchCount(req.VariantCount, req.DeliverableCount, len(req.Deliverables))
				if err != nil {
					writeError(w, http.StatusBadRequest, err)
					return
				}
				req.VariantCount = count
			}
			if !isMediaRequest || req.Agent == "image" || req.Intent == "image" {
				input := tool.ProjectTaskCreateInput{
					ID:                  req.ID,
					Title:               req.Title,
					Description:         req.Description,
					Prompt:              req.Prompt,
					Agent:               req.Agent,
					Intent:              req.Intent,
					EnhancePrompt:       req.EnhancePrompt != nil && *req.EnhancePrompt,
					WorkerName:          req.WorkerName,
					FeatureSize:         req.FeatureSize,
					WorkspacePath:       req.WorkspacePath,
					WorkspaceID:         req.WorkspaceID,
					WorkspaceGeneration: req.WorkspaceGeneration,
					ClientRequestID:     req.ClientRequestID,
					WorktreeBranch:      req.WorktreeBranch,
					OutcomeType:         req.OutcomeType,
					Operation:           req.Operation,
					Tier:                req.Tier,
					AspectRatio:         req.AspectRatio,
					Resolution:          req.Resolution,
					VariantCount:        req.VariantCount,
					DurationSeconds:     req.DurationSeconds,
					Model:               req.Model,
					Provider:            req.Provider,
					Thinking:            req.Thinking,
					ServiceTier:         req.ServiceTier,
					ContextMode:         req.ContextMode,
					Soundtrack:          req.Soundtrack,
					AutoApprove:         req.AutoApprove,
					PipelineStages:      req.PipelineStages,
					Deliverables:        req.Deliverables,
					WhatDidDo:           req.WhatDidDo,
					WhatNotDone:         req.WhatNotDone,
					AttachedMedia:       req.AttachedMedia,
					Document:            req.Document,
					PlanDocument:        req.PlanDocument,
					CoderAssignments:    req.CoderAssignments,
					TaskProgram:         req.TaskProgram,
					TaskProgramID:       req.TaskProgramID,
					PlanSummary:         req.PlanSummary,
					FullPlanMarkdown:    req.FullPlanMarkdown,
					DiffSummary:         req.DiffSummary,
				}
				if input.VariantCount <= 0 && req.DeliverableCount > 0 {
					input.VariantCount = req.DeliverableCount
				}

				task, err := s.CreateProjectTask(r.Context(), p, projectID, input)
				if err != nil {
					status := http.StatusBadRequest
					if strings.Contains(err.Error(), "forbidden") {
						status = http.StatusForbidden
					} else if strings.Contains(err.Error(), "not found") {
						status = http.StatusNotFound
					} else if strings.Contains(err.Error(), "worktree allocation failed") || strings.Contains(err.Error(), "internal") {
						status = http.StatusInternalServerError
					}
					writeError(w, status, err)
					return
				}
				writeJSON(w, http.StatusCreated, map[string]any{"task": sanitizeProjectTaskForClient(task), "model_preview": s.buildTaskModelPreview(p, task)})
				return
			}
			proj, found, err := db.GetProject(p.AccountScopeID, projectID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			if !found || proj == nil {
				writeError(w, http.StatusNotFound, errors.New("project not found"))
				return
			}

			// Bind retries to the exact full media request before routing or dispatch.
			mediaTaskID := strings.TrimSpace(req.ID)
			if mediaTaskID == "" {
				mediaTaskID = "task_" + sessionruntime.NewSessionID()
			}
			unlockMediaAdmission := s.lockProjectTaskAdmission(p.AccountScopeID, projectID, mediaTaskID)
			defer unlockMediaAdmission()
			mediaSubmissionHash := fmt.Sprintf("%x", sha256.Sum256(body))
			if existing, found, err := db.GetProjectTask(p.AccountScopeID, projectID, mediaTaskID); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			} else if found {
				if existing.SubmissionHash != mediaSubmissionHash {
					writeError(w, http.StatusConflict, errors.New("media task submission conflicts with reserved payload"))
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"task": sanitizeProjectTaskForClient(existing), "model_preview": s.buildTaskModelPreview(p, existing)})
				return
			}

			prompt := strings.TrimSpace(req.Prompt)
			if prompt == "" && strings.TrimSpace(req.Title) != "" {
				prompt = strings.TrimSpace(req.Title)
			}

			reqOp := strings.ToLower(strings.TrimSpace(req.Operation))
			if reqOp != "" && reqOp != pebblestore.VideoOperationCreate && reqOp != pebblestore.VideoOperationEdit && reqOp != pebblestore.VideoOperationExtend {
				writeError(w, http.StatusBadRequest, fmt.Errorf("task operation %q is invalid; must be create, edit, or extend", req.Operation))
				return
			}

			isDirectVideo := req.Intent == "video" || req.Agent == "video" || reqOp == pebblestore.VideoOperationEdit || reqOp == pebblestore.VideoOperationExtend || (reqOp == pebblestore.VideoOperationCreate && req.Agent == "video")
			var vModel string
			var vProvider string
			var vidClipCount int
			var normAR string
			var normRes string
			var normDur int
			var sourceVideo *videogen.ManagedVideoSource
			var sourceImage *videogen.ManagedVideoImage

			// Preflight check for video tasks before Router spend or persistence
			if isDirectVideo {
				if req.DurationSeconds < 0 || req.TaskProgram != nil || req.TaskProgramID != "" {
					writeError(w, http.StatusBadRequest, errors.New("video tasks require nonnegative duration and cannot carry a task program"))
					return
				}
				if req.VideoType == "multipart" || req.VideoType == "story" || req.ScenesCount > 1 || req.OutcomeType == "video_story" || len(req.Scenes) > 0 {
					if err := validateVideoScenes(req.Scenes, reqOp, max(req.VariantCount, req.DeliverableCount), req.Soundtrack); err != nil {
						writeError(w, http.StatusBadRequest, err)
						return
					}
					if req.ScenesCount > 0 && req.ScenesCount != len(req.Scenes) {
						writeError(w, http.StatusBadRequest, errors.New("scene count does not match explicit scenes"))
						return
					}
				}
				if req.VariantCount < 0 {
					writeError(w, http.StatusBadRequest, errors.New("video variant count cannot be negative"))
					return
				}
				if req.DeliverableCount < 0 {
					writeError(w, http.StatusBadRequest, errors.New("video deliverable count cannot be negative"))
					return
				}
				vidClipCount = req.VariantCount
				if vidClipCount == 0 {
					vidClipCount = req.DeliverableCount
				}
				if vidClipCount == 0 {
					vidClipCount = 1
				}
				if vidClipCount > 8 {
					writeError(w, http.StatusBadRequest, fmt.Errorf("video variant count %d exceeds maximum allowed (8)", vidClipCount))
					return
				}

				for _, m := range req.AttachedMedia {
					if hasConflictingMediaDeclaration(m) {
						writeError(w, http.StatusBadRequest, errors.New("conflicting attachment declarations between kind, media_type, and filename"))
						return
					}
				}

				if reqOp == "" {
					if len(req.AttachedMedia) == 1 && isVideoAttachment(req.AttachedMedia[0]) {
						writeError(w, http.StatusBadRequest, errors.New("video operation must be explicitly specified when source media is provided (edit or extend)"))
						return
					}
					reqOp = pebblestore.VideoOperationCreate
				}

				if reqOp == pebblestore.VideoOperationCreate {
					if len(req.AttachedMedia) > 1 {
						writeError(w, http.StatusBadRequest, errors.New("at most one initial image attachment is supported for video generation"))
						return
					}
					if len(req.AttachedMedia) == 1 {
						if isVideoAttachment(req.AttachedMedia[0]) {
							writeError(w, http.StatusBadRequest, errors.New("cannot provide source video for create operation; use edit or extend"))
							return
						}
						if !isImageAttachment(req.AttachedMedia[0]) {
							writeError(w, http.StatusBadRequest, fmt.Errorf("unsupported attachment kind %q for video generation; only images are supported as reference inputs", req.AttachedMedia[0].Kind))
							return
						}
					}
				} else {
					if len(req.AttachedMedia) == 0 {
						writeError(w, http.StatusBadRequest, fmt.Errorf("video %s operation requires source video", reqOp))
						return
					}
					if len(req.AttachedMedia) > 1 {
						writeError(w, http.StatusBadRequest, fmt.Errorf("video %s operation requires exactly 1 source video; multiple attachments are not supported", reqOp))
						return
					}
					if isImageAttachment(req.AttachedMedia[0]) {
						writeError(w, http.StatusBadRequest, fmt.Errorf("initial image input is not supported for video %s operation; only create operation supports initial image", reqOp))
						return
					}
					if !isVideoAttachment(req.AttachedMedia[0]) {
						writeError(w, http.StatusBadRequest, fmt.Errorf("unsupported attachment kind %q for video %s operation; only video attachments are supported", req.AttachedMedia[0].Kind, reqOp))
						return
					}
				}

				if reqOp == pebblestore.VideoOperationCreate {
					if len(req.AttachedMedia) == 1 {
						att := req.AttachedMedia[0]
						if att.Kind != "" && !strings.EqualFold(att.Kind, "image") {
							writeError(w, http.StatusBadRequest, fmt.Errorf("unsupported attachment kind %q for video generation; only images are supported as reference inputs", att.Kind))
							return
						}
						if att.MediaType != "" && !strings.HasPrefix(strings.ToLower(att.MediaType), "image/") {
							writeError(w, http.StatusBadRequest, fmt.Errorf("unsupported media type %q for video generation; only images are supported as reference inputs", att.MediaType))
							return
						}
						fn := strings.ToLower(strings.TrimSpace(att.Filename))
						if fn != "" && !strings.HasSuffix(fn, ".png") && !strings.HasSuffix(fn, ".jpg") && !strings.HasSuffix(fn, ".jpeg") {
							writeError(w, http.StatusBadRequest, fmt.Errorf("unsupported file extension on %q for video generation; only PNG and JPEG image formats are supported", att.Filename))
							return
						}
						imgBytes, mType, err := s.resolveSourceMediaBytes(r.Context(), p, att, "image")
						if err != nil {
							writeError(w, http.StatusBadRequest, fmt.Errorf("invalid image attachment: %w", err))
							return
						}
						if len(imgBytes) == 0 {
							writeError(w, http.StatusBadRequest, errors.New("attached image payload is empty"))
							return
						}
						if err := validateImageBytes(imgBytes, mType); err != nil {
							writeError(w, http.StatusBadRequest, err)
							return
						}
						sourceImage = &videogen.ManagedVideoImage{
							Bytes:     imgBytes,
							MediaType: mType,
						}
					}
				} else {
					att := req.AttachedMedia[0]
					srcRec, err := s.resolveSourceMediaRecord(r.Context(), p, att, "video", proj.ID)
					if err != nil {
						writeError(w, http.StatusBadRequest, fmt.Errorf("invalid video attachment: %w", err))
						return
					}
					if len(srcRec.Bytes) == 0 && srcRec.Provenance == nil {
						writeError(w, http.StatusBadRequest, errors.New("attached video payload is empty"))
						return
					}
					sourceVideo = &videogen.ManagedVideoSource{
						Bytes:      srcRec.Bytes,
						MediaType:  srcRec.MediaType,
						Provenance: srcRec.Provenance,
						SourceLink: srcRec.SourceLink,
					}
					if srcRec.Provenance != nil {
						sourceVideo.InteractionID = srcRec.Provenance.InteractionID
						sourceVideo.URI = srcRec.Provenance.ProviderResource
						sourceVideo.Model = srcRec.Provenance.Model
					}
				}

				pfReq := videogen.VideoPreflightRequest{
					AccountScopeID:  p.AccountScopeID,
					Operation:       reqOp,
					ExplicitModel:   strings.TrimSpace(req.Model),
					AspectRatio:     req.AspectRatio,
					Resolution:      req.Resolution,
					DurationSeconds: req.DurationSeconds,
					Prompt:          prompt,
					Principal:       p,
					Source:          sourceVideo,
					SourceProvenance: func() *pebblestore.VideoProvenance {
						if sourceVideo != nil {
							return sourceVideo.Provenance
						}
						return nil
					}(),
					Image:        sourceImage,
					IsQueuedTask: false,
				}
				pfRes, pfErr := s.preflightVideoOperation(r.Context(), pfReq)
				if pfErr != nil {
					writeError(w, http.StatusBadRequest, pfErr)
					return
				}

				vModel = pfRes.ResolvedModel
				vProvider = pfRes.ResolvedProvider
				reqOp = pfRes.Operation
				normAR = pfRes.AspectRatio
				normRes = pfRes.Resolution
				normDur = pfRes.DurationSeconds
			}

			isDirectSound := req.Intent == "sound" || req.Intent == "audio"
			enhancePrompt := req.EnhancePrompt != nil && *req.EnhancePrompt

			taskRouter := taskrouter.NewService(func(ctx context.Context, instructions, input string) (string, error) {
				res, err := s.invokeConfiguredRouterOnce(ctx, p, instructions, input, 64<<10)
				if err != nil {
					return "", err
				}
				return res.Text, nil
			})
			var routed pebblestore.TaskRouteResult
			if isDirectSound {
				soundModel := strings.TrimSpace(req.Model)
				if soundModel == "" && s.uiSettings != nil && strings.TrimSpace(p.AccountScopeID) != "" {
					if uiSet, err := s.uiSettings.GetForAccount(p.AccountScopeID); err == nil {
						if def := strings.TrimSpace(uiSet.Tools.Audio.DefaultModel); def != "" {
							soundModel = def
						}
					}
				}
				if soundModel == "" {
					soundModel = "lyria-3.5"
				}
				durSeconds := req.DurationSeconds
				if durSeconds <= 0 {
					durSeconds = 30
				}
				taskTitle, err := taskRouter.NameTask(r.Context(), prompt, req.Title)
				if err != nil {
					writeError(w, http.StatusBadRequest, fmt.Errorf("task router naming: %w", err))
					return
				}
				routed = pebblestore.TaskRouteResult{
					Title:            taskTitle,
					Agent:            "sound",
					OutcomeType:      "audio_clip",
					Tier:             "direct",
					VariantCount:     1,
					Stages:           []string{"Audio Parameter Configuration", "Model Sound Synthesis"},
					PlanSummary:      fmt.Sprintf("1. Configure %ds audio soundtrack parameters\n2. Synthesize with %s\n3. Deliver verified soundtrack clip", durSeconds, soundModel),
					FullPlanMarkdown: fmt.Sprintf("### Task Mission: %s\n\n- **Agent**: `@sound`\n- **Model**: `%s`\n- **Duration**: `%ds`\n\n#### Prompt\n%s\n", taskTitle, soundModel, durSeconds, prompt),
					Deliverables: []pebblestore.ProjectTaskDeliverable{
						{
							ID:          "deliv_snd",
							Title:       taskTitle,
							Kind:        "audio",
							Status:      "pending",
							Thumbnail:   "sound",
							Duration:    fmt.Sprintf("%ds", durSeconds),
							Description: fmt.Sprintf("Generated %ds audio soundtrack using %s: %s", durSeconds, soundModel, prompt),
						},
					},
					AttachedMedia: req.AttachedMedia,
				}
			} else if isDirectVideo && !enhancePrompt {
				durStr := ""
				if normDur > 0 {
					durStr = fmt.Sprintf("%ds", normDur)
				}

				taskTitle, err := taskRouter.NameTask(r.Context(), prompt, req.Title)
				if err != nil {
					writeError(w, http.StatusBadRequest, fmt.Errorf("task router naming: %w", err))
					return
				}
				cleanTitle := taskTitle

				var deliverables []pebblestore.ProjectTaskDeliverable
				if vidClipCount > 1 {
					for i := 1; i <= vidClipCount; i++ {
						deliverables = append(deliverables, pebblestore.ProjectTaskDeliverable{
							ID:          fmt.Sprintf("deliv_vid_%d", i),
							Title:       fmt.Sprintf("%s (Take %d, %s)", cleanTitle, i, normAR),
							Kind:        "video",
							Status:      "pending",
							Duration:    durStr,
							Description: fmt.Sprintf("Video clip %d of %d (%s, %s, %s) generated directly with %s: %s", i, vidClipCount, normAR, normRes, durStr, vModel, prompt),
						})
					}
				} else {
					deliverables = []pebblestore.ProjectTaskDeliverable{
						{
							ID:          "deliv_vid",
							Title:       fmt.Sprintf("%s (Single Video, %s)", cleanTitle, normAR),
							Kind:        "video",
							Status:      "pending",
							Duration:    durStr,
							Description: fmt.Sprintf("Single video clip (%s, %s, %s) generated directly with %s: %s", normAR, normRes, durStr, vModel, prompt),
						},
					}
				}

				routed = pebblestore.TaskRouteResult{
					Title:            taskTitle,
					Agent:            "video",
					OutcomeType:      "video_clip",
					Tier:             "direct",
					AspectRatio:      normAR,
					VariantCount:     vidClipCount,
					Mission:          prompt, // FULL PROMPT PRESERVED!
					Stages:           []string{"Video Parameter Configuration", "Model Generative Synthesis"},
					PlanSummary:      fmt.Sprintf("1. Configure %s video shot (%s, %s)\n2. Render directly with %s using native model audio\n3. Deliver verified video clip for review", durStr, normAR, normRes, vModel),
					FullPlanMarkdown: fmt.Sprintf("### Task Mission: %s\n\n- **Agent**: `@video`\n- **Mode**: Single Video (%d Clip · %s)\n- **Model**: `%s`\n- **Resolution**: `%s`\n- **Aspect Ratio**: `%s`\n- **Audio**: Model Generative Audio (Synchronized in 1 prompt)\n\n#### Visual Prompt\n%s\n", taskTitle, vidClipCount, durStr, vModel, normRes, normAR, prompt),
					Deliverables:     deliverables,
					AttachedMedia:    req.AttachedMedia,
				}
			} else {
				if req.VariantCount <= 0 && req.DeliverableCount > 0 {
					req.VariantCount = req.DeliverableCount
				}
				var rErr error
				routed, rErr = taskRouter.RouteTask(r.Context(), taskrouter.TaskRouteOptions{
					Title:              req.Title,
					Prompt:             prompt,
					RequestedWorkspace: req.WorkspacePath,
					Intent:             req.Intent,
					FeatureSize:        req.FeatureSize,
					Agent:              req.Agent,
					OutcomeType:        req.OutcomeType,
					Tier:               req.Tier,
					VideoType:          req.VideoType,
					EnhancePrompt:      enhancePrompt,
					AspectRatio:        req.AspectRatio,
					VariantCount:       req.VariantCount,
					ScenesCount:        req.ScenesCount,
					Soundtrack:         req.Soundtrack,
					AutoApprove:        req.AutoApprove,
					AttachedMedia:      req.AttachedMedia,
					Project:            proj,
				})
				if rErr != nil {
					writeError(w, http.StatusBadRequest, fmt.Errorf("task router: %w", rErr))
					return
				}
				if isDirectVideo {
					routed.Agent = "video"
					routed.OutcomeType = "video_clip"
					routed.Branch = ""
					routed.Scenes = nil
					routed.Soundtrack = ""
					routed.VariantCount = vidClipCount
					routed.AspectRatio = normAR
					durText := ""
					if normDur > 0 {
						durText = fmt.Sprintf("%ds", normDur)
					}
					if vidClipCount == 1 {
						routed.Deliverables = []pebblestore.ProjectTaskDeliverable{
							{
								ID:          "deliv_vid",
								Title:       fmt.Sprintf("%s (Single Video, %s)", routed.Title, normAR),
								Kind:        "video",
								Status:      "pending",
								Duration:    durText,
								Description: fmt.Sprintf("Single video clip (%s, %s): %s", normAR, durText, routed.Title),
							},
						}
					} else {
						var delivs []pebblestore.ProjectTaskDeliverable
						for i := 1; i <= vidClipCount; i++ {
							delivs = append(delivs, pebblestore.ProjectTaskDeliverable{
								ID:          fmt.Sprintf("deliv_vid_%d", i),
								Title:       fmt.Sprintf("%s (Take %d, %s)", routed.Title, i, normAR),
								Kind:        "video",
								Status:      "pending",
								Duration:    durText,
								Description: fmt.Sprintf("Video clip %d of %d (%s, %s): %s", i, vidClipCount, normAR, durText, routed.Title),
							})
						}
						routed.Deliverables = delivs
					}

				}
			}

			title := strings.TrimSpace(req.Title)
			if title == "" {
				title = routed.Title
			}
			agentName := strings.TrimSpace(req.Agent)
			if agentName == "" {
				agentName = routed.Agent
			}
			outcomeType := strings.TrimSpace(req.OutcomeType)
			if outcomeType == "" {
				outcomeType = routed.OutcomeType
			}
			tier := strings.TrimSpace(req.Tier)
			if tier == "" {
				tier = routed.Tier
			}

			isMediaAgent := agentName == "image" || agentName == "video" || agentName == "sound" || agentName == "audio"

			var worktreeBranch, worktreeName string
			baseBranch := "dev"
			if isMediaAgent {
				baseBranch = ""
			} else {
				worktreeBranch = strings.TrimSpace(req.WorktreeBranch)
				if worktreeBranch == "" || worktreeBranch == "main" || worktreeBranch == "dev" || worktreeBranch == "master" {
					worktreeBranch = routed.Branch
				}
				if worktreeBranch == "" || worktreeBranch == "main" || worktreeBranch == "dev" || worktreeBranch == "master" {
					worktreeBranch, _ = pebblestore.MakeWorktreeBranch(title, prompt)
				}
				worktreeName = strings.TrimPrefix(worktreeBranch, "agent/")
				worktreeName = strings.TrimPrefix(worktreeName, "worktree/")
			}
			description := strings.TrimSpace(req.Description)
			if description == "" {
				description = routed.Mission
			}
			if description == "" && prompt != "" {
				description = prompt
			}
			stages := req.PipelineStages
			if len(stages) == 0 {
				stages = routed.Stages
			}
			deliverables := req.Deliverables
			if len(deliverables) == 0 {
				deliverables = routed.Deliverables
			}
			for i := range deliverables {
				// Client cannot supply arbitrary VideoProvenance or provider handles
				deliverables[i].VideoProvenance = nil
			}
			for i := range req.AttachedMedia {
				// Client cannot supply arbitrary SourceLink or provider handles
				req.AttachedMedia[i].SourceLink = nil
			}
			workspacesInvolved := req.WorkspacesInvolved
			if len(workspacesInvolved) == 0 && !isMediaAgent {
				workspacesInvolved = routed.WorkspacesInvolved
			}
			planSummary := strings.TrimSpace(req.PlanSummary)
			if planSummary == "" {
				planSummary = routed.PlanSummary
			}
			fullPlanMarkdown := strings.TrimSpace(req.FullPlanMarkdown)
			if fullPlanMarkdown == "" {
				fullPlanMarkdown = routed.FullPlanMarkdown
			}
			revision := req.Revision
			if revision <= 0 {
				revision = 1
			}
			workerName := strings.TrimSpace(req.WorkerName)
			if workerName == "" {
				workerName = fmt.Sprintf("@%s Worker", strings.Title(agentName))
			}

			taskStatus := strings.TrimSpace(req.Status)
			if taskStatus == "" {
				if req.AutoApprove {
					taskStatus = "in_progress"
				} else {
					taskStatus = "pending_approval"
				}
			}

			aspectRatio := strings.TrimSpace(req.AspectRatio)
			if aspectRatio == "" {
				aspectRatio = routed.AspectRatio
			}
			variantCount := req.VariantCount
			if variantCount <= 0 {
				variantCount = routed.VariantCount
			}

			task := pebblestore.ProjectTaskRecord{
				ID:                  mediaTaskID,
				SubmissionHash:      mediaSubmissionHash,
				ProjectID:           projectID,
				Title:               title,
				Description:         description,
				Status:              taskStatus,
				Agent:               agentName,
				WorkerID:            "",
				WorkerName:          workerName,
				WorkerRunID:         "",
				AutomationID:        "",
				OutcomeType:         outcomeType,
				Operation:           reqOp,
				WorkspacePath:       strings.TrimSpace(req.WorkspacePath),
				WorktreeBranch:      worktreeBranch,
				WorktreeName:        worktreeName,
				BaseBranch:          baseBranch,
				GitStatus:           "unknown",
				UnintegratedCommits: req.UnintegratedCommits,
				DiffSummary:         strings.TrimSpace(req.DiffSummary),
				IsDirty:             req.IsDirty,
				ActionNeeded:        strings.TrimSpace(req.ActionNeeded),
				WhatDidDo:           req.WhatDidDo,
				WhatNotDone:         req.WhatNotDone,
				PipelineStages:      stages,
				CurrentStageIndex:   req.CurrentStageIndex,
				Deliverables:        deliverables,
				WorkspacesInvolved:  workspacesInvolved,
				ContextPoolSummary:  routed.ContextPoolSummary,
				PlanSummary:         planSummary,
				FullPlanMarkdown:    fullPlanMarkdown,
				Tier:                tier,
				FeatureSize:         req.FeatureSize,
				Revision:            revision,
				LastError:           strings.TrimSpace(req.LastError),
				AspectRatio:         aspectRatio,
				Resolution:          strings.TrimSpace(req.Resolution),
				VariantCount:        variantCount,
				DurationSeconds:     req.DurationSeconds,
				Model:               strings.TrimSpace(req.Model),
				Provider:            strings.TrimSpace(req.Provider),
				Thinking:            strings.TrimSpace(req.Thinking),
				ServiceTier:         strings.TrimSpace(req.ServiceTier),
				ContextMode:         strings.TrimSpace(req.ContextMode),
				Scenes:              routed.Scenes,
				Soundtrack:          routed.Soundtrack,
				AutoApprove:         req.AutoApprove,
				RouterAlert:         routed.RouterAlert,
				AttachedMedia:       req.AttachedMedia,
				TaskProgram:         req.TaskProgram,
				TaskProgramID:       req.TaskProgramID,
			}
			if isDirectVideo {
				task.Agent = "video"
				task.OutcomeType = "video_clip"
				task.Operation = reqOp
				task.Model = vModel
				task.Provider = vProvider
				task.AspectRatio = normAR
				task.Resolution = normRes
				task.DurationSeconds = normDur
				task.VariantCount = vidClipCount
				task.Deliverables = routed.Deliverables
				task.AccountID = p.AccountScopeID
				task.Scenes = append([]pebblestore.ProjectTaskScene(nil), req.Scenes...)
				if len(task.Scenes) > 1 {
					task.OutcomeType = "video_story"
				}
				task.Soundtrack = ""
				task.WorktreeBranch = ""
				task.BaseBranch = ""
				task.Description = prompt
				if sourceVideo != nil && sourceVideo.SourceLink != nil {
					task.SourceDigestSHA256 = sourceVideo.SourceLink.DigestSHA256
					if len(task.AttachedMedia) > 0 {
						task.AttachedMedia[0].DigestSHA256 = sourceVideo.SourceLink.DigestSHA256
						task.AttachedMedia[0].SourceLink = sourceVideo.SourceLink
					}
				}
				if enhancePrompt && strings.TrimSpace(routed.Mission) != "" {
					task.Description = routed.Mission
				}
				routed.TaskProgram = nil
			}
			if task.TaskProgram == nil && routed.TaskProgram != nil {
				task.TaskProgram = routed.TaskProgram
			}
			if task.TaskProgram != nil {
				if task.TaskProgram.ID == "" {
					task.TaskProgram.ID = fmt.Sprintf("prog-%d", time.Now().UnixMilli())
				}
				if task.TaskProgramID == "" {
					task.TaskProgramID = task.TaskProgram.ID
				}
				if len(task.TaskProgram.Stages) == 0 && len(task.TaskProgram.Jobs) > 0 {
					stageSet := make(map[string]bool)
					for i := range task.TaskProgram.Jobs {
						stID := strings.TrimSpace(task.TaskProgram.Jobs[i].StageID)
						if stID == "" {
							stID = "stage-1"
							task.TaskProgram.Jobs[i].StageID = stID
						}
						if !stageSet[stID] {
							stageSet[stID] = true
							task.TaskProgram.Stages = append(task.TaskProgram.Stages, pebblestore.TaskProgramStageSpec{
								ID:                 stID,
								DependencyEvidence: "Synthesized stage for cohort jobs",
							})
						}
					}
				}
			}
			if task.Agent == "coder" || task.OutcomeType == "code_pr" || task.OutcomeType == "bug_patch" {
				task.AspectRatio = ""
				task.VariantCount = 0
			}
			if task.Status == "pending_approval" || task.Status == "planning" || task.Status == "queued" {
				task.UnintegratedCommits = 0
				task.BehindCommits = 0
				task.DiffSummary = ""
				task.IsDirty = false
				task.DirtyCount = 0
				task.IsIntegrated = false
				task.SyncWarning = ""
				task.ActionNeeded = ""
			}
			if len(task.AttachedMedia) == 0 && len(routed.AttachedMedia) > 0 {
				task.AttachedMedia = routed.AttachedMedia
			}

			if isDirectMediaTask(&task) {
				if err := validateProjectMediaTaskSettings(s, &task, p); err != nil {
					writeError(w, http.StatusBadRequest, err)
					return
				}
			}
			if err := task.Validate(); err != nil {
				writeError(w, http.StatusBadRequest, fmt.Errorf("invalid task definition: %w", err))
				return
			}

			// Deploy execution: Direct plan submission, planning run, small Coder task, Task Program, or media.
			structDoc := req.Document
			if structDoc == nil && req.PlanDocument != nil {
				structDoc = req.PlanDocument
			}
			if structDoc != nil {
				// Direct structured plan submission using SAME service without planning run!
				task.Status = "pending_approval"
				task.ActionNeeded = "Review plan in task card and click Approve"
				if err := db.PutProjectTask(p.AccountScopeID, &task); err != nil {
					writeError(w, http.StatusBadRequest, err)
					return
				}
				subResult, err := s.SubmitProjectTaskPlan(r.Context(), sessionruntime.ProjectTaskPlanSubmissionInput{
					AccountScopeID:  p.AccountScopeID,
					UserID:          p.UserID,
					ProjectID:       projectID,
					TaskID:          task.ID,
					Document:        structDoc,
					PlanText:        fullPlanMarkdown,
					Title:           title,
					WorkspacePath:   task.WorkspacePath,
					ParentSessionID: proj.PrimarySessionID,
				})
				if err != nil {
					writeError(w, http.StatusBadRequest, fmt.Errorf("submit structured plan: %w", err))
					return
				}
				task = subResult.Task
				hydrateTaskPlanDocument(&task, db)
			} else if task.Agent == "plan" {
				// Explicit Plan request:
				// Map to Swarm ModePlan. Start read-only planning independently of implementation approval.
				// Auto-approve must NOT approve unseen plan!
				task.Status = "planning"
				task.ActionNeeded = "Plan agent investigating and authoring structured plan..."
				task.WhatDidDo = []string{"Started planning investigation"}
				if err := s.deployProjectTaskExecution(p, proj, &task, "in_progress", prompt); err != nil {
					writeError(w, http.StatusInternalServerError, fmt.Errorf("deploy planning session: %w", err))
					return
				}
			} else if task.Agent == "coder" || task.OutcomeType == "code_pr" || task.OutcomeType == "bug_patch" {
				// Small tasks:
				// Pending small task must NOT execute until approved; deploy_session is not approval.
				task.Status = "pending_approval"
				task.ActionNeeded = "Review task and click Approve to start Coder execution"
				if err := s.deployProjectTaskExecution(p, proj, &task, "pending_approval", prompt); err != nil {
					writeError(w, http.StatusInternalServerError, fmt.Errorf("deploy coder session: %w", err))
					return
				}
			} else if task.TaskProgram != nil {
				if req.AutoApprove {
					task.Status = "in_progress"
					if err := s.deployProjectTaskProgram(p, proj, &task); err != nil {
						writeError(w, http.StatusInternalServerError, fmt.Errorf("deploy task program: %w", err))
						return
					}
					hydrateTaskProgramStatus(&task, db)
				} else {
					task.Status = "pending_approval"
					task.ActionNeeded = "Review task program and click Approve"
				}
			} else if task.Agent == "image" || task.Agent == "video" || task.Agent == "sound" || task.Agent == "audio" {
				if err := admitProjectMediaTask(&task); err != nil {
					writeError(w, http.StatusBadRequest, err)
					return
				}
				if task.AutoApprove {
					task.Status = "in_progress"
					if err := s.deployProjectTaskExecution(p, proj, &task, "in_progress", prompt); err != nil {
						writeError(w, http.StatusInternalServerError, fmt.Errorf("deploy media execution: %w", err))
						return
					}
				}
			} else {
				if req.AutoApprove {
					task.Status = "in_progress"
					if err := s.deployProjectTaskExecution(p, proj, &task, "in_progress", prompt); err != nil {
						writeError(w, http.StatusInternalServerError, fmt.Errorf("deploy task execution: %w", err))
						return
					}
				} else {
					task.Status = "pending_approval"
					task.ActionNeeded = "Review task and click Approve"
				}
			}

			if !isDirectMediaTask(&task) || task.Status == "pending_approval" {
				if err := db.PutProjectTask(p.AccountScopeID, &task); err != nil {
					writeError(w, http.StatusBadRequest, err)
					return
				}
			}

			// Add task ID to project.ActiveTaskIDs
			_, _ = db.UpdateProject(p.AccountScopeID, projectID, func(projRecord *pebblestore.ProjectRecord) error {
				for _, tid := range projRecord.ActiveTaskIDs {
					if tid == task.ID {
						return nil
					}
				}
				projRecord.ActiveTaskIDs = append(projRecord.ActiveTaskIDs, task.ID)
				return nil
			})

			writeJSON(w, http.StatusCreated, map[string]any{
				"task":          sanitizeProjectTaskForClient(&task),
				"model_preview": s.buildTaskModelPreview(p, &task),
			})
			return
		}

		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}

	// 5. Single Project Task resource: /v3/projects/{id}/tasks/{taskId}
	if len(segments) == 3 && segments[1] == "tasks" {
		taskID := segments[2]
		if r.Method == http.MethodGet {
			if !s.requireScopeAny(w, r, "projects:read", "sessions:read") {
				return
			}
			task, found, err := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			if !found || task == nil {
				writeError(w, http.StatusNotFound, errors.New("project task not found"))
				return
			}
			catalogTask := *task
			project, exists, catalogErr := db.GetProject(p.AccountScopeID, projectID)
			if catalogErr != nil || !exists || project == nil {
				catalogTask.SourceWorkspace.WorkspaceGeneration = 0
			} else if _, sourceErr := s.resolveProjectTaskSource(p, project, task.SourceWorkspace.Path, task.SourceWorkspace.WorkspaceID, task.SourceWorkspace.WorkspaceGeneration, true); sourceErr != nil {
				catalogTask.SourceWorkspace.WorkspaceGeneration = 0
			}
			if err := reconcileTaskGitStateContext(r.Context(), db, &catalogTask); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			catalogTask.SourceWorkspace = task.SourceWorkspace
			*task = catalogTask
			syncTaskSessionState(task, db)
			hydrateTaskProgramStatus(task, db)
			hydrateTaskPlanDocument(task, db)
			writeJSON(w, http.StatusOK, map[string]any{
				"task":          sanitizeProjectTaskForClient(task),
				"model_preview": s.buildTaskModelPreview(p, task),
			})
			return
		}

		if r.Method == http.MethodPatch {
			if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
				return
			}
			body, ok := readProjectMediaRequest(w, r)
			if !ok {
				return
			}
			var patch map[string]any
			if err := json.Unmarshal(body, &patch); err != nil {
				writeError(w, http.StatusBadRequest, errors.New("invalid JSON payload"))
				return
			}

			updated, err := db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
				if t.Status != "pending_approval" && t.Status != "queued" {
					return fmt.Errorf("task cannot be modified in status %q (only pending tasks can be updated)", t.Status)
				}
				if v, ok := patch["title"].(string); ok && strings.TrimSpace(v) != "" {
					t.Title = strings.TrimSpace(v)
				}
				if v, ok := patch["description"].(string); ok {
					t.Description = strings.TrimSpace(v)
				}
				if v, ok := patch["status"].(string); ok && strings.TrimSpace(v) != "" && strings.TrimSpace(v) != t.Status {
					return errors.New("task lifecycle status requires its approval or execution operation")
				}
				if v, ok := patch["agent"].(string); ok {
					t.Agent = strings.TrimSpace(v)
				}
				if _, ok := patch["worker_id"]; ok {
					return errors.New("worker_id is a server-managed receipt and cannot be modified")
				}
				if _, ok := patch["worker_run_id"]; ok {
					return errors.New("worker_run_id is a server-managed receipt and cannot be modified")
				}
				if _, ok := patch["automation_id"]; ok {
					return errors.New("automation_id is a server-managed receipt and cannot be modified")
				}
				if _, ok := patch["session_id"]; ok {
					return errors.New("session_id is server-managed and cannot be modified")
				}
				if _, ok := patch["project_id"]; ok {
					return errors.New("project_id is immutable and cannot be modified")
				}
				if v, ok := patch["worker_name"].(string); ok {
					if t.WorkerID != "" {
						return errors.New("cannot modify worker_name on worker-attributed task")
					}
					t.WorkerName = strings.TrimSpace(v)
				}
				if v, ok := patch["current_stage_index"].(float64); ok {
					t.CurrentStageIndex = int(v)
				}
				if stagesRaw, ok := patch["pipeline_stages"]; ok {
					rawBytes, err := json.Marshal(stagesRaw)
					if err == nil {
						var stages []string
						if err := json.Unmarshal(rawBytes, &stages); err == nil {
							t.PipelineStages = stages
						}
					}
				}
				if delivRaw, ok := patch["deliverables"]; ok {
					rawBytes, err := json.Marshal(delivRaw)
					if err == nil {
						var delivs []pebblestore.ProjectTaskDeliverable
						if err := json.Unmarshal(rawBytes, &delivs); err == nil {
							existingProv := make(map[string]*pebblestore.VideoProvenance)
							existingModel := make(map[string]string)
							existingAR := make(map[string]string)
							existingRes := make(map[string]string)
							existingDur := make(map[string]int)
							for _, oldDeliv := range t.Deliverables {
								if oldDeliv.VideoProvenance != nil {
									existingProv[oldDeliv.ID] = oldDeliv.VideoProvenance
								}
								if oldDeliv.Model != "" {
									existingModel[oldDeliv.ID] = oldDeliv.Model
								}
								if oldDeliv.AspectRatio != "" {
									existingAR[oldDeliv.ID] = oldDeliv.AspectRatio
								}
								if oldDeliv.Resolution != "" {
									existingRes[oldDeliv.ID] = oldDeliv.Resolution
								}
								if oldDeliv.DurationSeconds > 0 {
									existingDur[oldDeliv.ID] = oldDeliv.DurationSeconds
								}
							}
							for i := range delivs {
								if oldP, ok := existingProv[delivs[i].ID]; ok {
									delivs[i].VideoProvenance = oldP
								} else {
									delivs[i].VideoProvenance = nil
								}
								delivs[i].Model = existingModel[delivs[i].ID]
								delivs[i].AspectRatio = existingAR[delivs[i].ID]
								delivs[i].Resolution = existingRes[delivs[i].ID]
								delivs[i].DurationSeconds = existingDur[delivs[i].ID]
							}
							t.Deliverables = delivs
						}
					}
				}
				if v, ok := patch["outcome_type"].(string); ok {
					t.OutcomeType = strings.TrimSpace(v)
				}
				if v, ok := patch["workspace_path"].(string); ok {
					t.WorkspacePath = strings.TrimSpace(v)
				}
				if v, ok := patch["worktree_branch"].(string); ok {
					t.WorktreeBranch = strings.TrimSpace(v)
				}
				if v, ok := patch["worktree_name"].(string); ok {
					t.WorktreeName = strings.TrimSpace(v)
				}
				if v, ok := patch["base_branch"].(string); ok {
					t.BaseBranch = strings.TrimSpace(v)
				}
				if v, ok := patch["behind_commits"].(float64); ok {
					t.BehindCommits = int(v)
				}
				if v, ok := patch["is_integrated"].(bool); ok {
					t.IsIntegrated = v
				}
				if v, ok := patch["dirty_count"].(float64); ok {
					t.DirtyCount = int(v)
				}
				if v, ok := patch["sync_warning"].(string); ok {
					t.SyncWarning = strings.TrimSpace(v)
				}
				if v, ok := patch["unintegrated_commits"].(float64); ok {
					t.UnintegratedCommits = int(v)
				}
				if v, ok := patch["diff_summary"].(string); ok {
					t.DiffSummary = strings.TrimSpace(v)
				}
				if v, ok := patch["is_dirty"].(bool); ok {
					t.IsDirty = v
				}
				if v, ok := patch["action_needed"].(string); ok {
					t.ActionNeeded = strings.TrimSpace(v)
				}
				if arr, ok := patch["what_did_do"].([]any); ok {
					var items []string
					for _, item := range arr {
						if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
							items = append(items, strings.TrimSpace(s))
						}
					}
					t.WhatDidDo = items
				}
				if arr, ok := patch["what_not_done"].([]any); ok {
					var items []string
					for _, item := range arr {
						if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
							items = append(items, strings.TrimSpace(s))
						}
					}
					t.WhatNotDone = items
				}
				if wsRaw, ok := patch["workspaces_involved"]; ok {
					rawBytes, err := json.Marshal(wsRaw)
					if err == nil {
						var ws []string
						if err := json.Unmarshal(rawBytes, &ws); err == nil {
							t.WorkspacesInvolved = ws
						}
					}
				}
				if v, ok := patch["plan_summary"].(string); ok {
					t.PlanSummary = strings.TrimSpace(v)
				}
				if v, ok := patch["full_plan_markdown"].(string); ok {
					t.FullPlanMarkdown = strings.TrimSpace(v)
				}
				if v, ok := patch["tier"].(string); ok {
					t.Tier = strings.TrimSpace(v)
				}
				if v, ok := patch["last_error"].(string); ok {
					t.LastError = strings.TrimSpace(v)
				}
				if v, ok := patch["revision"].(float64); ok {
					t.Revision = int(v)
				}
				if tpRaw, ok := patch["task_program"]; ok {
					rawBytes, err := json.Marshal(tpRaw)
					if err == nil {
						var tp pebblestore.TaskProgramDefinition
						if err := json.Unmarshal(rawBytes, &tp); err == nil {
							t.TaskProgram = &tp
						}
					}
				}
				if v, ok := patch["task_program_id"].(string); ok {
					t.TaskProgramID = strings.TrimSpace(v)
				}
				if v, ok := patch["model"].(string); ok {
					t.Model = strings.TrimSpace(v)
				}
				if v, ok := patch["provider"].(string); ok {
					t.Provider = strings.TrimSpace(v)
				}
				if v, ok := patch["thinking"].(string); ok {
					t.Thinking = strings.TrimSpace(v)
				}
				if v, ok := patch["service_tier"].(string); ok {
					t.ServiceTier = strings.TrimSpace(v)
				}
				if v, ok := patch["context_mode"].(string); ok {
					t.ContextMode = strings.TrimSpace(v)
				}
				if v, ok := patch["feature_size"].(string); ok {
					t.FeatureSize = strings.ToLower(strings.TrimSpace(v))
					if t.FeatureSize == "big" && (t.Agent == "coder" || t.Agent == "swarm" || t.Agent == "") {
						t.Agent = "swarm"
						t.Tier = "complex"
						t.OutcomeType = "code_pr"
					} else if t.FeatureSize == "small" && t.Agent == "plan" {
						t.Agent = "coder"
						t.Tier = "direct"
						t.OutcomeType = "code_pr"
					}
				}
				return t.Validate()
			})
			if err != nil {
				if strings.Contains(err.Error(), "not found") {
					writeError(w, http.StatusNotFound, err)
					return
				}
				writeError(w, http.StatusBadRequest, err)
				return
			}

			writeJSON(w, http.StatusOK, map[string]any{
				"task": sanitizeProjectTaskForClient(updated),
			})
			return
		}

		if r.Method == http.MethodDelete {
			if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
				return
			}
			revision, err := strconv.Atoi(r.URL.Query().Get("revision"))
			if err != nil || revision <= 0 {
				writeError(w, http.StatusBadRequest, errors.New("positive task revision required"))
				return
			}
			if err := db.DeleteProjectTaskIfRevision(p.AccountScopeID, projectID, taskID, revision); err != nil {
				writeError(w, http.StatusConflict, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"status":  "deleted",
				"task_id": taskID,
			})
			return
		}

		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}

	// Archive a project task using its exact revision; no execution state is changed.
	if len(segments) == 4 && segments[1] == "tasks" && segments[3] == "archive" {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
			return
		}
		var req struct {
			Revision int `json:"revision"`
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
		if err != nil || json.Unmarshal(body, &req) != nil || req.Revision <= 0 {
			writeError(w, http.StatusBadRequest, errors.New("positive task revision required"))
			return
		}
		archived, err := db.ArchiveProjectTaskIfRevision(p.AccountScopeID, projectID, segments[2], req.Revision)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"task": sanitizeProjectTaskForClient(archived)})
		return
	}

	// 5b. Task model preview: GET /v3/projects/{id}/tasks/{taskId}/model-preview
	if len(segments) == 4 && segments[1] == "tasks" && (segments[3] == "model-preview" || segments[3] == "preview") {
		taskID := segments[2]
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if !s.requireScopeAny(w, r, "projects:read", "sessions:read") {
			return
		}
		task, found, err := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found || task == nil {
			writeError(w, http.StatusNotFound, errors.New("project task not found"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"task":          task,
			"model_preview": s.buildTaskModelPreview(p, task),
		})
		return
	}

	// 6. Integrate task commits: POST /v3/projects/{id}/tasks/{taskId}/integrate
	if len(segments) == 4 && segments[1] == "tasks" && (segments[3] == "integrate" || segments[3] == "recover-integrate") {
		taskID := segments[2]
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
			return
		}
		task, found, err := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found || task == nil {
			writeError(w, http.StatusNotFound, errors.New("task not found"))
			return
		}

		// Mutation requests must identify the exact selected session and captured lane.
		var selection struct {
			SessionID    string `json:"session_id"`
			SourceBranch string `json:"source_branch"`
			TargetBranch string `json:"target_branch"`
			Revision     int    `json:"revision"`
			AttemptID    string `json:"attempt_id"`
			SourceHead   string `json:"source_head"`
			TargetHead   string `json:"target_head"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&selection); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("select the task session and target branch before integrating: %w", err))
			return
		}
		if selection.SessionID == "" || selection.SessionID != task.SessionID || selection.SourceBranch == "" || selection.TargetBranch == "" {
			writeError(w, http.StatusConflict, errors.New("task selection changed or captured source/target branch is missing; refresh the task"))
			return
		}
		selectedSession, sessionFound, sessionErr := db.GetSession(selection.SessionID)
		if sessionErr != nil || !sessionFound || selectedSession.AccountScopeID != p.AccountScopeID || selectedSession.UserID != p.UserID || !selectedSession.WorktreeEnabled || strings.TrimSpace(selectedSession.WorktreeRootPath) == "" || strings.TrimSpace(selectedSession.WorktreeBranch) != selection.SourceBranch || strings.TrimSpace(selectedSession.WorktreeBaseBranch) != selection.TargetBranch || (task.WorktreeBranch != "" && task.WorktreeBranch != selection.SourceBranch) || (task.BaseBranch != "" && task.BaseBranch != selection.TargetBranch) {
			writeError(w, http.StatusConflict, errors.New("selected task is not bound to an owned worktree and captured target branch; refresh its lineage"))
			return
		}
		capturedPath, _ := selectedSession.Metadata["swarm_v3_source_workspace_path"].(string)
		capturedBase, _ := selectedSession.Metadata["base_commit"].(string)
		if strings.TrimSpace(capturedPath) == "" || strings.TrimSpace(capturedBase) == "" || (task.SourceWorkspace.Path != "" && task.SourceWorkspace.Path != capturedPath) || (task.BaseCommit != "" && task.BaseCommit != capturedBase) {
			writeError(w, http.StatusConflict, errors.New("captured source repository or fork commit is missing or differs from the task; refresh its lineage"))
			return
		}
		proj, projectFound, projectErr := db.GetProject(p.AccountScopeID, projectID)
		if projectErr != nil || !projectFound || proj == nil {
			writeError(w, http.StatusConflict, errors.New("project source catalog is unavailable"))
			return
		}
		if _, err := s.resolveProjectTaskSource(p, proj, task.SourceWorkspace.Path, task.SourceWorkspace.WorkspaceID, task.SourceWorkspace.WorkspaceGeneration, true); err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		gitState := inspectTaskGitStateContext(r.Context(), *task, db)
		if segments[3] == "recover-integrate" {
			s.recoverTaskDelta(w, r, p, task, selectedSession, gitState.deliveryAssessment, selection.Revision, selection.AttemptID, selection.SourceHead, selection.TargetHead)
			return
		}
		if gitState.deliveryAssessment == nil || gitState.deliveryAssessment.Freshness != "observed" || (gitState.deliveryAssessment.State != "candidate_work" && gitState.deliveryAssessment.State != "integrated") {
			writeError(w, http.StatusConflict, errors.New("delivery is not actionable for direct integration; refresh and review recovery"))
			return
		}
		receipt := &pebblestore.ProjectTaskIntegration{State: "in_progress", SessionID: selection.SessionID, SourceBranch: selection.SourceBranch, TargetBranch: selection.TargetBranch, TargetWorkspacePath: capturedPath}
		if err := pebblestore.BeginProjectTaskIntegration(db, p.AccountScopeID, task, receipt); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		defer func() {
			if receipt.State == "in_progress" {
				receipt.State = "conflict"
				if receipt.Error == "" {
					receipt.Error = "Integration did not complete. Inspect the source and target Git state before retrying."
				}
				if _, err := pebblestore.FinishProjectTaskIntegration(db, p.AccountScopeID, task, receipt); err != nil {
					log.Printf("persist terminal integration receipt failed: %v", err)
				}
			}
		}()
		// Inspect git state first
		if gitState.isDirty {
			writeError(w, http.StatusConflict, fmt.Errorf("cannot integrate: worktree has %d uncommitted modification(s); commit or discard them before integrating", gitState.dirtyCount))
			return
		}
		if gitState.unintegratedCommits == 0 {
			if gitState.isIntegrated {
				// Inspect the captured checkout, not a remote-tracking ref, before
				// claiming this task was integrated into the requested target.
				checkout, checkoutErr := s.worktrees.InspectTaskWorkspace(capturedPath)
				sourceState, sourceErr := s.worktrees.InspectTaskWorkspace(selectedSession.WorktreeRootPath)
				if checkoutErr != nil || sourceErr != nil || checkout.BranchName != selection.TargetBranch || sourceState.BranchName != selection.SourceBranch || checkout.HeadCommit != gitState.deliveryAssessment.TargetOID || sourceState.HeadCommit != gitState.deliveryAssessment.SourceOID || !checkout.Clean || !sourceState.Clean || sourceState.HeadCommit == capturedBase {
					writeError(w, http.StatusConflict, errors.New("captured target or committed source is unavailable; inspect Git before retrying"))
					return
				}
				receipt.SourceHead = sourceState.HeadCommit
				receipt.PreviousTargetHead = checkout.HeadCommit
				ancestorCtx, ancestorCancel := context.WithTimeout(r.Context(), 3*time.Second)
				defer ancestorCancel()
				if err := exec.CommandContext(ancestorCtx, "git", "-C", capturedPath, "merge-base", "--is-ancestor", sourceState.HeadCommit, checkout.HeadCommit).Run(); err != nil {
					receipt.Error = "Integration ancestry changed during verification; inspect the retained source and target before launching repair."
					writeError(w, http.StatusConflict, errors.New(receipt.Error))
					return
				}
				receipt.SourceHead = sourceState.HeadCommit
				receipt.ResultingTargetHead = checkout.HeadCommit
				receipt.State = "already_integrated"
				updated, updateErr := pebblestore.FinishProjectTaskIntegration(db, p.AccountScopeID, task, receipt)
				if updateErr != nil {
					writeError(w, http.StatusInternalServerError, fmt.Errorf("Git integration verified at %s but task receipt persistence failed; reconcile task state without rerunning Git: %w", receipt.ResultingTargetHead, updateErr))
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{
					"status": "already_integrated",
					"task":   sanitizeProjectTaskForClient(updated),
				})
				return
			}
			writeError(w, http.StatusBadRequest, errors.New("cannot integrate: worktree has no commits to integrate"))
			return
		}

		// Resolve worktree target path
		targetPath := strings.TrimSpace(task.WorkspacePath)
		var sess *pebblestore.SessionSnapshot
		if task.SessionID != "" {
			if sSnap, sFound, _ := db.GetSession(task.SessionID); sFound {
				sess = &sSnap
				if sess.WorktreeEnabled && strings.TrimSpace(sess.WorktreeRootPath) != "" {
					targetPath = strings.TrimSpace(sess.WorktreeRootPath)
				}
			}
		}
		parentWs := ""
		if sess != nil && sess.Metadata != nil {
			if src, ok := sess.Metadata["swarm_v3_source_workspace_path"].(string); ok && src != "" {
				parentWs = src
			}
		}
		if parentWs != capturedPath || targetPath != selectedSession.WorktreeRootPath || parentWs == targetPath {
			writeError(w, http.StatusConflict, errors.New("task worktree does not match the captured repository and session lane"))
			return
		}

		type workspaceInspector interface {
			InspectTaskWorkspace(workspacePath string) (worktreeruntime.TaskWorkspaceState, error)
		}
		var inspector workspaceInspector = &worktreeruntime.Service{}
		if s.worktrees != nil {
			inspector = s.worktrees
		}
		parentState, err := inspector.InspectTaskWorkspace(parentWs)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("inspect parent repository %q: %w", parentWs, err))
			return
		}
		if !parentState.Clean {
			writeError(w, http.StatusConflict, fmt.Errorf("captured repository is dirty on %s; finish those changes before integrating", parentState.BranchName))
			return
		}
		if parentState.BranchName != selection.TargetBranch {
			writeError(w, http.StatusConflict, fmt.Errorf("captured checkout is on %s, not selected target %s; check out the target and retry", parentState.BranchName, selection.TargetBranch))
			return
		}

		childState, err := inspector.InspectTaskWorkspace(targetPath)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("inspect worktree %q: %w", targetPath, err))
			return
		}
		if childState.HeadCommit != gitState.deliveryAssessment.SourceOID || parentState.HeadCommit != gitState.deliveryAssessment.TargetOID {
			writeError(w, http.StatusConflict, errors.New("delivery heads moved; refresh before integration"))
			return
		}
		if !childState.Clean {
			writeError(w, http.StatusConflict, errors.New("source worktree is dirty; explicitly commit changes before integrating"))
			return
		}
		if childState.BranchName != selection.SourceBranch {
			writeError(w, http.StatusConflict, errors.New("source worktree branch changed; refresh the selected task"))
			return
		}

		baseCommit := capturedBase
		if baseCommit == childState.HeadCommit {
			writeError(w, http.StatusBadRequest, errors.New("cannot integrate: worktree has no commits beyond its base commit"))
			return
		}

		type taskIntegrator interface {
			PrepareTaskIntegration(workspacePath, targetBranch, targetHead string, children []worktreeruntime.TaskIntegrationChild) (worktreeruntime.TaskIntegrationPlan, error)
			ApplyTaskIntegration(workspacePath string, plan worktreeruntime.TaskIntegrationPlan) (worktreeruntime.TaskIntegrationResult, error)
		}
		var integrator taskIntegrator = &worktreeruntime.Service{}
		if ti, ok := s.worktrees.(taskIntegrator); ok {
			integrator = ti
		}
		// Persist inspected provenance before preparation: a merge conflict (or a
		// restart during preparation) must retain the exact committed repair base.
		receipt.SourceHead = childState.HeadCommit
		receipt.PreviousTargetHead = parentState.HeadCommit
		if _, err := db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
			if err := pebblestore.CheckProjectTaskIntegration(t, receipt); err != nil {
				return err
			}
			copy := *receipt
			t.Integration = &copy
			t.Revision++
			return nil
		}); err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		plan, err := integrator.PrepareTaskIntegration(parentWs, parentState.BranchName, parentState.HeadCommit, []worktreeruntime.TaskIntegrationChild{
			{
				SessionID:        selection.SessionID,
				BaseCommit:       baseCommit,
				HeadCommit:       childState.HeadCommit,
				PreserveAncestry: true,
			},
		})
		if err != nil {
			receipt.Error = fmt.Sprintf("prepare integration failed: %v", err)
			writeError(w, http.StatusBadRequest, errors.New(receipt.Error))
			return
		}

		// Patch equivalence can eliminate every replay candidate without landing
		// the original history. Do not apply a no-op and then claim integration;
		// retain the inspected receipt so repair can merge in an isolated lane.
		if len(plan.Commits) == 0 && plan.FastForwardHead == "" {
			receipt.Error = "Integration requires original commit ancestry: equivalent patches exist on the captured target, but the source history is unmerged. Launch repair to reconcile history in a new isolated worktree, then explicitly integrate."
			writeError(w, http.StatusConflict, errors.New(receipt.Error))
			return
		}

		result, err := integrator.ApplyTaskIntegration(parentWs, plan)
		if err != nil {
			receipt.Error = fmt.Sprintf("apply integration failed: %v", err)
			writeError(w, http.StatusInternalServerError, errors.New(receipt.Error))
			return
		}

		receipt.ResultingTargetHead = result.ResultingParentHead
		// The integration service reports an applied operation, but the receipt is
		// authoritative only after verifying the actual checked-out target ancestry.
		verified, verifyErr := inspector.InspectTaskWorkspace(parentWs)
		if verifyErr != nil || verified.BranchName != selection.TargetBranch || verified.HeadCommit != result.ResultingParentHead {
			receipt.Error = "target changed after promotion; inspect Git before retrying"
			writeError(w, http.StatusConflict, errors.New(receipt.Error))
			return
		}
		verifyCtx, verifyCancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer verifyCancel()
		if err := exec.CommandContext(verifyCtx, "git", "-C", parentWs, "merge-base", "--is-ancestor", childState.HeadCommit, verified.HeadCommit).Run(); err != nil {
			receipt.Error = "source commit ancestry on target could not be verified; inspect Git before retrying"
			writeError(w, http.StatusConflict, errors.New(receipt.Error))
			return
		}
		receipt.State = "integrated"
		updated, err := pebblestore.FinishProjectTaskIntegration(db, p.AccountScopeID, task, receipt)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("Git integration verified at %s but task receipt persistence failed; reconcile task state without rerunning Git: %w", receipt.ResultingTargetHead, err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "integrated",
			"task":   sanitizeProjectTaskForClient(updated),
			"integration": map[string]any{
				"target_branch":         parentState.BranchName,
				"previous_target_head":  parentState.HeadCommit,
				"resulting_target_head": result.ResultingParentHead,
			},
		})
		return
	}

	// 7. Approve task session: POST /v3/projects/{id}/tasks/{taskId}/approve (or /accept)
	if len(segments) == 4 && segments[1] == "tasks" && (segments[3] == "approve" || segments[3] == "accept") {
		taskID := segments[2]
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
			return
		}
		if segments[3] == "accept" {
			current, found, err := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
			if err != nil || !found || current == nil {
				writeError(w, http.StatusNotFound, errors.New("task not found"))
				return
			}
			if current.Agent == "video" || current.Agent == "image" {
				updated, err := db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
					if t.Status == "completed" {
						return nil
					}
					if t.Status != "needs_review" || len(t.Deliverables) == 0 {
						return errors.New("media task is not ready for acceptance")
					}
					for _, d := range t.Deliverables {
						if (d.Status != "ready" && d.Status != "accepted") || d.MediaURL == "" {
							return errors.New("all media deliverables must be ready before acceptance")
						}
					}
					for i := range t.Deliverables {
						t.Deliverables[i].Status = "accepted"
					}
					t.Status = "completed"
					t.ActionNeeded = ""
					return nil
				})
				if err != nil {
					writeError(w, http.StatusConflict, err)
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"status": "accepted", "task": sanitizeProjectTaskForClient(updated)})
				return
			}
		}
		var guards tool.ProjectTaskApprovalGuards
		if !readProjectTaskJSON(w, r, &guards) {
			return
		}
		existingTask, found, _ := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
		wasAlreadyApproved := false
		if found && existingTask != nil && (existingTask.Status == "in_progress" || existingTask.Status == "completed") {
			wasAlreadyApproved = true
		}
		updated, err := s.ApproveProjectTask(r.Context(), p, projectID, taskID, guards)
		if err != nil {
			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "forbidden") {
				status = http.StatusForbidden
			} else if strings.Contains(err.Error(), "not found") {
				status = http.StatusNotFound
			}
			writeError(w, status, err)
			return
		}
		respStatus := "approved"
		if wasAlreadyApproved {
			respStatus = "already_approved"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": respStatus,
			"task":   sanitizeProjectTaskForClient(updated),
		})
		return
	}

	// 7c. Reject task: POST /v3/projects/{id}/tasks/{taskId}/reject
	if len(segments) == 4 && segments[1] == "tasks" && segments[3] == "reject" {
		taskID := segments[2]
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
			return
		}
		var guards tool.ProjectTaskApprovalGuards
		if !readProjectTaskJSON(w, r, &guards) {
			return
		}
		existingTask, found, err := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found || existingTask == nil {
			writeError(w, http.StatusNotFound, errors.New("task not found"))
			return
		}
		if existingTask.AccountID != "" && existingTask.AccountID != p.AccountScopeID {
			writeError(w, http.StatusForbidden, errors.New("cross-account task rejection forbidden"))
			return
		}
		if existingTask.ProjectID != "" && existingTask.ProjectID != projectID {
			writeError(w, http.StatusForbidden, errors.New("cross-project task rejection forbidden"))
			return
		}
		if guards.SessionID != "" && existingTask.SessionID != "" && guards.SessionID != existingTask.SessionID {
			writeError(w, http.StatusBadRequest, fmt.Errorf("session ID mismatch: expected %q, got %q", existingTask.SessionID, guards.SessionID))
			return
		}
		if guards.PlanID != "" && (existingTask.PlanBinding == nil || guards.PlanID != existingTask.PlanBinding.PlanID) {
			writeError(w, http.StatusBadRequest, fmt.Errorf("plan ID mismatch: expected %q, got %q", existingTask.PlanBinding.PlanID, guards.PlanID))
			return
		}
		if guards.DefinitionRevision > 0 {
			if existingTask.PlanBinding != nil && existingTask.PlanBinding.DefinitionRevision > 0 && guards.DefinitionRevision != existingTask.PlanBinding.DefinitionRevision {
				writeError(w, http.StatusBadRequest, fmt.Errorf("plan definition is stale (guarded revision %d, current %d)", guards.DefinitionRevision, existingTask.PlanBinding.DefinitionRevision))
				return
			}
			if (existingTask.PlanBinding == nil || existingTask.PlanBinding.DefinitionRevision <= 0) && existingTask.Revision > 0 && guards.DefinitionRevision != existingTask.Revision {
				writeError(w, http.StatusBadRequest, fmt.Errorf("task revision is stale (guarded revision %d, current %d)", guards.DefinitionRevision, existingTask.Revision))
				return
			}
		}

		var boundPlan pebblestore.SessionPlanSnapshot
		var planCopy pebblestore.SessionPlanSnapshot
		hasBoundPlan := existingTask.PlanBinding != nil && existingTask.PlanBinding.PlanID != "" && existingTask.SessionID != ""
		if hasBoundPlan {
			if existingTask.PlanBinding.SessionID != "" && existingTask.SessionID != "" && existingTask.PlanBinding.SessionID != existingTask.SessionID {
				writeError(w, http.StatusConflict, errors.New("cross-session plan binding forbidden"))
				return
			}
			pSnap, ok, err := db.GetPlan(existingTask.SessionID, existingTask.PlanBinding.PlanID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			if !ok || pSnap.ID == "" {
				writeError(w, http.StatusConflict, errors.New("bound plan unavailable"))
				return
			}
			if pSnap.AccountScopeID != p.AccountScopeID {
				writeError(w, http.StatusForbidden, errors.New("cross-account plan rejection forbidden"))
				return
			}
			if pSnap.SessionID != existingTask.SessionID {
				writeError(w, http.StatusForbidden, errors.New("cross-session plan rejection forbidden"))
				return
			}
			if pSnap.ApprovalState != "approved" && existingTask.PlanBinding.DefinitionRevision > 0 && pSnap.Version != existingTask.PlanBinding.DefinitionRevision {
				writeError(w, http.StatusBadRequest, fmt.Errorf("plan definition is stale (task revision %d, current %d)", existingTask.PlanBinding.DefinitionRevision, pSnap.Version))
				return
			}
			if pSnap.ApprovalState == "approved" && existingTask.PlanBinding.Receipt != "" && pSnap.AcceptedDefinitionReceipt != "" && pSnap.AcceptedDefinitionReceipt != existingTask.PlanBinding.Receipt {
				writeError(w, http.StatusBadRequest, fmt.Errorf("plan definition receipt mismatch: expected %q, got %q", existingTask.PlanBinding.Receipt, pSnap.AcceptedDefinitionReceipt))
				return
			}
			boundPlan = pSnap
			if boundPlan.Version <= 0 {
				boundPlan.Version = 1
			}

			// Reject plan definition via canonical mutation.
			planCopy = boundPlan
			planCopy.Status, planCopy.ApprovalState = "rejected", "rejected"
			planCopy.ParentRevision = boundPlan.Version
			planCopy.Version = boundPlan.Version + 1
			planCopy.UpdatedAt = time.Now().UnixMilli()
			archived := boundPlan
			archived.Active = false
			key := fmt.Sprintf("project-task:reject:%s:%d", taskID, existingTask.PlanBinding.DefinitionRevision)
			if _, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
				SessionID: existingTask.SessionID, UserID: p.UserID, AccountScopeID: p.AccountScopeID,
				ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key,
				Kind: sessionruntime.SessionMutationSavePlan, PlanSave: &pebblestore.V3PlanSaveMutation{
					Plan:                  planCopy,
					ArchivedRevision:      &archived,
					ExpectedParentVersion: boundPlan.Version,
				},
			}); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
		}

		updated, err := db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
			t.Status = "rejected"
			t.ActionNeeded = "Task rejected by user"
			t.WhatDidDo = append(t.WhatDidDo, "Task rejected by user")
			return nil
		})
		if err != nil {
			// Honest reconciliation boundary: session mutation and task update are not a single atomic store transaction.
			// If updating the project task fails after the session plan was marked rejected, attempt to reconcile
			// the session plan back to its prior state to minimize split-brain inconsistency.
			if hasBoundPlan {
				currPlan, currFound, getErr := db.GetPlan(existingTask.SessionID, existingTask.PlanBinding.PlanID)
				if getErr != nil || !currFound {
					writeError(w, http.StatusInternalServerError, fmt.Errorf("task update failed: %w; nonrecoverable plan rollback failed: bound plan lookup failed: %v", err, getErr))
					return
				}
				// Verify canonical version and receipt: do not overwrite concurrent changes
				if currPlan.Version != planCopy.Version ||
					(existingTask.PlanBinding.Receipt != "" && currPlan.AcceptedDefinitionReceipt != existingTask.PlanBinding.Receipt) ||
					currPlan.Status != "rejected" {
					writeError(w, http.StatusInternalServerError, fmt.Errorf("task update failed: %w; nonrecoverable plan rollback: plan compensation skipped due to concurrent modification", err))
					return
				}
				reconciledPlan := boundPlan
				reconciledPlan.Version = currPlan.Version + 1
				reconciledPlan.ParentRevision = currPlan.Version
				reconciledPlan.UpdatedAt = time.Now().UnixMilli()
				currArchived := currPlan
				currArchived.Active = false
				restoreKey := fmt.Sprintf("project-task:reject-reconcile:%s:%d:%d", taskID, existingTask.PlanBinding.DefinitionRevision, time.Now().UnixNano())
				_, restoreErr := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
					SessionID: existingTask.SessionID, UserID: p.UserID, AccountScopeID: p.AccountScopeID,
					ClientRequestID: restoreKey, IdempotencyKey: restoreKey, PayloadHash: restoreKey, RequestHash: restoreKey,
					Kind: sessionruntime.SessionMutationSavePlan, PlanSave: &pebblestore.V3PlanSaveMutation{
						Plan:                  reconciledPlan,
						ArchivedRevision:      &currArchived,
						ExpectedParentVersion: currPlan.Version,
					},
				})
				if restoreErr != nil {
					writeError(w, http.StatusInternalServerError, fmt.Errorf("task update failed: %w; nonrecoverable plan rollback failed: %v", err, restoreErr))
					return
				}
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "rejected",
			"task":   updated,
		})
		return
	}

	// 7b. Reopen task session: POST /v3/projects/{id}/tasks/{taskId}/reopen
	if len(segments) == 4 && segments[1] == "tasks" && (segments[3] == "reopen" || segments[3] == "history") {
		s.handleProjectTaskFollowup(w, r, p, projectID, segments[2])
		return
	}
	// 7c. Complete task: POST /v3/projects/{id}/tasks/{taskId}/complete
	if len(segments) == 4 && segments[1] == "tasks" && segments[3] == "complete" {
		taskID := segments[2]
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
			return
		}
		var guards tool.ProjectTaskApprovalGuards
		if !readProjectTaskJSON(w, r, &guards) {
			return
		}
		existing, found, err := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
		if err != nil || !found || existing == nil {
			writeError(w, http.StatusNotFound, errors.New("task not found"))
			return
		}
		if existing.AccountID != "" && existing.AccountID != p.AccountScopeID {
			writeError(w, http.StatusForbidden, errors.New("cross-account task completion forbidden"))
			return
		}
		if existing.ProjectID != "" && existing.ProjectID != projectID {
			writeError(w, http.StatusForbidden, errors.New("cross-project task completion forbidden"))
			return
		}
		if guards.SessionID != "" && existing.SessionID != "" && guards.SessionID != existing.SessionID {
			writeError(w, http.StatusBadRequest, fmt.Errorf("session ID mismatch: expected %q, got %q", existing.SessionID, guards.SessionID))
			return
		}
		if guards.PlanID != "" && (existing.PlanBinding == nil || guards.PlanID != existing.PlanBinding.PlanID) {
			writeError(w, http.StatusBadRequest, errors.New("plan ID mismatch"))
			return
		}
		if guards.DefinitionRevision > 0 {
			if existing.PlanBinding != nil && existing.PlanBinding.DefinitionRevision > 0 && guards.DefinitionRevision != existing.PlanBinding.DefinitionRevision {
				writeError(w, http.StatusBadRequest, fmt.Errorf("plan definition is stale (guarded revision %d, current %d)", guards.DefinitionRevision, existing.PlanBinding.DefinitionRevision))
				return
			}
			if existing.Revision > 0 && guards.DefinitionRevision != existing.Revision {
				writeError(w, http.StatusBadRequest, fmt.Errorf("task revision is stale (guarded revision %d, current %d)", guards.DefinitionRevision, existing.Revision))
				return
			}
		}
		updated, err := db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
			t.Status = "completed"
			t.ActionNeeded = "Task marked completed by user"
			t.WhatDidDo = append(t.WhatDidDo, "Accepted and marked completed")
			return nil
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "completed",
			"task":   sanitizeProjectTaskForClient(updated),
		})
		return
	}

	// 8. Refine task plan / error re-plan: POST /v3/projects/{id}/tasks/{taskId}/refine
	if len(segments) == 4 && segments[1] == "tasks" && segments[3] == "refine" {
		taskID := segments[2]
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
			return
		}
		type refineReq struct {
			Feedback           string `json:"feedback,omitempty"`
			ErrorSummary       string `json:"error_summary,omitempty"`
			Agent              string `json:"agent,omitempty"`
			FeatureSize        string `json:"feature_size,omitempty"`
			OutcomeType        string `json:"outcome_type,omitempty"`
			Tier               string `json:"tier,omitempty"`
			Model              string `json:"model,omitempty"`
			DefinitionRevision int    `json:"definition_revision,omitempty"`
			ExpectedRevision   int    `json:"expected_revision,omitempty"`
			SessionID          string `json:"session_id,omitempty"`
			PlanID             string `json:"plan_id,omitempty"`
		}
		var rReq refineReq
		if !readProjectTaskJSON(w, r, &rReq) {
			return
		}

		proj, found, err := db.GetProject(p.AccountScopeID, projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found || proj == nil {
			writeError(w, http.StatusNotFound, errors.New("project not found"))
			return
		}

		existingTask, found, err := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found || existingTask == nil {
			writeError(w, http.StatusNotFound, errors.New("task not found"))
			return
		}
		if existingTask.AccountID != "" && existingTask.AccountID != p.AccountScopeID {
			writeError(w, http.StatusForbidden, errors.New("cross-account task refinement forbidden"))
			return
		}
		if existingTask.ProjectID != "" && existingTask.ProjectID != projectID {
			writeError(w, http.StatusForbidden, errors.New("cross-project task refinement forbidden"))
			return
		}
		if rReq.SessionID != "" && existingTask.SessionID != "" && rReq.SessionID != existingTask.SessionID {
			writeError(w, http.StatusBadRequest, fmt.Errorf("session ID mismatch: expected %q, got %q", existingTask.SessionID, rReq.SessionID))
			return
		}
		if rReq.PlanID != "" && (existingTask.PlanBinding == nil || rReq.PlanID != existingTask.PlanBinding.PlanID) {
			writeError(w, http.StatusBadRequest, fmt.Errorf("plan ID mismatch: expected %q, got %q", existingTask.PlanBinding.PlanID, rReq.PlanID))
			return
		}
		if existingTask.PlanBinding != nil {
			updated, err := s.refineBoundProjectTask(p, existingTask, tool.ProjectTaskApprovalGuards{SessionID: rReq.SessionID, PlanID: rReq.PlanID, DefinitionRevision: rReq.DefinitionRevision}, rReq.Feedback)
			if err != nil {
				writeError(w, http.StatusConflict, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": "planning", "task": updated})
			return
		}
		expectedRev := rReq.DefinitionRevision
		if expectedRev <= 0 {
			expectedRev = rReq.ExpectedRevision
		}
		if expectedRev > 0 {
			if existingTask.PlanBinding != nil && existingTask.PlanBinding.DefinitionRevision > 0 && existingTask.PlanBinding.DefinitionRevision != expectedRev {
				writeError(w, http.StatusBadRequest, fmt.Errorf("plan definition is stale (guarded revision %d, current %d)", expectedRev, existingTask.PlanBinding.DefinitionRevision))
				return
			}
			if existingTask.Revision > 0 && existingTask.Revision != expectedRev {
				writeError(w, http.StatusBadRequest, fmt.Errorf("task revision is stale (guarded revision %d, current %d)", expectedRev, existingTask.Revision))
				return
			}
		}

		fb := strings.TrimSpace(rReq.Feedback)
		errSum := strings.TrimSpace(rReq.ErrorSummary)

		updated, err := db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
			if t.Revision <= 0 {
				t.Revision = 1
			}
			t.Revision++

			if fb != "" {
				t.FeedbackHistory = append(t.FeedbackHistory, fb)
			}
			if errSum != "" {
				t.LastError = errSum
			} else if fb != "" && t.LastError != "" {
				t.LastError = ""
			}

			if rReq.Agent != "" {
				t.Agent = strings.TrimSpace(rReq.Agent)
			}
			if rReq.FeatureSize != "" {
				t.FeatureSize = strings.ToLower(strings.TrimSpace(rReq.FeatureSize))
				if t.FeatureSize == "big" && (t.Agent == "coder" || t.Agent == "swarm" || t.Agent == "") {
					t.Agent = "swarm"
					t.Tier = "complex"
					t.OutcomeType = "code_pr"
				} else if t.FeatureSize == "small" && t.Agent == "plan" {
					t.Agent = "coder"
					t.Tier = "direct"
					t.OutcomeType = "code_pr"
				}
			}
			if rReq.OutcomeType != "" {
				t.OutcomeType = strings.TrimSpace(rReq.OutcomeType)
			}
			if rReq.Tier != "" {
				t.Tier = strings.TrimSpace(rReq.Tier)
			}
			if rReq.Model != "" {
				t.Model = strings.TrimSpace(rReq.Model)
			}

			seedPrompt := t.Description
			if seedPrompt == "" {
				seedPrompt = t.Title
			}
			taskRouter := taskrouter.NewService(func(ctx context.Context, instructions, input string) (string, error) {
				res, err := s.invokeConfiguredRouterOnce(ctx, p, instructions, input, 64<<10)
				if err != nil {
					return "", err
				}
				return res.Text, nil
			})
			routed, rErr := taskRouter.RefineTask(r.Context(), taskrouter.TaskRouteOptions{
				Prompt:             seedPrompt,
				RequestedWorkspace: t.WorkspacePath,
				Agent:              t.Agent,
				FeatureSize:        t.FeatureSize,
				OutcomeType:        t.OutcomeType,
				Tier:               t.Tier,
				AspectRatio:        t.AspectRatio,
				VariantCount:       t.VariantCount,
				ScenesCount:        len(t.Scenes),
				Soundtrack:         t.Soundtrack,
				Feedback:           fb,
				LastError:          t.LastError,
				Project:            proj,
			})
			if rErr != nil {
				return fmt.Errorf("refine task router: %w", rErr)
			}

			t.PipelineStages = routed.Stages
			t.Deliverables = routed.Deliverables
			t.WorkspacesInvolved = routed.WorkspacesInvolved
			t.ContextPoolSummary = routed.ContextPoolSummary
			t.PlanSummary = routed.PlanSummary
			t.FullPlanMarkdown = routed.FullPlanMarkdown
			t.Status = "pending_approval"
			t.ActionNeeded = fmt.Sprintf("Review revised plan (Rev %d) and click Approve", t.Revision)
			if fb != "" {
				t.WhatDidDo = append(t.WhatDidDo, fmt.Sprintf("Revised plan (Rev %d) based on: %s", t.Revision, truncateString(fb, 50)))
			} else if t.LastError != "" {
				t.WhatDidDo = append(t.WhatDidDo, fmt.Sprintf("Re-planned error recovery strategy (Rev %d)", t.Revision))
			}
			return t.Validate()
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if updated != nil && updated.SessionID != "" {
			refineMsg := fmt.Sprintf("## Plan Refined (Revision %d)\n\n**User Directives:** %s\n\n**Context Pool:** %s\n\n**Updated Plan:**\n%s", updated.Revision, fb, updated.ContextPoolSummary, updated.FullPlanMarkdown)
			_, _, _, _ = s.sessions.AppendMessage(updated.SessionID, "user", refineMsg, map[string]any{
				"role":                 "project_plan_refine",
				"revision":             updated.Revision,
				"context_pool_summary": updated.ContextPoolSummary,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "refined",
			"task":   sanitizeProjectTaskForClient(updated),
		})
		return
	}

	// 9. Deploy task program: POST /v3/projects/{id}/tasks/{taskId}/program:deploy
	if len(segments) == 4 && segments[1] == "tasks" && (segments[3] == "program:deploy" || segments[3] == "deploy-program") {
		taskID := segments[2]
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
			return
		}
		task, found, err := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found || task == nil {
			writeError(w, http.StatusNotFound, errors.New("project task not found"))
			return
		}
		if task.TaskProgram == nil {
			writeError(w, http.StatusBadRequest, errors.New("task has no task program definition"))
			return
		}
		proj, _, _ := db.GetProject(p.AccountScopeID, projectID)
		if err := s.deployProjectTaskProgram(p, proj, task); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "deployed",
			"task":   sanitizeProjectTaskForClient(task),
		})
		return
	}

	// 10. Redeploy task program job: POST /v3/projects/{id}/tasks/{taskId}/program:redeploy-job
	if len(segments) == 4 && segments[1] == "tasks" && (segments[3] == "program:redeploy-job" || segments[3] == "redeploy-job") {
		taskID := segments[2]
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
			return
		}
		var req struct {
			JobID    string `json:"job_id"`
			Feedback string `json:"feedback,omitempty"`
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("cannot read request body"))
			return
		}
		if len(bytes.TrimSpace(body)) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				writeError(w, http.StatusBadRequest, fmt.Errorf("malformed JSON body: %w", err))
				return
			}
		}
		jobID := strings.TrimSpace(req.JobID)
		if jobID == "" {
			writeError(w, http.StatusBadRequest, errors.New("job_id is required"))
			return
		}
		if err := s.redeployTaskProgramJob(p, projectID, taskID, jobID, req.Feedback); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		updated, _, _ := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
		hydrateTaskProgramStatus(updated, db)
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "redeploying",
			"task":   sanitizeProjectTaskForClient(updated),
		})
		return
	}

	// 11. Inspect task program record: GET /v3/projects/{id}/tasks/{taskId}/program
	if len(segments) == 4 && segments[1] == "tasks" && segments[3] == "program" {
		taskID := segments[2]
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if !s.requireScopeAny(w, r, "projects:read", "sessions:read") {
			return
		}
		task, found, err := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found || task == nil {
			writeError(w, http.StatusNotFound, errors.New("project task not found"))
			return
		}
		if task.TaskProgramID == "" || task.SessionID == "" {
			writeError(w, http.StatusNotFound, errors.New("task has no deployed task program"))
			return
		}
		prog, ok, err := db.GetTaskProgram(task.SessionID, task.TaskProgramID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, errors.New("task program not found"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"program": prog,
		})
		return
	}

	writeError(w, http.StatusNotFound, errors.New("not found"))
}

func sanitizeProjectTaskForClient(t *pebblestore.ProjectTaskRecord) *pebblestore.ProjectTaskRecord {
	if t == nil {
		return nil
	}
	cp := *t
	cp.Attempts = append([]pebblestore.ProjectTaskAttempt(nil), t.Attempts...)
	// Board responses carry bounded references; full chronological requests use /history.
	if len(cp.Attempts) > 10 {
		cp.Attempts = cp.Attempts[len(cp.Attempts)-10:]
	}
	for i := range cp.Attempts {
		cp.Attempts[i].Deliverables = append([]pebblestore.ProjectTaskDeliverable(nil), cp.Attempts[i].Deliverables...)
		for j := range cp.Attempts[i].Deliverables {
			cp.Attempts[i].Deliverables[j].VideoProvenance = cp.Attempts[i].Deliverables[j].VideoProvenance.ClientSafeCopy()
		}
	}
	if cp.VideoProvenance != nil {
		cp.VideoProvenance = cp.VideoProvenance.ClientSafeCopy()
	}
	if len(cp.Deliverables) > 0 {
		dels := make([]pebblestore.ProjectTaskDeliverable, len(cp.Deliverables))
		for i, d := range cp.Deliverables {
			dels[i] = projectDeliverableForClient(t, d)
			if dels[i].VideoProvenance != nil {
				dels[i].VideoProvenance = dels[i].VideoProvenance.ClientSafeCopy()
				if dels[i].VideoProvenance.Model != "" {
					if dels[i].VideoProvenance.Provider != "" {
						dels[i].Model = videoExecutionIdentity(dels[i].VideoProvenance.Provider, dels[i].VideoProvenance.Model)
					} else {
						dels[i].Model = dels[i].VideoProvenance.Model
					}
				}
				if dels[i].VideoProvenance.AspectRatio != "" {
					dels[i].AspectRatio = dels[i].VideoProvenance.AspectRatio
				}
				if dels[i].VideoProvenance.Resolution != "" {
					dels[i].Resolution = dels[i].VideoProvenance.Resolution
				}
				if dels[i].VideoProvenance.DurationSeconds > 0 {
					dels[i].DurationSeconds = dels[i].VideoProvenance.DurationSeconds
				}
			}
		}
		cp.Deliverables = dels
	}
	if len(cp.AttachedMedia) > 0 {
		att := make([]pebblestore.ProjectTaskMediaRef, len(cp.AttachedMedia))
		for i, m := range cp.AttachedMedia {
			att[i] = m
			if m.SourceLink != nil {
				att[i].SourceLink = m.SourceLink.Clone()
			}
		}
		cp.AttachedMedia = att
	}
	if len(cp.Scenes) > 0 {
		cp.Scenes = append([]pebblestore.ProjectTaskScene(nil), cp.Scenes...)
	}
	if len(cp.WhatDidDo) > 0 {
		cp.WhatDidDo = append([]string(nil), cp.WhatDidDo...)
	}
	if len(cp.WhatNotDone) > 0 {
		cp.WhatNotDone = append([]string(nil), cp.WhatNotDone...)
	}
	if len(cp.PipelineStages) > 0 {
		cp.PipelineStages = append([]string(nil), cp.PipelineStages...)
	}
	if len(cp.ContextSources) > 0 {
		cp.ContextSources = append([]pebblestore.ProjectTaskSource(nil), cp.ContextSources...)
	}
	if len(cp.WorkspacesInvolved) > 0 {
		cp.WorkspacesInvolved = append([]string(nil), cp.WorkspacesInvolved...)
	}
	if len(cp.FeedbackHistory) > 0 {
		cp.FeedbackHistory = append([]string(nil), cp.FeedbackHistory...)
	}
	return &cp
}

const maxProjectTaskActionBodyBytes = 1024 * 1024

func readProjectTaskJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	if r.Body == nil {
		return true
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxProjectTaskActionBodyBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("cannot read request body"))
		return false
	}
	if len(body) > maxProjectTaskActionBodyBytes {
		writeError(w, http.StatusBadRequest, errors.New("request body exceeds maximum allowed size (oversized)"))
		return false
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return true
	}
	if bytes.Equal(trimmed, []byte("null")) {
		writeError(w, http.StatusBadRequest, errors.New("malformed JSON body: null value not allowed"))
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	if err := dec.Decode(out); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("malformed JSON body: %w", err))
		return false
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		writeError(w, http.StatusBadRequest, errors.New("malformed JSON body: trailing data after JSON object"))
		return false
	}
	return true
}
