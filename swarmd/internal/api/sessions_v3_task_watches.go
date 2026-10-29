package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// A conversation's task watches (manage_projects wait_tasks). GET lists them;
// POST task-watches/stop is the person's stop: it ends the conversation's
// watches (or one watch_id, or only task_ids) through the canonical mutation,
// with a visible "Stopped watching" line. A watch never makes the conversation
// pending, so no run is involved.

type sessionsV3TaskWatchStopRequest struct {
	WatchID string   `json:"watch_id,omitempty"`
	TaskIDs []string `json:"task_ids,omitempty"`
}

func (s *Server) handleSessionV3TaskWatches(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if found, err := s.authorizeSessionsV3PrimarySession(principal, sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if !found {
		writeSessionNotFound(w)
		return
	}
	watches, err := s.sessions.Store().ListV3SessionTaskWatches(sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": sessionID, "task_watches": pebblestore.V3TaskWatchSummaries(watches)})
}

func (s *Server) handleSessionV3TaskWatchesStop(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req sessionsV3TaskWatchStopRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if found, err := s.authorizeSessionsV3PrimarySession(principal, sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if !found {
		writeSessionNotFound(w)
		return
	}
	op := &pebblestore.V3ProjectTaskWaitMutation{Cancel: true, CancelledBy: pebblestore.ProjectTaskWaitEndedByStop, WatchID: strings.TrimSpace(req.WatchID), TaskIDs: req.TaskIDs}
	now := time.Now().UnixMilli()
	sum := sha256.Sum256([]byte(sessionID + "\x00" + op.WatchID + "\x00" + strings.Join(req.TaskIDs, "\x00") + "\x00" + time.UnixMilli(now).UTC().Format(time.RFC3339Nano)))
	key := "task-watch-stop:" + hex.EncodeToString(sum[:16])
	result, err := s.sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
		SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		Kind: pebblestore.V3SessionMutationAppendMessage, EventType: "session.task_watch.cancelled",
		ClientRequestID: key, PayloadHash: key, NowUnixMs: now, TaskWait: op,
	})
	if errors.Is(err, pebblestore.ErrProjectTaskWatchNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	watches, err := s.sessions.Store().ListV3SessionTaskWatches(sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "stopped_task_ids": op.CancelledTaskIDs, "task_watches": pebblestore.V3TaskWatchSummaries(watches), "event_seq": result.PrimarySeq})
}
