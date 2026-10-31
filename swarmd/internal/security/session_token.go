package security

import (
	"errors"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// CreateSessionToken uses the canonical scoped credential store. This key has
// no HTTP API scopes; only the loopback session MCP handler accepts it.
func (s *Service) CreateSessionToken(account, user, sessionID string) (string, error) {
	if strings.TrimSpace(account) == "" || strings.TrimSpace(user) == "" || strings.TrimSpace(sessionID) == "" {
		return "", errors.New("session token requires an owner and session")
	}
	token, _, err := s.createScopedTokenRecord(nil, "Session tools", []string{"session:mcp"}, account, user, 0, func(r *pebblestore.ScopedTokenRecord) { r.SessionID = sessionID; r.Kind = KeyKindInternal })
	return token, err
}
