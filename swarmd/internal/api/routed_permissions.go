package api

import (
	"errors"
	"net/http"

	"swarm/packages/swarmd/internal/permission"
	"swarm/packages/swarmd/internal/tool"
)

func (s *Server) handlePeerPermissionCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.perm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("permission service is not configured"))
		return
	}
	var req struct {
		Input permission.CreateInput `json:"input"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	record, err := s.perm.CreatePending(req.Input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"permission": record,
	})
}

func (s *Server) handlePeerPermissionWait(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.perm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("permission service is not configured"))
		return
	}
	var req struct {
		SessionID    string `json:"session_id"`
		PermissionID string `json:"permission_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	record, err := s.perm.WaitForResolution(r.Context(), req.SessionID, req.PermissionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"permission": record,
	})
}

func (s *Server) handlePeerPermissionCancelRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.perm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("permission service is not configured"))
		return
	}
	var req struct {
		SessionID string `json:"session_id"`
		RunID     string `json:"run_id"`
		Reason    string `json:"reason"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	records, err := s.perm.CancelRunPending(req.SessionID, req.RunID, req.Reason)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"permissions": records,
	})
}

func (s *Server) handlePeerPermissionMarkStarted(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.perm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("permission service is not configured"))
		return
	}
	var req struct {
		SessionID string `json:"session_id"`
		RunID     string `json:"run_id"`
		CallID    string `json:"call_id"`
		Step      int    `json:"step"`
		StartedAt int64  `json:"started_at"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	record, ok, err := s.perm.MarkToolStarted(req.SessionID, req.RunID, req.CallID, req.Step, req.StartedAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"found":      ok,
		"permission": record,
	})
}

func (s *Server) handlePeerPermissionMarkCompleted(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.perm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("permission service is not configured"))
		return
	}
	var req struct {
		SessionID   string      `json:"session_id"`
		RunID       string      `json:"run_id"`
		CallID      string      `json:"call_id"`
		Step        int         `json:"step"`
		Result      tool.Result `json:"result"`
		CompletedAt int64       `json:"completed_at"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	record, ok, err := s.perm.MarkToolCompleted(req.SessionID, req.RunID, req.CallID, req.Step, req.Result, req.CompletedAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"found":      ok,
		"permission": record,
	})
}
