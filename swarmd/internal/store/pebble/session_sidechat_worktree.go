package pebblestore

// validateSidechatWorktreeBinding authorizes borrowing, never ownership or
// recovery. Runtime admission separately validates the parent's Git identity.
func (s *SessionStore) validateSidechatWorktreeBinding(input V3SessionMutationInput, next SessionSnapshot) error {
	kind := v3LibraryMetadataString(next.Metadata, "system_sidechat_kind")
	parentID := v3LibraryMetadataString(next.Metadata, "parent_session_id")
	if (kind != "plan" && kind != "ai") || parentID == "" || parentID == input.SessionID || !next.WorktreeEnabled {
		return ErrWorktreeRecoveryConflict
	}
	parent, ok, err := s.GetSession(parentID)
	if err != nil {
		return err
	}
	if !ok || !parent.WorktreeEnabled || parent.UserID != input.UserID || parent.AccountScopeID != input.AccountScopeID ||
		v3LibraryMetadataString(parent.Metadata, "lineage_kind") == "system_sidechat" ||
		v3LibraryMetadataString(parent.Metadata, "swarm_v3_worktree_owner_session_id") != parent.ID ||
		next.WorkspacePath != parent.WorkspacePath || next.WorktreeRootPath != parent.WorktreeRootPath ||
		next.WorktreeBranch != parent.WorktreeBranch || next.WorktreeBaseBranch != parent.WorktreeBaseBranch {
		return ErrWorktreeRecoveryConflict
	}
	for _, key := range []string{"swarm_v3_source_workspace_id", "swarm_v3_source_workspace_generation", "swarm_v3_source_workspace_path", "swarm_v3_runtime_workspace_path", "swarm_v3_worktree_owner_session_id", "swarm_v3_worktree_base_commit", "base_commit"} {
		if v3LibraryMetadataString(next.Metadata, key) != v3LibraryMetadataString(parent.Metadata, key) {
			return ErrWorktreeRecoveryConflict
		}
	}
	var claim WorktreeOwnership
	found, err := s.store.GetJSON(worktreeOwnershipKey(parent.WorktreeRootPath), &claim)
	if err != nil {
		return err
	}
	// Legacy parents may have no ownership projection yet. Borrowing creates
	// none; it cannot authorize recovery or supplant an existing claim.
	if found && (claim.OwnerSessionID != parent.ID || claim.AccountScopeID != input.AccountScopeID || claim.UserID != input.UserID || claim.ClaimantSessionID != "") {
		return ErrWorktreeRecoveryConflict
	}
	return s.validateRetainedWorktreeProgramClaims(parent.WorktreeRootPath, parent.ID)
}
