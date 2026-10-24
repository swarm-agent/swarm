package pebblestore

// isBorrowedReviewWorktree reports whether a session is a reviewer worker
// session that reads the reviewed job's own worktree in place: it names that
// worktree's owning session and never claims the worktree itself.
func isBorrowedReviewWorktree(next SessionSnapshot) bool {
	return v3LibraryMetadataString(next.Metadata, "worker_role") == WorkerRoleReviewer &&
		v3LibraryMetadataString(next.Metadata, "worker_review_lane_owner") != ""
}

// validateBorrowedReviewWorktreeBinding authorizes a reviewer session's
// borrowing of the reviewed job's worktree, never ownership or recovery: the
// owner is a live session of the same user and account that owns that exact
// worktree on that branch, and the reviewer session carries the owner's
// repository identity unchanged. Runtime admission separately checks Git
// (the reviewed head) and registers the worktree read-only for the reviewer.
func (s *SessionStore) validateBorrowedReviewWorktreeBinding(input V3SessionMutationInput, next SessionSnapshot) error {
	ownerID := v3LibraryMetadataString(next.Metadata, "worker_review_lane_owner")
	if ownerID == "" || ownerID == input.SessionID || !next.WorktreeEnabled ||
		!s.isAdmittedReviewSession(next) ||
		v3LibraryMetadataString(next.Metadata, "worker_review_fixes") != "" ||
		v3LibraryMetadataString(next.Metadata, "swarm_v3_worktree_owner_session_id") != ownerID {
		return ErrWorktreeRecoveryConflict
	}
	owner, ok, err := s.GetSession(ownerID)
	if err != nil {
		return err
	}
	if !ok || !owner.WorktreeEnabled || owner.UserID != input.UserID || owner.AccountScopeID != input.AccountScopeID ||
		v3LibraryMetadataString(owner.Metadata, "lineage_kind") == "system_sidechat" ||
		v3LibraryMetadataString(owner.Metadata, "swarm_v3_worktree_owner_session_id") != owner.ID ||
		next.WorktreeRootPath != owner.WorktreeRootPath || next.WorktreeBranch != owner.WorktreeBranch {
		return ErrWorktreeRecoveryConflict
	}
	for _, key := range []string{"swarm_v3_source_workspace_path", "swarm_v3_runtime_workspace_path"} {
		if v3LibraryMetadataString(next.Metadata, key) != v3LibraryMetadataString(owner.Metadata, key) {
			return ErrWorktreeRecoveryConflict
		}
	}
	var claim WorktreeOwnership
	found, err := s.store.GetJSON(worktreeOwnershipKey(owner.WorktreeRootPath), &claim)
	if err != nil {
		return err
	}
	// Borrowing creates no claim and cannot supplant one.
	if found && (claim.OwnerSessionID != owner.ID || claim.AccountScopeID != input.AccountScopeID || claim.UserID != input.UserID || claim.ClaimantSessionID != "") {
		return ErrWorktreeRecoveryConflict
	}
	return nil
}

// Native review sessions are backed by a reserved task, not a worker receipt.
func (s *SessionStore) isAdmittedReviewSession(next SessionSnapshot) bool {
	if v3LibraryMetadataString(next.Metadata, "worker_execution_run_id") != "" {
		return v3LibraryMetadataString(next.Metadata, SessionPurposeMetadataKey) == SessionPurposeAutomationExecution
	}
	task, found, err := s.GetProjectTask(next.AccountScopeID, v3LibraryMetadataString(next.Metadata, "project_id"), v3LibraryMetadataString(next.Metadata, "task_id"))
	return err == nil && found && task != nil && task.Agent == "reviewer" && task.WorkerID == "" && task.SessionID == next.ID && task.ReviewTarget != nil && task.AutomationID != "" && task.ReviewTarget["lane_owner"] == next.Metadata["worker_review_lane_owner"] && task.ReviewTarget["lane"] == next.WorktreeRootPath && task.StartCommit == next.WorktreeBaseBranch
}
