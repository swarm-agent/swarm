package api

import "swarm/packages/swarmd/internal/provider/claudecode"

// SetSessionMCP mounts only on LoopbackAPIHandler, not the SDK listener, private
// socket, peer transport or Desktop proxy.
func (s *Server) SetSessionMCP(endpoint *claudecode.SessionMCP) { s.sessionMCP = endpoint }

func (s *Server) SetSessionMCPAddress(addr string) error {
	if s.sessionMCP == nil {
		return nil
	}
	return s.sessionMCP.SetAddress(addr)
}
