package api

// waitCompanionMaintenance waits until no background Companion state pass is
// running. Every commit's nudge starts (or re-arms) its pass before the
// commit returns, so after the caller's last commit this waits for all of
// them.
func (s *Server) waitCompanionMaintenance() {
	m := &s.companionMaintenance
	m.mu.Lock()
	defer m.mu.Unlock()
	for len(m.running) > 0 {
		m.idle.Wait()
	}
}
