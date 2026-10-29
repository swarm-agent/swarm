package api

import (
	"log"
)

// Task watches (manage_projects wait_tasks) are durable records on the
// conversation; the store fires them. This server's part is to call the store
// when something can make a watch ready, and to start the outcome turn.

// reconcileProjectTaskWaits fires ready local watches: on committed task
// publication, at startup and once the task progress hook exists.
func (s *Server) reconcileProjectTaskWaits(account, project string) {
	if s == nil || s.sessions == nil {
		return
	}
	err := s.sessions.Store().ReconcileProjectTaskWaits(account, project, s.publishProjectTaskWake)
	if err != nil {
		log.Printf("project task watch reconciliation failed: %v", err)
	}
}

// deliverSessionTaskWatches runs after a conversation's run ended. A watch
// whose outcomes became ready while that run was active was deferred; it is
// delivered now. Linked-machine followers of the conversation check again.
func (s *Server) deliverSessionTaskWatches(sessionID string) {
	if s == nil || s.sessions == nil || sessionID == "" {
		return
	}
	if err := s.sessions.Store().ReconcileSessionTaskWatches(sessionID, s.publishProjectTaskWake); err != nil {
		log.Printf("project task watch delivery failed: %v", err)
	}
	s.kickRemoteTaskWatches(sessionID)
}

// migrateProjectTaskWaits converts runs still asleep on the retired pending
// wait into watches, leaving those conversations idle. Startup only.
func (s *Server) migrateProjectTaskWaits() {
	if s == nil || s.sessions == nil {
		return
	}
	converted, err := s.sessions.Store().MigrateProjectTaskWaitsToWatches()
	if err != nil {
		log.Printf("project task wait migration failed: %v", err)
	}
	if converted > 0 {
		log.Printf("project task waits converted to watches: %d", converted)
	}
}

// FollowTaskWatch starts following a watch the tool just registered: a
// linked-machine watch is followed from that machine's activity. A local
// watch needs nothing here; task publication and run ends fire it.
func (s *Server) FollowTaskWatch(sessionID, watchID string) {
	s.watchRemoteTaskWatch(sessionID, watchID)
}
