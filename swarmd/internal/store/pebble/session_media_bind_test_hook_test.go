package pebblestore

// SetMediaStagingBindCommitHookForTest installs a failure seam immediately
// before the atomic routed media/session authority batch commits.
func (s *SessionStore) SetMediaStagingBindCommitHookForTest(hook func(sessionID string) error) func() {
	if s == nil || s.store == nil {
		return func() {}
	}
	previous := s.store.sessionMutations.beforeMediaStagingBindCommit
	s.store.sessionMutations.beforeMediaStagingBindCommit = hook
	return func() { s.store.sessionMutations.beforeMediaStagingBindCommit = previous }
}
