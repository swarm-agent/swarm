package api

import (
	"errors"
	"net/http"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Human management stays account-authorized. Any session attribution must also
// cross the durable agent boundary; a body session ID never overrides a scoped
// principal or silently falls back to human authority when invalid.
func (s *Server) authorizeEnvironmentHTTPSession(r *http.Request, selected, toolName string) (pebblestore.SessionSnapshot, error) {
	deny := errors.New("environment access denied: authenticated Swarm or Orchestrator session required")
	p, ok := PrincipalFromRequest(r)
	if !ok || !p.Valid() {
		return pebblestore.SessionSnapshot{}, deny
	}
	selected = strings.TrimSpace(selected)
	if p.SessionID != "" {
		if selected != "" && selected != p.SessionID {
			return pebblestore.SessionSnapshot{}, deny
		}
		selected = p.SessionID
	}
	if selected == "" {
		return pebblestore.SessionSnapshot{}, nil
	}
	if s.sessions == nil {
		return pebblestore.SessionSnapshot{}, deny
	}
	snap, found, err := s.sessions.GetSession(selected)
	if err != nil || !found || snap.AccountScopeID != p.AccountScopeID || snap.UserID != p.UserID || !tool.EnvironmentToolAllowed(snap.Metadata, toolName) {
		return pebblestore.SessionSnapshot{}, deny
	}
	return snap, nil
}
