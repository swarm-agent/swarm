package api

import (
	"net/url"
	"strings"
)

// isLocalSwarmID reports whether swarmID names the daemon's own runtime. It is
// intentionally independent of remote authority discovery and proxy routing.
func (s *Server) isLocalSwarmID(swarmID string) bool {
	swarmID = strings.TrimSpace(swarmID)
	if s == nil || s.swarm == nil || swarmID == "" {
		return false
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return false
	}
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(state.Node.SwarmID), swarmID)
}

func isLoopbackBackendURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	host := strings.TrimSpace(parsed.Hostname())
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}
