package pebblestore

// DeviceCallContext returns the session and run a device call belongs to.
// Client sessions (devices) are not read here: they live in the identity
// session store, which the daemon keeps on the secrets database, not on the
// main database this PermissionStore is built on.
func (s *PermissionStore) DeviceCallContext(sessionID, runID string) (SessionSnapshot, V3SessionRunIntent, error) {
	sessions := NewSessionStore(s.store)
	session, _, err := sessions.GetSession(sessionID)
	if err != nil {
		return session, V3SessionRunIntent{}, err
	}
	run, _, err := sessions.GetV3SessionRunIntent(sessionID, runID)
	return session, run, err
}
