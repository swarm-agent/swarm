package api

import (
	"log"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// ConfigureProjectRealtime wires startup publication for project and task updates to the durable V3 hub.
// Missed wakeups are repaired by scoped outbox replay, never by polling.
func (s *Server) ConfigureProjectRealtime(store *pebblestore.Store) {
	if s == nil || store == nil {
		return
	}
	store.SetProjectPublisher(func(record pebblestore.V3RealtimeOutboxRecord) {
		if err := s.publishCommittedV3RealtimeOutbox(record); err != nil {
			log.Print("project realtime wake failed; durable replay required")
		}
	})
}
