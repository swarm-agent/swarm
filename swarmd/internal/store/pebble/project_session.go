package pebblestore

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// ProjectConversationID is server-owned provenance, not client project metadata.
func ProjectConversationID(session SessionSnapshot) string {
	id, _ := session.Metadata["swarm_v3_project_id"].(string)
	return strings.TrimSpace(id)
}

// ValidateProjectConversation keeps conversation authority separate from all
// filesystem grants, including temporary grants and worktree overrides.
func (s *SessionStore) ValidateProjectConversation(session SessionSnapshot, accountID, userID string) error {
	id := ProjectConversationID(session)
	if id == "" || accountID == "" || userID == "" || session.AccountScopeID != accountID || session.UserID != userID {
		return errors.New("project conversation ownership mismatch")
	}
	if session.Metadata["agent_name"] != "system-orchestrator" || session.Metadata["resolved_agent_name"] != "system-orchestrator" || session.Metadata["project_id"] != id {
		return errors.New("project conversation agent or project identity mismatch")
	}
	if session.WorkspacePath != "" || session.WorkspaceName != "" || session.WorktreeEnabled || session.WorktreeRootPath != "" || session.WorktreeBranch != "" || session.WorktreeBaseBranch != "" || len(session.WorkspaceGrants) != 0 || len(session.TemporaryWorkspaceRoots) != 0 {
		return errors.New("project conversation cannot carry filesystem authority")
	}
	for _, key := range []string{"task_id", "task_attempt_id", "workspace_path", "worktree_root_path", "worktree_enabled"} {
		if _, supplied := session.Metadata[key]; supplied {
			return errors.New("project conversation cannot carry task or workspace metadata")
		}
	}
	project, found, err := s.GetProject(accountID, id)
	if err != nil {
		return err
	}
	if !found || project == nil || project.AccountID != accountID {
		return errors.New("project conversation project not found")
	}
	return nil
}

// ListProjectConversations reads only compact project/user ordered summaries.
// Membership is maintained by canonical session batches, not a primary pointer.
func (s *SessionStore) ListProjectConversations(accountID, userID, projectID string, limit int) ([]SessionSnapshot, error) {
	if accountID == "" || userID == "" || projectID == "" {
		return nil, errors.New("project conversation listing requires account, user, and project")
	}
	return s.listProjectConversationSummaries(accountID, userID, projectID, limit)
}

// validateProjectPermissionTransitionLocked runs while the session mutation lock
// is held, so a terminal run cannot race a pending permission decision to disk.
func (s *PermissionStore) validateProjectPermissionTransitionLocked(record PermissionRecord, previous *PermissionRecord) error {
	if previous == nil || previous.Status != PermissionStatusPending || record.Status == PermissionStatusPending {
		return nil
	}
	sessions := NewSessionStore(s.store)
	session, found, err := sessions.GetSession(record.SessionID)
	if err != nil {
		return err
	}
	if !found || ProjectConversationID(session) == "" {
		return nil
	}
	if err := sessions.ValidateProjectConversation(session, session.AccountScopeID, session.UserID); err != nil {
		return err
	}
	current, found, err := s.GetPermission(record.SessionID, record.ID)
	if err != nil {
		return err
	}
	if !found || current.Status != PermissionStatusPending || current.RunID != record.RunID || current.CallID != record.CallID {
		return errors.New("project permission changed before resolution")
	}
	if previous.SessionID != record.SessionID || previous.ID != record.ID || previous.RunID != record.RunID || previous.CallID != record.CallID || record.RunID == "" || record.CallID == "" {
		return errors.New("project permission identity mismatch")
	}
	// Run termination must still be able to cancel its retained pending records.
	if record.Status == PermissionStatusCancelled {
		return nil
	}
	active, found, err := sessions.GetV3SessionActiveRunIntent(record.SessionID)
	if err != nil {
		return err
	}
	if !found || active.RunID != record.RunID {
		return errors.New("project permission run is no longer active")
	}
	return nil
}

// ListArchivedProjectConversations filters before limiting; unrelated tombstones
// must not hide a project's restorable history. Scan only the account index.
func (s *SessionStore) ListArchivedProjectConversations(accountID, userID, projectID string, limit int) ([]V3SessionTombstone, error) {
	if accountID == "" || userID == "" || projectID == "" {
		return nil, errors.New("project archive listing requires account, user, and project")
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	out := make([]V3SessionTombstone, 0)
	err := scanRangeFromReader(s.store.db, scanRangeOptions{Prefix: V3SessionTombstoneByAccountPrefix(accountID)}, func(_ string, value []byte) (bool, error) {
		var item V3SessionTombstone
		if err := json.Unmarshal(value, &item); err != nil {
			return false, err
		}
		session := item.Session
		if item.Archived && !item.Deleted && item.AccountScopeID == accountID && item.UserID == userID && session.AccountScopeID == accountID && session.UserID == userID && session.Metadata["project_id"] == projectID && session.Metadata["agent_name"] == "system-orchestrator" && session.Metadata["task_id"] == nil && session.Metadata["parent_session_id"] == nil {
			out = append(out, item)
			sort.Slice(out, func(i, j int) bool {
				if out[i].UpdatedAt != out[j].UpdatedAt {
					return out[i].UpdatedAt > out[j].UpdatedAt
				}
				return out[i].SessionID < out[j].SessionID
			})
			if len(out) > limit {
				out = out[:limit]
			}
		}
		return true, nil
	})
	return out, err
}
