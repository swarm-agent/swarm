package api

// backgroundCommandLifecycle is the bash tool's per-conversation registry of
// background commands (owned by the tool runtime). The executor tells it when
// a conversation's run is stopped (its background commands are killed) and
// when a run ends (a report pinned to that run and not delivered is
// delivered again).
type backgroundCommandLifecycle interface {
	StopSessionBackgroundCommands(sessionID, reason string) int
	SessionRunEnded(sessionID, runID string)
}

// SetBackgroundCommandLifecycle installs the background command registry.
// Call it during daemon wiring, before the server handles requests.
func (s *Server) SetBackgroundCommandLifecycle(lifecycle backgroundCommandLifecycle) {
	if s == nil {
		return
	}
	s.backgroundCommands = lifecycle
}
