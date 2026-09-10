package api

import (
	"log"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// ConfigureAutomationRealtime wires startup publication to the durable V3 hub.
// Missed wakeups are repaired by scoped outbox replay, never by polling.
func (s *Server) ConfigureAutomationRealtime(store *pebblestore.Store) {
	store.SetAutomationPublisher(func(record pebblestore.V3RealtimeOutboxRecord) {
		if err := s.publishCommittedV3RealtimeOutbox(record); err != nil {
			log.Printf("automation realtime wake failed: %v", err)
		}
	})
}
