package session

import (
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Service) ClaudeCodeSession(p identity.Principal, sessionID string) (string, error) {
	row, ok, err := s.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	if !ok || row.AccountScopeID != p.AccountScopeID || row.UserID != p.UserID {
		return "", errors.New("provider session owner mismatch")
	}
	return row.ClaudeCodeSessionID, nil
}

// RecordClaudeCodeSession crosses the canonical V3 mutation boundary and changes
// only the continuation field, never a stale copy of the session metadata.
func (s *Service) RecordClaudeCodeSession(p identity.Principal, sessionID, cliID string) error {
	if strings.TrimSpace(cliID) == "" || len(cliID) > 256 || strings.ContainsAny(cliID, "/\\\r\n\x00") {
		return errors.New("invalid Claude Code session id")
	}
	key, err := pebblestore.GenerateToken(16)
	if err != nil {
		return err
	}
	_, err = s.ApplySessionMutation(SessionMutationInput{SessionID: sessionID, AccountScopeID: p.AccountScopeID, UserID: p.UserID,
		Kind: SessionMutationUpdateMetadata, ClientRequestID: "claudecode-" + key, PayloadHash: fmt.Sprintf("claudecode:%s", cliID), ClaudeCodeSessionID: &cliID})
	return err
}
