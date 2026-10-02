package pebble

import (
	"errors"
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
	if session.WorkspacePath != "" || session.WorkspaceName != "" || session.WorktreeEnabled || session.WorktreeRootPath != "" || len(session.WorkspaceGrants) != 0 || len(session.TemporaryWorkspaceRoots) != 0 {
		return errors.New("project conversation cannot carry filesystem authority")
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

// ListProjectConversations filters the account index before applying the result
// limit. Legacy project-bound Orchestrator conversations remain discoverable;
// the primary-session pointer is not the membership authority.
func (s *SessionStore) ListProjectConversations(accountID, userID, projectID string, limit int) ([]SessionSnapshot, error) {
	if accountID == "" || userID == "" || projectID == "" {
		return nil, errors.New("project conversation listing requires account, user, and project")
	}
	return s.listSessionsForAccount(accountID, limit, func(session SessionSnapshot) bool {
		return session.UserID == userID && session.Metadata["project_id"] == projectID && session.Metadata["agent_name"] == "system-orchestrator"
	})
}
