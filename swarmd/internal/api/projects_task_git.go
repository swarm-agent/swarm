package api

import (
	"context"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/worktree"
)

// taskDeliveredState reads the durable delivery receipt, not current local Git
// readiness. A PR without its URL is not a completed delivery.
func taskDeliveredState(state, prURL string) string {
	switch state {
	case "pull_request":
		if strings.TrimSpace(prURL) != "" {
			return state
		}
	case "merged", "integrated", "already_integrated":
		return state
	}
	return ""
}

func inspectTaskGitState(task pebblestore.ProjectTaskRecord, db *pebblestore.SessionStore) taskGitState {
	return inspectTaskGitStateContext(context.Background(), task, db)
}

func inspectTaskGitStateContext(ctx context.Context, task pebblestore.ProjectTaskRecord, db *pebblestore.SessionStore) taskGitState {
	a := &pebblestore.TaskDeliveryAssessment{
		AccountID: task.AccountID, TaskID: task.ID, TaskRevision: task.Revision,
		AttemptID: task.ActiveAttemptID, SessionID: task.SessionID,
		WorkspaceID: task.SourceWorkspace.WorkspaceID, WorkspaceGeneration: task.SourceWorkspace.WorkspaceGeneration,
		BaseOID: task.BaseCommit, SourceBranch: task.WorktreeBranch, TargetBranch: task.BaseBranch,
		State: "unavailable", ReasonCode: "lane_unavailable", Reason: "Authenticated captured task lane is unavailable", Freshness: "unknown", AllowedActions: []string{},
	}
	res := taskGitState{worktreeBranch: task.WorktreeBranch, worktreeName: task.WorktreeName, gitStatus: "unknown", deliveryAssessment: a}
	if task.Agent == pebblestore.WorkerRoleReviewer {
		a.State, a.ReasonCode, a.Reason, a.Freshness = "report_only", "reviewer_report", "Reviewer jobs return reports; nothing can be delivered", "observed"
		return res
	}
	if folderTask(&task) && task.Integration != nil && task.Integration.State == "in_folder" && task.Integration.SessionID == task.SessionID {
		a.State, a.SourceOID, a.Freshness = "in_folder", task.Integration.SourceHead, "recorded"
		a.ReasonCode, a.Reason = "folder_delivery", "Task commits are already in the folder"
		res.baseBranch, res.baseCommit, res.gitStatus = task.BaseBranch, task.BaseCommit, "clean"
		return res
	}
	if receipt := task.Integration; receipt != nil && taskDeliveredState(receipt.State, receipt.PullRequestURL) != "" {
		// The receipt describes delivery already performed. Do not inspect a
		// possibly moved or removed checkout as if delivery were still pending.
		a.State, a.ReasonCode, a.Reason = receipt.State, "delivery_receipt", "Job delivery recorded"
		return res
	}
	if db == nil || task.SessionID == "" || task.Archived || task.Status == "pending_approval" || task.Status == "planning" || task.Status == "queued" || task.Agent == "image" || task.Agent == "video" || task.Agent == "sound" || task.Agent == "audio" {
		return res
	}
	session, found, err := db.GetSession(task.SessionID)
	if err != nil || !found || task.AccountID == "" || task.SourceWorkspace.WorkspaceID == "" || task.SourceWorkspace.WorkspaceGeneration <= 0 || verifyProjectTaskSession(&task, session, task.AccountID) != nil || !session.WorktreeEnabled || session.WorktreeBranch == "" || session.WorktreeBaseBranch == "" || (task.WorktreeBranch != "" && task.WorktreeBranch != session.WorktreeBranch) || (task.BaseBranch != "" && task.BaseBranch != session.WorktreeBaseBranch) {
		return res
	}
	claims, claimErr := db.InspectWorktreeOwnership(task.AccountID, session.UserID, []string{session.WorktreeRootPath})
	if claimErr != nil || len(claims) != 1 || claims[0].OwnerSessionID != session.ID || claims[0].ClaimantSessionID != "" {
		return res
	}
	source := strings.TrimSpace(sessionsV3MetadataString(session.Metadata, "swarm_v3_source_workspace_path"))
	base := strings.TrimSpace(sessionsV3MetadataString(session.Metadata, "base_commit"))
	if source == "" || base == "" || source == session.WorktreeRootPath || task.SourceWorkspace.Path != source || (task.BaseCommit != "" && task.BaseCommit != base) {
		return res
	}
	res.worktreeBranch, res.baseBranch, res.baseCommit = session.WorktreeBranch, session.WorktreeBaseBranch, base
	res.worktreeName = strings.TrimPrefix(strings.TrimPrefix(res.worktreeBranch, "agent/"), "worktree/")
	a.BaseOID, a.SourceBranch, a.TargetBranch = base, res.worktreeBranch, res.baseBranch
	in := worktree.TaskDeliveryInput{Identity: *a, SourcePath: session.WorktreeRootPath, TargetPath: source}
	assessment := worktree.AssessTaskDelivery(ctx, in)
	in.Identity = assessment
	assessment = worktree.ObserveTaskDeltaReceipt(ctx, in, task.Integration)
	if attempt := task.ActiveAttempt(); attempt != nil && attempt.Recovery != nil && attempt.Recovery.PreparedHead != "" && assessment.SourceOID == attempt.Recovery.PreparedHead {
		assessment.State, assessment.ReasonCode, assessment.Reason = "ambiguous", "unresolved_recovery", "Resolve and commit the retained task-delta conflicts in the repair session before integration"
		assessment.AllowedActions = []string{}
	}
	res.deliveryAssessment = &assessment
	res.actionNeeded = taskAssessmentAction(&assessment)
	switch assessment.State {
	case "integrated":
		res.gitStatus, res.isIntegrated = "clean", true
	case "empty", "recovered", "equivalent":
		res.gitStatus = "clean"
	case "dirty":
		res.gitStatus, res.isDirty = "dirty", true
	case "candidate_work":
		res.gitStatus, res.unintegratedCommits = "diverged", assessment.CandidateCommits
	case "history_equivalent", "history_rewritten", "ambiguous":
		res.gitStatus = "diverged"
	}
	res.isDirty = assessment.SourceDirty
	if assessment.SourceDirty || assessment.TargetDirty {
		res.gitStatus = "dirty"
		res.actionNeeded = taskDeliveryDirtyMessage(&assessment)
	}
	if res.isIntegrated && (task.TaskProgramID != "" || task.TaskProgram != nil) {
		programID := task.TaskProgramID
		if programID == "" && task.TaskProgram != nil {
			programID = task.TaskProgram.ID
		}
		program, found, err := db.GetTaskProgram(task.SessionID, programID)
		if err != nil || !found || program.State != pebblestore.TaskProgramStateCompleted {
			res.isIntegrated, res.gitStatus = false, "unknown"
			assessment.State, assessment.ReasonCode, assessment.Reason = "unavailable", "program_incomplete", "Task Program is not fully completed and promoted to the captured target"
			assessment.AllowedActions = []string{}
			res.actionNeeded = assessment.Reason
		}
	}
	return res
}

