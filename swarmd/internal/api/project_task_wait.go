package api

import (
	"log"
	"time"
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

// scheduleTaskWatchCheck checks a watch again when its milestone wake is due
// (a milestone it reached was held by the once-a-minute limit, and the
// milestones then are coalesced into one wake). One pending check per watch;
// a restart re-checks every watch at startup instead.
func (s *Server) scheduleTaskWatchCheck(sessionID, watchID string, at int64) {
	if s == nil || s.sessions == nil {
		return
	}
	key := sessionID + "\x00" + watchID
	s.remoteTaskWaitMu.Lock()
	if s.taskWatchChecks == nil {
		s.taskWatchChecks = map[string]bool{}
	}
	if s.taskWatchChecks[key] {
		s.remoteTaskWaitMu.Unlock()
		return
	}
	s.taskWatchChecks[key] = true
	s.remoteTaskWaitMu.Unlock()
	time.AfterFunc(max(time.Until(time.UnixMilli(at)), 0), func() {
		s.remoteTaskWaitMu.Lock()
		delete(s.taskWatchChecks, key)
		kick := s.remoteTaskWaitRuns[key]
		s.remoteTaskWaitMu.Unlock()
		if s.shuttingDown.Load() {
			return
		}
		if kick != nil {
			// A linked-machine watch: its follower reads the machine again.
			select {
			case kick <- struct{}{}:
			default:
			}
			return
		}
		if err := s.sessions.Store().ReconcileSessionTaskWatches(sessionID, s.publishProjectTaskWake); err != nil {
			log.Printf("project task watch check failed: %v", err)
		}
	})
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
