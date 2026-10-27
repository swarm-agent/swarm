package pebblestore

func (s *PermissionStore) DeviceCallContext(sessionID, runID string) (SessionSnapshot, V3SessionRunIntent, []ClientSessionRecord, error) {
	sessions := NewSessionStore(s.store)
	session, _, err := sessions.GetSession(sessionID)
	if err != nil {
		return session, V3SessionRunIntent{}, nil, err
	}
	run, _, err := sessions.GetV3SessionRunIntent(sessionID, runID)
	if err != nil {
		return session, run, nil, err
	}
	clients, err := NewIdentitySessionStore(s.store).ListClientSessions()
	return session, run, clients, err
}
