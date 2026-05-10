package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

const (
	managedHostSessionOpenPath        = "/v1/swarm/managed-hosts/sessions/open"
	managedHostSessionMessagePath     = "/v1/swarm/managed-hosts/sessions/message"
	managedHostSessionRunPath         = "/v1/swarm/managed-hosts/sessions/run"
	managedHostSessionStopPath        = "/v1/swarm/managed-hosts/sessions/stop"
	peerManagedHostSessionOpenPath    = "/v1/swarm/peer/managed-host-sessions/open"
	peerManagedHostSessionMessagePath = "/v1/swarm/peer/managed-host-sessions/message"
	peerManagedHostSessionRunPath     = "/v1/swarm/peer/managed-host-sessions/run"
	peerManagedHostSessionStopPath    = "/v1/swarm/peer/managed-host-sessions/stop"
	peerManagedHostSessionEventPath   = "/v1/swarm/peer/managed-host-sessions/event"
)

type managedHostSessionOpenRequest struct {
	TargetSwarmID        string         `json:"target_swarm_id"`
	Title                string         `json:"title"`
	WorkspacePath        string         `json:"workspace_path"`
	HostWorkspacePath    string         `json:"host_workspace_path"`
	RuntimeWorkspacePath string         `json:"runtime_workspace_path"`
	WorkspaceName        string         `json:"workspace_name"`
	Mode                 string         `json:"mode"`
	AgentName            string         `json:"agent_name"`
	Metadata             map[string]any `json:"metadata"`
	Preference           struct {
		Provider    string `json:"provider"`
		Model       string `json:"model"`
		Thinking    string `json:"thinking"`
		ServiceTier string `json:"service_tier,omitempty"`
		ContextMode string `json:"context_mode,omitempty"`
	} `json:"preference"`
}

type managedHostSessionMessageRequest struct {
	TargetSwarmID string         `json:"target_swarm_id,omitempty"`
	SessionID     string         `json:"session_id"`
	Role          string         `json:"role"`
	Content       string         `json:"content"`
	Metadata      map[string]any `json:"metadata"`
}

type managedHostSessionRunRequest struct {
	TargetSwarmID string `json:"target_swarm_id,omitempty"`
	SessionID     string `json:"session_id"`
	Type          string `json:"type"`
	runruntime.RunRequest
	RunID   string `json:"run_id,omitempty"`
	LastSeq uint64 `json:"last_seq,omitempty"`
}

