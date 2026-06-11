package api

import (
	"errors"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	runruntime "swarm/packages/swarmd/internal/run"
)

func (s *Server) handleSessionV3PrimaryCompact(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.runner == nil {
		writeError(w, http.StatusInternalServerError, errors.New("run service is not configured"))
		return
	}
	var req sessionsV3CompactRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	clientRequestID := strings.TrimSpace(firstNonEmpty(req.ClientRequestID, req.IdempotencyKey, r.Header.Get("Idempotency-Key")))
	if clientRequestID == "" {
		writeError(w, http.StatusBadRequest, errors.New("client_request_id is required"))
		return
	}
	if _, found, err := s.hydrateSessionsV3Primary(principal, sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if !found {
		writeSessionNotFound(w)
		return
	}
	runID := strings.TrimSpace(req.RunID)
	if runID == "" {
		runID = stableSessionsV3PrimaryRunID(sessionID, clientRequestID)
	}
	streamEvents := make([]runruntime.StreamEvent, 0, 8)
	result, err := s.runner.RunTurnStreaming(r.Context(), sessionID, runruntime.RunRequest{
		Prompt:       strings.TrimSpace(req.Note),
		AgentName:    strings.TrimSpace(req.AgentName),
		Instructions: strings.TrimSpace(req.Instructions),
		Compact:      true,
	}, runruntime.RunStartMeta{
		RunID:                runID,
		Principal:            principal,
		ApplySessionMutation: s.applySessionV3PrimaryMutation,
	}, func(event runruntime.StreamEvent) {
		streamEvents = append(streamEvents, event)
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	updated, found, err := s.hydrateSessionsV3Primary(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeError(w, http.StatusInternalServerError, errors.New("updated sessions v3 projection was not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"session": updated.Session,
		"result":  result,
		"events":  sessionsV3CompactStreamEvents(streamEvents),
	})
}

func sessionsV3CompactStreamEvents(events []runruntime.StreamEvent) []map[string]any {
	out := make([]map[string]any, 0, len(events))
	for _, event := range events {
		out = append(out, sessionsV3CompactStreamEvent(event))
	}
	return out
}

func sessionsV3CompactStreamEvent(event runruntime.StreamEvent) map[string]any {
	item := map[string]any{
		"type":       event.Type,
		"session_id": event.SessionID,
		"run_id":     event.RunID,
	}
	if event.Agent != "" {
		item["agent"] = event.Agent
	}
	if event.Status != "" {
		item["status"] = event.Status
	}
	if event.Step != 0 {
		item["step"] = event.Step
	}
	if event.Delta != "" {
		item["delta"] = event.Delta
	}
	if event.Summary != "" {
		item["summary"] = event.Summary
	}
	if event.ToolName != "" {
		item["tool_name"] = event.ToolName
	}
	if event.CallID != "" {
		item["call_id"] = event.CallID
	}
	if event.Arguments != "" {
		item["arguments"] = event.Arguments
	}
	if event.Output != "" {
		item["output"] = event.Output
	}
	if event.RawOutput != "" {
		item["raw_output"] = event.RawOutput
	}
	if event.Error != "" {
		item["error"] = event.Error
	}
	if event.DurationMS != 0 {
		item["duration_ms"] = event.DurationMS
	}
	if event.Message != nil {
		item["message"] = event.Message
	}
	if event.TurnUsage != nil {
		item["turn_usage"] = event.TurnUsage
	}
	if event.UsageSummary != nil {
		item["usage_summary"] = event.UsageSummary
	}
	if event.Title != "" {
		item["title"] = event.Title
	}
	if event.TitleStage != "" {
		item["title_stage"] = event.TitleStage
	}
	if event.Warning != "" {
		item["warning"] = event.Warning
	}
	if event.Branch != "" {
		item["branch"] = event.Branch
	}
	if event.Lifecycle != nil {
		item["lifecycle"] = event.Lifecycle
	}
	return item
}
