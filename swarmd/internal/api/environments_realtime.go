package api

import (
	"log"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// ConfigureEnvironmentRealtime wires startup publication for environment changes to the durable V3 hub.
// Missed wakeups are repaired by scoped outbox replay, never by polling.
func (s *Server) ConfigureEnvironmentRealtime(store *pebblestore.Store) {
	if s == nil || store == nil {
		return
	}
	store.SetEnvironmentPublisher(func(record pebblestore.V3RealtimeOutboxRecord) {
		if err := s.publishCommittedV3RealtimeOutbox(record); err != nil {
			log.Print("environment realtime wake failed; durable replay required")
		}
	})
}