type managedHostSessionRunAccepted struct {
	OK             bool   `json:"ok,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	RunID          string `json:"run_id,omitempty"`
	Status         string `json:"status,omitempty"`
	Background     bool   `json:"background,omitempty"`
	TargetKind     string `json:"target_kind,omitempty"`
	TargetName     string `json:"target_name,omitempty"`
	OwnerTransport string `json:"owner_transport,omitempty"`
}

type managedHostSessionEventRequest struct {
	SessionID     string         `json:"session_id"`
	EventType     string         `json:"event_type"`
	Payload       map[string]any `json:"payload"`
	CausationID   string         `json:"causation_id,omitempty"`
	CorrelationID string         `json:"correlation_id,omitempty"`
}

type peerManagedHostSessionOpenRequest struct {
	SessionID string                          `json:"session_id"`
	Request   managedHostSessionCreateRequest `json:"request"`
	Route     managedHostSessionRoute         `json:"route"`
}

type managedHostSessionCreateRequest struct {
	Title                string         `json:"title"`
	WorkspacePath        string         `json:"workspace_path"`
	HostWorkspacePath    string         `json:"host_workspace_path,omitempty"`
	RuntimeWorkspacePath string         `json:"runtime_workspace_path,omitempty"`
	WorkspaceName        string         `json:"workspace_name"`
	Mode                 string         `json:"mode"`
	AgentName            string         `json:"agent_name"`
	Metadata             map[string]any `json:"metadata"`
	Preference           struct {
		Provider    string `json:"provider"`
		Model       string `json:"model"`
		Thinking    string `json:"thinking"`
		ServiceTier string `json:"service_tier,omitempty"`
		ContextMode string `json:"context_mode,omitempty"`
	} `json:"preference"`
}

type managedHostSessionRoute struct {
	PrimarySwarmID        string `json:"primary_swarm_id"`
	PrimaryBackendURL     string `json:"primary_backend_url,omitempty"`
	ManagedHostSwarmID    string `json:"managed_host_swarm_id"`
	ManagedHostName       string `json:"managed_host_name,omitempty"`
	ManagedHostBackendURL string `json:"managed_host_backend_url,omitempty"`
	HostWorkspacePath     string `json:"host_workspace_path,omitempty"`
	RuntimeWorkspacePath  string `json:"runtime_workspace_path,omitempty"`
}

func (s *Server) handleManagedHostSessionOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("session service not configured"))
		return
	}
	var req managedHostSessionOpenRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	target, localSwarmID, _, status, err := s.resolveManagedHostSessionTarget(r, req.TargetSwarmID)
	if err != nil {
		writeError(w, status, err)
		return
	}
	sessionID := sessionruntime.NewSessionID()
	workspacePath := firstNonEmpty(strings.TrimSpace(req.HostWorkspacePath), strings.TrimSpace(req.WorkspacePath), strings.TrimSpace(req.RuntimeWorkspacePath))
	runtimeWorkspacePath := firstNonEmpty(strings.TrimSpace(req.RuntimeWorkspacePath), strings.TrimSpace(req.WorkspacePath), workspacePath)
	if runtimeWorkspacePath == "" {
		writeError(w, http.StatusBadRequest, errors.New("workspace_path is required"))
		return
	}
	workspaceName := firstNonEmpty(strings.TrimSpace(req.WorkspaceName), filepath.Base(runtimeWorkspacePath))
	route := managedHostSessionRoute{
		PrimarySwarmID:        localSwarmID,
		PrimaryBackendURL:     hostedSessionHostBackendURLFromServer(s),
		ManagedHostSwarmID:    strings.TrimSpace(target.SwarmID),
		ManagedHostName:       firstNonEmpty(strings.TrimSpace(target.Name), strings.TrimSpace(target.SwarmID)),
		ManagedHostBackendURL: strings.TrimSpace(target.BackendURL),
		HostWorkspacePath:     workspacePath,
		RuntimeWorkspacePath:  runtimeWorkspacePath,
	}
	peerReq := peerManagedHostSessionOpenRequest{
		SessionID: sessionID,
		Request: managedHostSessionCreateRequest{
			Title:                req.Title,
			WorkspacePath:        runtimeWorkspacePath,
			HostWorkspacePath:    runtimeWorkspacePath,
			RuntimeWorkspacePath: runtimeWorkspacePath,
			WorkspaceName:        workspaceName,
			Mode:                 req.Mode,
			AgentName:            req.AgentName,
			Metadata:             managedHostSessionMetadata(req.Metadata, route),
			Preference:           req.Preference,
		},
		Route: route,
	}
	var peerResp struct {
		OK      bool                        `json:"ok"`
		Session pebblestore.SessionSnapshot `json:"session"`
		Warning string                      `json:"warning,omitempty"`
	}
	if err := s.postPeerJSONToSwarmTarget(r.Context(), *target, peerManagedHostSessionOpenPath, peerReq, &peerResp); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	mirror := peerResp.Session
	if strings.TrimSpace(mirror.ID) == "" {
		mirror.ID = sessionID
	}
	mirror.Metadata = managedHostSessionMetadata(mirror.Metadata, route)
	mirrored, event, err := s.sessions.StoreMirroredSessionWithEvent(mirror)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": mirrored, "target": managedHostSessionTargetResponse(*target), "warning": strings.TrimSpace(peerResp.Warning)})
}

func (s *Server) handleManagedHostSessionMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("session service not configured"))
		return
	}
	var req managedHostSessionMessageRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.SessionID) == "" {
		writeError(w, http.StatusBadRequest, errors.New("session_id is required"))
		return
	}
	targetSwarmID := strings.TrimSpace(req.TargetSwarmID)
	if targetSwarmID == "" {
		session, ok, err := s.sessions.GetSession(req.SessionID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !ok {
			writeError(w, http.StatusBadRequest, errors.New("managed host mirrored session was not found"))
			return
		}
		targetSwarmID = managedHostSessionStringMetadata(session.Metadata, "swarm_managed_host_swarm_id")
	}
	target, _, _, status, err := s.resolveManagedHostSessionTarget(r, targetSwarmID)
	if err != nil {
		writeError(w, status, err)
		return
	}
	var peerResp struct {
		OK      bool                        `json:"ok"`
		Message pebblestore.MessageSnapshot `json:"message"`
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := s.postPeerJSONToSwarmTarget(r.Context(), *target, peerManagedHostSessionMessagePath, req, &peerResp); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	mirrored, err := s.sessions.StoreMirroredMessage(peerResp.Session, peerResp.Message)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": peerResp.Message, "session": mirrored})
}

func (s *Server) handleManagedHostSessionRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("session service not configured"))
		return
	}
	var req managedHostSessionRunRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.SessionID) == "" {
		writeError(w, http.StatusBadRequest, errors.New("session_id is required"))
		return
	}
	targetSwarmID := strings.TrimSpace(req.TargetSwarmID)
	if targetSwarmID == "" {
		session, ok, err := s.sessions.GetSession(req.SessionID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !ok {
			writeError(w, http.StatusBadRequest, errors.New("managed host mirrored session was not found"))
			return
		}
		targetSwarmID = managedHostSessionStringMetadata(session.Metadata, "swarm_managed_host_swarm_id")
	}
	target, _, _, status, err := s.resolveManagedHostSessionTarget(r, targetSwarmID)
	if err != nil {
		writeError(w, status, err)
		return
	}
	var peerResp managedHostSessionRunAccepted
	if err := s.postPeerJSONToSwarmTarget(r.Context(), *target, peerManagedHostSessionRunPath, req, &peerResp); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusAccepted, peerResp)
}

func (s *Server) handleManagedHostSessionStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req managedHostSessionRunRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.SessionID) == "" {
		writeError(w, http.StatusBadRequest, errors.New("session_id is required"))
		return
	}
	targetSwarmID := strings.TrimSpace(req.TargetSwarmID)
	if targetSwarmID == "" {
		session, ok, err := s.sessions.GetSession(req.SessionID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !ok {
			writeError(w, http.StatusBadRequest, errors.New("managed host mirrored session was not found"))
			return
		}
		targetSwarmID = managedHostSessionStringMetadata(session.Metadata, "swarm_managed_host_swarm_id")
	}
	target, _, _, status, err := s.resolveManagedHostSessionTarget(r, targetSwarmID)
	if err != nil {
		writeError(w, status, err)
		return
	}
	var peerResp map[string]any
	if err := s.postPeerJSONToSwarmTarget(r.Context(), *target, peerManagedHostSessionStopPath, req, &peerResp); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, peerResp)
}

func (s *Server) handlePeerManagedHostSessionOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.requirePeerAuth(w, r) {
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("session service not configured"))
		return
	}
	var req peerManagedHostSessionOpenRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	runtimeWorkspacePath := firstNonEmpty(strings.TrimSpace(req.Request.RuntimeWorkspacePath), strings.TrimSpace(req.Request.WorkspacePath), strings.TrimSpace(req.Route.RuntimeWorkspacePath))
	hostWorkspacePath := firstNonEmpty(strings.TrimSpace(req.Request.HostWorkspacePath), runtimeWorkspacePath, strings.TrimSpace(req.Route.HostWorkspacePath))
	childReq := sessionCreateRequest{
		Title:                req.Request.Title,
		WorkspacePath:        runtimeWorkspacePath,
		HostWorkspacePath:    hostWorkspacePath,
		RuntimeWorkspacePath: runtimeWorkspacePath,
		WorkspaceName:        req.Request.WorkspaceName,
		Mode:                 req.Request.Mode,
		AgentName:            req.Request.AgentName,
		Metadata:             managedHostSessionMetadata(req.Request.Metadata, req.Route),
		Preference:           req.Request.Preference,
	}
	session, event, warning, modeWarning, err := s.createSessionFromRequestWithSessionID(childReq, nil, true, req.SessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": session, "warning": strings.TrimSpace(strings.Join([]string{warning, modeWarning}, " "))})
}

func (s *Server) handlePeerManagedHostSessionMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.requirePeerAuth(w, r) {
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("session service not configured"))
		return
	}
	var req managedHostSessionMessageRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	message, session, event, err := s.sessions.AppendMessage(req.SessionID, req.Role, req.Content, req.Metadata)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": message, "session": session})
}

func (s *Server) handlePeerManagedHostSessionRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.requirePeerAuth(w, r) {
		return
	}
	if s.runner == nil || s.runStreams == nil {
		writeError(w, http.StatusInternalServerError, errors.New("run service not configured"))
		return
	}
	var req managedHostSessionRunRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.SessionID) == "" {
		writeError(w, http.StatusBadRequest, errors.New("session_id is required"))
		return
	}
	req.RunRequest = req.RunRequest.Normalized()
	if req.RunRequest.Prompt == "" && !req.RunRequest.Compact {
		writeError(w, http.StatusBadRequest, errors.New("prompt is required"))
		return
	}
	if normalized := runruntime.NormalizeRunTargetKind(req.RunRequest.TargetKind); req.RunRequest.TargetKind != "" && normalized == "" {
		writeError(w, http.StatusBadRequest, errors.New("unsupported target_kind"))
		return
	}
	state, err := s.runStreams.newRun(req.SessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	started := s.startManagedHostRunStreamExecution(state.runID, req.SessionID, req.RunRequest)
	if startErr := <-started; startErr != nil {
		writeError(w, http.StatusBadRequest, startErr)
		return
	}
	writeJSON(w, http.StatusAccepted, managedHostSessionRunAccepted{OK: true, SessionID: req.SessionID, RunID: state.runID, Status: "accepted", Background: req.RunRequest.Background, TargetKind: strings.TrimSpace(req.RunRequest.TargetKind), TargetName: strings.TrimSpace(req.RunRequest.TargetName), OwnerTransport: "managed_host_peer"})
}

func (s *Server) handlePeerManagedHostSessionStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.requirePeerAuth(w, r) {
		return
	}
	var req managedHostSessionRunRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.SessionID) == "" || strings.TrimSpace(req.RunID) == "" {
		writeError(w, http.StatusBadRequest, errors.New("session_id and run_id are required"))
		return
	}
	s.runStreams.setStopReason(req.RunID, "run stopped by user")
	if err := s.runner.StopSessionRun(req.SessionID, req.RunID, "run stopped by user"); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": req.SessionID, "run_id": req.RunID})
}

func (s *Server) handlePeerManagedHostSessionEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.requirePeerAuth(w, r) {
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("session service not configured"))
		return
	}
	var req managedHostSessionEventRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	env, err := s.sessions.StoreMirroredEvent(req.SessionID, req.EventType, req.Payload, req.CausationID, req.CorrelationID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.storeMirroredEventPayloadLifecycle(req.SessionID, req.Payload); err != nil {
		log.Printf("warning: store managed-host mirrored event lifecycle failed session_id=%q event_type=%q: %v", strings.TrimSpace(req.SessionID), strings.TrimSpace(req.EventType), err)
	}
	if err := s.storeMirroredEventPayloadMessage(req.SessionID, req.Payload); err != nil {
		log.Printf("warning: store managed-host mirrored event message failed session_id=%q event_type=%q: %v", strings.TrimSpace(req.SessionID), strings.TrimSpace(req.EventType), err)
	}
	if err := s.publishManagedHostSessionEventToPrimaryRunStream(req.SessionID, req.EventType, req.Payload); err != nil {
		log.Printf("warning: publish managed-host mirrored event to run stream failed session_id=%q event_type=%q: %v", strings.TrimSpace(req.SessionID), strings.TrimSpace(req.EventType), err)
	}
	if s.hub != nil {
		s.hub.Publish(env)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "event": env})
}

func (s *Server) publishManagedHostSessionEventToPrimaryRunStream(sessionID, eventType string, payload map[string]any) error {
	if s == nil || s.runStreams == nil || len(payload) == 0 {
		return nil
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var msg runStreamWireEvent
	if err := json.Unmarshal(encoded, &msg); err != nil {
		return err
	}
	if strings.TrimSpace(msg.Type) == "" {
		msg.Type = strings.TrimSpace(eventType)
	}
	if strings.TrimSpace(msg.Type) == "" {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if strings.TrimSpace(msg.SessionID) == "" {
		msg.SessionID = sessionID
	}
	if sessionID == "" || !strings.EqualFold(strings.TrimSpace(msg.SessionID), sessionID) {
		return nil
	}
	runID := strings.TrimSpace(msg.RunID)
	if runID == "" {
		return nil
	}
	state, err := s.runStreams.ensureRunWithID(sessionID, runID)
	if err != nil {
		return err
	}
	if state == nil {
		return nil
	}
	s.runStreams.publish(runID, msg)
	return nil
}

func (s *Server) startManagedHostRunStreamExecution(runID, sessionID string, request runruntime.RunRequest) <-chan error {
	started := make(chan error, 1)
	if s == nil || s.runner == nil || s.runStreams == nil {
		started <- errors.New("run service is not configured")
		return started
	}
	s.beginActiveRun()
	go func() {
		defer s.endActiveRun()
		defer close(started)
		startSignaled := false
		result, err := s.runner.RunTurnStreaming(s.runCtx, sessionID, request, runruntime.RunStartMeta{RunID: runID, OwnerTransport: "managed_host_peer"}, func(event runruntime.StreamEvent) {
			if !startSignaled && strings.EqualFold(strings.TrimSpace(event.Type), runruntime.StreamEventSessionLifecycle) && event.Lifecycle != nil && event.Lifecycle.Active {
				startSignaled = true
				select {
				case started <- nil:
				default:
				}
			}
			s.runStreams.publishRuntimeEvent(runID, event)
			if publishErr := s.publishManagedHostSessionEventToPrimary(event); publishErr != nil {
				log.Printf("warning: publish managed-host run event to primary failed session_id=%q run_id=%q event_type=%q: %v", strings.TrimSpace(sessionID), strings.TrimSpace(runID), strings.TrimSpace(event.Type), publishErr)
			}
		})
		if err != nil {
			if !startSignaled {
				select {
				case started <- err:
				default:
				}
			}
			s.runStreams.publishError(runID, sessionID, err)
			return
		}
		if !startSignaled {
			select {
			case started <- nil:
			default:
			}
		}
		for _, event := range result.Events {
			if s.hub != nil {
				s.hub.Publish(event)
			}
		}
		streamResult := result
		streamResult.ToolMessages = nil
		s.runStreams.publishCompleted(runID, sessionID, streamResult)
	}()
	return started
}

func (s *Server) publishManagedHostSessionEventToPrimary(event runruntime.StreamEvent) error {
	if s == nil || s.sessions == nil || event.SessionID == "" || event.Type == "" {
		return nil
	}
	session, ok, err := s.sessions.GetSession(event.SessionID)
	if err != nil || !ok {
		return err
	}
	primarySwarmID := managedHostSessionStringMetadata(session.Metadata, "swarm_managed_host_primary_swarm_id")
	primaryBackendURL := managedHostSessionStringMetadata(session.Metadata, "swarm_managed_host_primary_backend_url")
	if primarySwarmID == "" || primaryBackendURL == "" {
		return nil
	}
	target := swarmTarget{SwarmID: primarySwarmID, BackendURL: primaryBackendURL}
	payloadBytes, err := json.Marshal(runStreamWireEvent{Type: event.Type, SessionID: event.SessionID, RunID: event.RunID, Agent: event.Agent, Step: event.Step, Delta: event.Delta, Summary: event.Summary, ToolName: event.ToolName, CallID: event.CallID, Arguments: event.Arguments, Output: event.Output, RawOutput: event.RawOutput, Error: event.Error, DurationMS: event.DurationMS, Message: event.Message, Permission: event.Permission, TurnUsage: event.TurnUsage, UsageSummary: event.UsageSummary, Title: event.Title, TitleStage: event.TitleStage, Warning: event.Warning, Lifecycle: event.Lifecycle})
	if err != nil {
		return err
	}
	var payload map[string]any
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return err
	}
	return s.postPeerJSONToSwarmTarget(s.runCtx, target, peerManagedHostSessionEventPath, managedHostSessionEventRequest{SessionID: event.SessionID, EventType: event.Type, Payload: payload, CausationID: event.RunID}, nil)
}

func (s *Server) resolveManagedHostSessionTarget(r *http.Request, targetSwarmID string) (*swarmTarget, string, string, int, error) {
	if s == nil || s.swarm == nil {
		return nil, "", "", http.StatusInternalServerError, errors.New("swarm service is not configured")
	}
	targetSwarmID = strings.TrimSpace(targetSwarmID)
	if targetSwarmID == "" {
		return nil, "", "", http.StatusBadRequest, errors.New("target_swarm_id is required")
	}
	targets, _, err := s.swarmTargetsForRequestWithOptions(requestWithSwarmTargetQuery(r, targetSwarmID), true)
	if err != nil {
		return nil, "", "", http.StatusBadRequest, err
	}
	var target *swarmTarget
	for i := range targets {
		if strings.EqualFold(strings.TrimSpace(targets[i].SwarmID), targetSwarmID) {
			targetCopy := targets[i]
			target = &targetCopy
			break
		}
	}
	if target == nil {
		return nil, "", "", http.StatusBadRequest, errors.New("managed host target was not found")
	}
	if !strings.EqualFold(strings.TrimSpace(target.Relationship), swarmruntime.RelationshipManaged) || strings.EqualFold(strings.TrimSpace(target.Kind), "manager") || strings.EqualFold(strings.TrimSpace(target.Relationship), "self") {
		return nil, "", "", http.StatusBadRequest, errors.New("target must be a managed host")
	}
	if strings.TrimSpace(target.BackendURL) == "" {
		return nil, "", "", http.StatusBadRequest, errors.New("managed host route is missing")
	}
	if !target.Selectable {
		return nil, "", "", http.StatusBadRequest, errors.New("managed host is not selectable")
	}
	ctx, cancel := context.WithTimeout(r.Context(), swarmTargetHealthTimeout)
	defer cancel()
	if !probeSwarmTargetBackend(ctx, target.BackendURL) {
		return nil, "", "", http.StatusBadGateway, errors.New("managed host route is not reachable")
	}
	peerToken, err := s.outgoingPeerAuthTokenForTarget(r, *target)
	if err != nil {
		return nil, "", "", http.StatusBadRequest, err
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return nil, "", "", http.StatusInternalServerError, err
	}
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		return nil, "", "", http.StatusInternalServerError, err
	}
	localSwarmID := strings.TrimSpace(state.Node.SwarmID)
	if localSwarmID == "" {
		return nil, "", "", http.StatusInternalServerError, errors.New("local swarm id is not configured")
	}
	return target, localSwarmID, peerToken, http.StatusOK, nil
}

func managedHostSessionStringMetadata(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, ok := metadata[key]
	if !ok {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func managedHostSessionMetadata(metadata map[string]any, route managedHostSessionRoute) map[string]any {
	merged := mergeSessionCreateMetadata(metadata, map[string]any{
		"swarm_managed_host_session":                true,
		"swarm_managed_host_primary_swarm_id":       strings.TrimSpace(route.PrimarySwarmID),
		"swarm_managed_host_primary_backend_url":    strings.TrimSpace(route.PrimaryBackendURL),
		"swarm_managed_host_swarm_id":               strings.TrimSpace(route.ManagedHostSwarmID),
		"swarm_managed_host_name":                   strings.TrimSpace(route.ManagedHostName),
		"swarm_managed_host_backend_url":            strings.TrimSpace(route.ManagedHostBackendURL),
		"swarm_managed_host_host_workspace_path":    strings.TrimSpace(route.HostWorkspacePath),
		"swarm_managed_host_runtime_workspace_path": strings.TrimSpace(route.RuntimeWorkspacePath),
	})
	for key, value := range merged {
		if strings.TrimSpace(key) == "" || value == "" {
			delete(merged, key)
		}
	}
	return merged
}

func managedHostSessionTargetResponse(target swarmTarget) map[string]any {
	return map[string]any{
		"swarm_id": strings.TrimSpace(target.SwarmID),
		"name":     firstNonEmpty(strings.TrimSpace(target.Name), strings.TrimSpace(target.SwarmID)),
		"online":   target.Online,
	}
}

func hostedSessionHostBackendURLFromServer(s *Server) string {
	if s == nil {
		return ""
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return ""
	}
	return hostedSessionHostBackendURL(cfg)
}
