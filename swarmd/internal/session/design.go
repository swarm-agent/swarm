package session

import pebblestore "swarm/packages/swarmd/internal/store/pebble"

// DesignStore is independent artifact history, not a session lifecycle authority.
// New acceptances must pass through ApplySessionMutation.
func (s *Service) DesignStore() *pebblestore.Store {
	if s == nil || s.store == nil { return nil }
	return s.store.DesignStore()
}
