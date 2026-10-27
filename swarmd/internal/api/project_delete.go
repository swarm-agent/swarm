package api

import (
	"errors"
	"fmt"
	"net/http"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Server) deleteProject(w http.ResponseWriter, account, id string) {
	db := s.sessions.Store()
	if _, found, err := db.GetProject(account, id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	} else if !found {
		writeError(w, http.StatusNotFound, pebblestore.ErrProjectNotFound)
		return
	}
	sessions, err := db.ListSessionsMatching(10001, func(session pebblestore.SessionSnapshot) bool {
		return session.AccountScopeID == account && session.Metadata["project_id"] == id
	})
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if len(sessions) > 10000 {
		writeError(w, 409, errors.New("project has too many sessions to delete safely"))
		return
	}
	active := 0
	for _, session := range sessions {
		_, running, err := db.GetV3SessionActiveRunIntent(session.ID)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		lifecycle, found, err := db.GetSessionLifecycle(session.ID)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if running || (found && lifecycle.Active) {
			active++
		}
	}
	if active > 0 {
		writeError(w, 409, fmt.Errorf("cannot delete project: %d sessions have active runs", active))
		return
	}
	if err := db.DeleteProject(account, id); err != nil {
		status := http.StatusInternalServerError
		var unsettled *pebblestore.ProjectTasksUnsettledError
		if errors.As(err, &unsettled) {
			status = http.StatusConflict
		}
		if errors.Is(err, pebblestore.ErrProjectNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	if len(sessions) > 0 {
		events, err := s.sessions.ArchiveSessionsWithEvents(sessionIDsFromSnapshots(sessions))
		if err != nil {
			writeError(w, 500, fmt.Errorf("project deleted, but session archiving failed: %w", err))
			return
		}
		s.publishSessionsV3ArchiveRealtime(sessions, events)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "id": id})
}
