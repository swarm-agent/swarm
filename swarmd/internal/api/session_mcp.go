package api

import (
	"path/filepath"
	"strings"

	"swarm/packages/swarmd/internal/provider/claudecode"
	"swarm/packages/swarmd/internal/provider/registry"
)

// SetSessionMCP mounts only on LoopbackAPIHandler, not the SDK listener, private
// socket, peer transport or Desktop proxy.
func (s *Server) SetSessionMCP(endpoint *claudecode.SessionMCP) { s.sessionMCP = endpoint }

// connectClaudeCodeSessionMCP gives a registered Claude Code runner the standing
// /mcp endpoint. Its state lives under the configured daemon data directory:
// the root is handed over by WithDataDir or SetDataDir, the address on bind,
// and a recovered turn waits (WaitReady) until both are set.
func (s *Server) connectClaudeCodeSessionMCP(providers *registry.Registry) {
	if s == nil || providers == nil || s.security == nil {
		return
	}
	runner, ok := providers.GetRunner(claudecode.ProviderID)
	if !ok {
		return
	}
	claudeCode, ok := runner.(*claudecode.Runner)
	if !ok {
		return
	}
	endpoint := claudecode.NewSessionMCP(s.security, s.claudeCodeSessionRoot())
	claudeCode.SetSessionMCP(endpoint)
	s.SetSessionMCP(endpoint)
}

func (s *Server) claudeCodeSessionRoot() string {
	dataDir := strings.TrimSpace(s.dataDir)
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "claudecode")
}
