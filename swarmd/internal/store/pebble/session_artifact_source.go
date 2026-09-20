package pebblestore

// GetRetainedArtifactSourceSession resolves read-only source ownership from the
// canonical live snapshot or retained archive tombstone. It never reactivates a
// session, and must not be used to authorize a destination mutation.
func (s *SessionStore) GetRetainedArtifactSourceSession(id string) (SessionSnapshot, bool, error) {
	session, ok, err := s.GetSession(id)
	if err != nil || ok {
		return session, ok, err
	}
	tombstone, ok, err := s.GetV3SessionTombstone(id)
	if err != nil || !ok {
		return SessionSnapshot{}, false, err
	}
	if tombstone.Deleted || !tombstone.Archived || tombstone.Session.ID != id || tombstone.Session.AccountScopeID != tombstone.AccountScopeID || tombstone.Session.UserID != tombstone.UserID {
		return SessionSnapshot{}, false, nil
	}
	return tombstone.Session, true, nil
}
