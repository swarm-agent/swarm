package api

import (
	"errors"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	V3PlanRuntimeHydratePath = "/v3/plan-runtime:hydrate"
	V3PlanRuntimeReplayPath  = "/v3/plan-runtime:replay"
	V3PlanRuntimeCommandPath = "/v3/plan-runtime:command"
)

type sessionsV3PlanRuntimeHydrateRequest struct {
	SessionID          string `json:"session_id"`
	PlanID             string `json:"plan_id"`
	DefinitionRevision uint64 `json:"definition_revision"`
}

type sessionsV3PlanRuntimeReplayRequest struct {
	SessionID string `json:"session_id"`
	PlanID    string `json:"plan_id"`
	AfterSeq  uint64 `json:"after_execution_seq"`
	Limit     int    `json:"limit,omitempty"`
}

type sessionsV3PlanRuntimeCommandRequest struct {
	SessionID            string   `json:"session_id"`
	PlanID               string   `json:"plan_id"`
	Action               string   `json:"action"`
	DefinitionRevision   uint64   `json:"definition_revision"`
	ExpectedExecutionSeq uint64   `json:"expected_execution_seq"`
	ClientRequestID      string   `json:"client_request_id"`
	CheckpointID         string   `json:"checkpoint_id,omitempty"`
	SubtaskIDs           []string `json:"subtask_ids,omitempty"`
	CompleteCheckpoint   bool     `json:"complete_checkpoint,omitempty"`
	NextSubtaskID        string   `json:"next_subtask_id,omitempty"`
	AttemptID            string   `json:"attempt_id,omitempty"`
	Outcome              string   `json:"outcome,omitempty"`
	EvidenceRef          string   `json:"evidence_ref,omitempty"`
	NextAction           string   `json:"next_action,omitempty"`
	RunID                string   `json:"run_id,omitempty"`
	EpochID              string   `json:"epoch_id,omitempty"`
	RunSessionID         string   `json:"run_session_id,omitempty"`
	ParentSessionID      string   `json:"parent_session_id,omitempty"`
}

func (s *Server) handleSessionsV3PlanRuntimeCommand(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.sessionsV3PlanRuntimePrincipal(w, r)
	if !ok {
		return
	}
	var req sessionsV3PlanRuntimeCommandRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.SessionID, req.PlanID = strings.TrimSpace(req.SessionID), strings.TrimSpace(req.PlanID)
	if allowed, err := s.authorizeReadableSessionV3Access(principal, req.SessionID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	} else if !allowed {
		writeError(w, http.StatusNotFound, errors.New("session not found"))
		return
	}
	receipt, err := sessionruntime.NewPlanRuntimeCommandService(s.sessions.Store()).Execute(sessionruntime.PlanRuntimeExecutionInput{
		Action: req.Action, SessionID: req.SessionID, PlanID: req.PlanID,
		AccountScopeID: strings.TrimSpace(principal.AccountScopeID), ActorID: strings.TrimSpace(principal.UserID),
		DefinitionRevision: req.DefinitionRevision, ExpectedExecutionSeq: req.ExpectedExecutionSeq, ClientRequestID: req.ClientRequestID,
		CheckpointID: req.CheckpointID, SubtaskIDs: req.SubtaskIDs, CompleteCheckpoint: req.CompleteCheckpoint, NextSubtaskID: req.NextSubtaskID,
		AttemptID: req.AttemptID, Outcome: req.Outcome, EvidenceRef: req.EvidenceRef, NextAction: req.NextAction,
		RunID: req.RunID, EpochID: req.EpochID, RunSessionID: req.RunSessionID, ParentSessionID: req.ParentSessionID,
	})
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, pebblestore.ErrPlanRuntimeIdempotencyConflict) || errors.Is(err, pebblestore.ErrPlanRuntimeExecutionConflict) {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "receipt": receipt})
}

func (s *Server) handleSessionsV3PlanRuntimeHydrate(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.sessionsV3PlanRuntimePrincipal(w, r)
	if !ok {
		return
	}
	var req sessionsV3PlanRuntimeHydrateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.SessionID, req.PlanID = strings.TrimSpace(req.SessionID), strings.TrimSpace(req.PlanID)
	if req.SessionID == "" || req.PlanID == "" || req.DefinitionRevision == 0 {
		writeError(w, http.StatusBadRequest, errors.New("session_id, plan_id, and definition_revision are required"))
		return
	}
	if allowed, err := s.authorizeReadableSessionV3Access(principal, req.SessionID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	} else if !allowed {
		writeError(w, http.StatusNotFound, errors.New("session not found"))
		return
	}
	hydration, found, err := s.sessions.Store().HydratePlanRuntime(req.SessionID, req.PlanID, req.DefinitionRevision)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, errors.New("plan runtime not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "plan_runtime": hydration})
}

func (s *Server) handleSessionsV3PlanRuntimeReplay(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.sessionsV3PlanRuntimePrincipal(w, r)
	if !ok {
		return
	}
	var req sessionsV3PlanRuntimeReplayRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.SessionID, req.PlanID = strings.TrimSpace(req.SessionID), strings.TrimSpace(req.PlanID)
	if req.SessionID == "" || req.PlanID == "" {
		writeError(w, http.StatusBadRequest, errors.New("session_id and plan_id are required"))
		return
	}
	if allowed, err := s.authorizeReadableSessionV3Access(principal, req.SessionID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	} else if !allowed {
		writeError(w, http.StatusNotFound, errors.New("session not found"))
		return
	}
	page, err := s.sessions.Store().ListPlanExecutionOutboxAfter(req.SessionID, req.PlanID, req.AfterSeq, req.Limit)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "protocol": pebblestore.PlanExecutionRealtimeProtocol,
		"protocol_version": pebblestore.PlanRuntimeSchemaVersion, "plan_id": req.PlanID,
		"records": page.Records, "next_after_execution_seq": page.NextAfterSeq,
		"has_more": page.HasMore, "encoded_bytes": page.EncodedBytes,
	})
}

func (s *Server) sessionsV3PlanRuntimePrincipal(w http.ResponseWriter, r *http.Request) (identity.Principal, bool) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return identity.Principal{}, false
	}
	if s == nil || s.sessions == nil || s.sessions.Store() == nil {
		writeError(w, http.StatusInternalServerError, errors.New("sessions v3 service is not configured"))
		return identity.Principal{}, false
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return identity.Principal{}, false
	}
	return principal, true
}