// taskAssessmentAction keeps transient observation failures as status notes,
// never instructions for the person. The next status read reassesses Git.
func taskAssessmentAction(a *pebblestore.TaskDeliveryAssessment) string {
	if a.ReasonCode == "inspection_timeout" || a.ReasonCode == "inspection_cancelled" {
		a.Reason = "Git state not read in time; will retry"
		return ""
	}
	if a.State == "integrated" || a.State == "empty" {
		return ""
	}
	return a.Reason
}

// Reads project ephemeral facts only: never change execution outcome, historical
// integration receipts, actionable errors, or publish read-triggered events.
func reconcileTaskGitState(db *pebblestore.SessionStore, task *pebblestore.ProjectTaskRecord) error {
	if task == nil {
		return nil
	}
	return reconcileTaskGitStateContext(context.Background(), db, task)
}

func reconcileTaskGitStateContext(ctx context.Context, db *pebblestore.SessionStore, task *pebblestore.ProjectTaskRecord) error {
	if task == nil || task.Design != nil {
		return nil
	}
	state := inspectTaskGitStateContext(ctx, *task, db)
	task.DeliveryAssessment = state.deliveryAssessment
	task.GitStatus, task.UnintegratedCommits, task.IsIntegrated = state.gitStatus, state.unintegratedCommits, state.isIntegrated
	task.IsDirty, task.DirtyCount = state.isDirty, state.dirtyCount
	if state.baseCommit != "" {
		task.WorktreeBranch, task.WorktreeName, task.BaseBranch, task.BaseCommit = state.worktreeBranch, state.worktreeName, state.baseBranch, state.baseCommit
	}
	if task.LastError == "" && (task.Integration == nil || task.Integration.Error == "") {
		task.ActionNeeded = state.actionNeeded
	}
	return nil
}
