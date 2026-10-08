package api

import (
	"context"
	"errors"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Capture the executing conversation, not the legacy mutable project primary
// pointer. HTTP/workspace task creation has no originating project conversation.
func (s *Server) projectTaskOrigin(ctx context.Context, p identity.Principal, projectID string) (string, error) {
	run, ok := tool.VideoRunContextFromContext(ctx)
	if !ok {
		return "", nil
	}
	db := s.sessions.Store()
	parent, found, err := db.GetSession(run.SessionID)
	if err != nil {
		return "", err
	}
	if !found || parent.AccountScopeID != p.AccountScopeID || parent.UserID != p.UserID {
		return "", errors.New("task origin session ownership mismatch")
	}
	if pebblestore.ProjectConversationID(parent) == "" {
		return "", nil
	}
	if err := db.ValidateProjectConversation(parent, p.AccountScopeID, p.UserID); err != nil {
		return "", err
	}
	if pebblestore.ProjectConversationID(parent) != projectID {
		return "", errors.New("task origin project mismatch")
	}
	tomb, found, err := db.GetV3SessionTombstone(parent.ID)
	if err != nil {
		return "", err
	}
	if found && (tomb.Archived || tomb.Deleted) {
		return "", errors.New("task origin archived or deleted")
	}
	state, found, err := db.GetV3SessionRunState(parent.ID)
	if err != nil {
		return "", err
	}
	if !found || state.RunID != run.RunID || state.Status != pebblestore.V3RunIntentRunning {
		return "", errors.New("task origin provider run is no longer current")
	}
	return parent.ID, nil
}
