package api

import "strings"

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
