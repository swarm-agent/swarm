package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	gorillaws "github.com/gorilla/websocket"
	"swarm/packages/swarmd/internal/identity"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	transportws "swarm/packages/swarmd/internal/transport/ws"
)

const (
	managedHostSessionOpenPath            = "/v1/swarm/managed-hosts/sessions/open"
	managedHostSessionMessagePath         = "/v1/swarm/managed-hosts/sessions/message"
	managedHostSessionRunPath             = "/v1/swarm/managed-hosts/sessions/run"
	managedHostSessionStopPath            = "/v1/swarm/managed-hosts/sessions/stop"
	managedHostWorkspaceGitCommitPath     = "/v1/swarm/managed-hosts/workspace/git/commit"
	peerManagedHostSessionOpenPath        = "/v1/swarm/peer/managed-host-sessions/open"
	peerManagedHostSessionMessagePath     = "/v1/swarm/peer/managed-host-sessions/message"
	peerManagedHostSessionRunPath         = "/v1/swarm/peer/managed-host-sessions/run"
	peerManagedHostSessionRunStreamPath   = "/v1/swarm/peer/managed-host-sessions/run/stream"
	peerManagedHostWorkspaceGitCommitPath = "/v1/workspace/git/commit"
	peerManagedHostSessionStopPath        = "/v1/swarm/peer/managed-host-sessions/stop"
	peerManagedHostSessionEventPath       = "/v1/swarm/peer/managed-host-sessions/event"
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
	WorktreeMode         string         `json:"worktree_mode,omitempty"`
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
	WorktreeMode         string         `json:"worktree_mode,omitempty"`
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
	UserID                string `json:"user_id,omitempty"`
	AccountScopeID        string `json:"account_scope_id,omitempty"`
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
	principal, principalOK := PrincipalFromRequest(r)
	if !principalOK || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
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
		UserID:                strings.TrimSpace(principal.UserID),
		AccountScopeID:        strings.TrimSpace(principal.AccountScopeID),
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
			WorktreeMode:         req.WorktreeMode,
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
	if strings.TrimSpace(mirror.WorkspacePath) != "" {
		route.RuntimeWorkspacePath = strings.TrimSpace(mirror.WorkspacePath)
	}
	if err := requireManagedHostSessionMirrorOwnership(&mirror, principal); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	mirror.Metadata = managedHostSessionMetadata(mirror.Metadata, route)
	routeRecord := pebblestore.SessionRouteRecord{
		SessionID:            strings.TrimSpace(mirror.ID),
		UserID:               strings.TrimSpace(principal.UserID),
		AccountScopeID:       strings.TrimSpace(principal.AccountScopeID),
		ChildSwarmID:         strings.TrimSpace(route.ManagedHostSwarmID),
		ChildBackendURL:      strings.TrimSpace(route.ManagedHostBackendURL),
		HostWorkspacePath:    strings.TrimSpace(route.HostWorkspacePath),
		RuntimeWorkspacePath: strings.TrimSpace(route.RuntimeWorkspacePath),
		CreatedAt:            mirror.CreatedAt,
		UpdatedAt:            mirror.UpdatedAt,
	}
	if err := s.upsertTopologySessionRoute(routeRecord); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	mirrored, event, err := s.sessions.StoreMirroredSessionWithEvent(mirror)
	if err != nil {
		if cleanupErr := s.rollbackHostedSessionCreate(routeRecord.SessionID); cleanupErr != nil {
			log.Printf("managed-host session route rollback failed session_id=%q err=%v", routeRecord.SessionID, cleanupErr)
		}
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
	target, status, err := s.managedHostTargetForSessionRequest(r, req.SessionID, req.TargetSwarmID)
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
	target, status, err := s.managedHostTargetForSessionRequest(r, req.SessionID, req.TargetSwarmID)
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
	target, status, err := s.managedHostTargetForSessionRequest(r, req.SessionID, req.TargetSwarmID)
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
	principal, ok := s.verifiedPeerManagedHostSessionOpenPrincipal(r, req)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
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
		WorktreeMode:         req.Request.WorktreeMode,
		Metadata:             managedHostSessionMetadata(req.Request.Metadata, req.Route),
		Preference:           req.Request.Preference,
	}
	session, event, warning, modeWarning, err := s.createSessionFromRequestWithSessionID(childReq, nil, true, req.SessionID, principal, true)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	routeRecord := pebblestore.SessionRouteRecord{
		SessionID:            strings.TrimSpace(session.ID),
		UserID:               strings.TrimSpace(principal.UserID),
		AccountScopeID:       strings.TrimSpace(principal.AccountScopeID),
		ChildSwarmID:         strings.TrimSpace(req.Route.ManagedHostSwarmID),
		ChildBackendURL:      strings.TrimSpace(req.Route.ManagedHostBackendURL),
		HostSwarmID:          strings.TrimSpace(req.Route.PrimarySwarmID),
		HostWorkspacePath:    strings.TrimSpace(req.Route.HostWorkspacePath),
		RuntimeWorkspacePath: firstNonEmpty(strings.TrimSpace(session.WorkspacePath), runtimeWorkspacePath),
		CreatedAt:            session.CreatedAt,
		UpdatedAt:            session.UpdatedAt,
	}
	if s.sessionRoutes == nil {
		if cleanupErr := s.sessions.DeleteSession(session.ID); cleanupErr != nil {
			log.Printf("managed-host peer session route rollback failed session_id=%q err=%v", session.ID, cleanupErr)
		}
		writeError(w, http.StatusInternalServerError, errors.New("session route store not configured"))
		return
	}
	if _, err := s.sessionRoutes.Put(routeRecord); err != nil {
		if cleanupErr := s.sessions.DeleteSession(session.ID); cleanupErr != nil {
			log.Printf("managed-host peer session route rollback failed session_id=%q err=%v", session.ID, cleanupErr)
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if strings.TrimSpace(routeRecord.ChildBackendURL) != "" {
		if err := s.upsertTopologySessionRoute(routeRecord); err != nil {
			if cleanupErr := s.rollbackHostedSessionCreate(session.ID); cleanupErr != nil {
				log.Printf("managed-host peer session route rollback failed session_id=%q err=%v", session.ID, cleanupErr)
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}
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
	r = s.requestWithTrustedSessionPrincipal(r, req.SessionID)
	if _, _, ok := s.verifySessionOwnershipForRequest(w, r, req.SessionID); !ok {
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
	r = s.requestWithTrustedSessionPrincipal(r, req.SessionID)
	if _, _, ok := s.verifySessionOwnershipForRequest(w, r, req.SessionID); !ok {
		return
	}
	accepted, status, err := s.startPeerManagedHostSessionRun(req)
	if err != nil {
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusAccepted, accepted)
}

func (s *Server) handleManagedHostSessionRunStreamWebsocket(w http.ResponseWriter, r *http.Request, sessionID string) {
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("session service not configured"))
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, errors.New("session id is required"))
		return
	}
	target, status, err := s.managedHostTargetForSessionRequest(r, sessionID, r.URL.Query().Get("target_swarm_id"))
	if err != nil {
		writeError(w, status, err)
		return
	}
	if err := s.proxyManagedHostRunStreamWebsocket(w, r, *target, sessionID); err != nil {
		if errors.Is(err, transportws.ErrUpgradeRequired) {
			writeError(w, http.StatusUpgradeRequired, errors.New("websocket upgrade required"))
			return
		}
		writeError(w, http.StatusBadGateway, err)
	}
}

func (s *Server) handleManagedHostSessionRunStreamControl(w http.ResponseWriter, r *http.Request, sessionID string) {
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("session service not configured"))
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, errors.New("session id is required"))
		return
	}
	target, status, err := s.managedHostTargetForSessionRequest(r, sessionID, r.URL.Query().Get("target_swarm_id"))
	if err != nil {
		writeError(w, status, err)
		return
	}
	var inbound runStreamInboundMessage
	if err := decodeJSON(r, &inbound); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	inbound.SessionID = sessionID
	var peerResp managedHostSessionRunAccepted
	if err := s.postPeerJSONToSwarmTarget(r.Context(), *target, peerManagedHostSessionRunStreamPath, inbound, &peerResp); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusAccepted, peerResp)
}

func (s *Server) proxyManagedHostRunStreamWebsocket(w http.ResponseWriter, r *http.Request, target swarmTarget, sessionID string) error {
	if !isWebsocketUpgradeRequest(r) {
		return transportws.ErrUpgradeRequired
	}
	if s.swarm == nil {
		return errors.New("swarm service not configured")
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return err
	}
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		return err
	}
	peerToken, err := s.outgoingPeerAuthTokenForTarget(r, target)
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(strings.TrimSpace(target.BackendURL), "/") + peerManagedHostSessionRunStreamPath
	wsEndpoint, err := websocketEndpointForBackend(endpoint)
	if err != nil {
		return err
	}
	headers := cloneHeaderForUpstreamWebsocket(r.Header)
	headers.Set(peerAuthSwarmIDHeader, strings.TrimSpace(state.Node.SwarmID))
	headers.Set(peerAuthTokenHeader, peerToken)
	downstream, err := transportws.Accept(w, r)
	if err != nil {
		return err
	}
	defer downstream.Close()
	upstream, resp, err := gorillaws.DefaultDialer.DialContext(r.Context(), wsEndpoint, headers)
	if err != nil {
		s.sendRunStreamControl(downstream, runStreamControlMessage{Type: "error", OK: false, Error: summarizeWebsocketDialError(err, resp).Error()})
		return nil
	}
	defer upstream.Close()
	first, err := downstream.ReadText()
	if err != nil {
		return err
	}
	patched, err := managedHostRunStreamStartPayloadWithSession(first, sessionID)
	if err != nil {
		return err
	}
	if err := upstream.WriteMessage(gorillaws.TextMessage, patched); err != nil {
		return err
	}
	bridgeWebsocketTextWithUpstreamObserver(downstream, upstream, func(payload []byte) {
		s.mirrorManagedHostRunStreamFrame(sessionID, payload)
	})
	return nil
}

func (s *Server) mirrorManagedHostRunStreamFrame(sessionID string, payload []byte) {
	if s == nil || s.sessions == nil || len(payload) == 0 {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	var msg runStreamWireEvent
	if err := json.Unmarshal(payload, &msg); err != nil {
		return
	}
	eventType := strings.TrimSpace(msg.Type)
	if eventType == "" {
		return
	}
	if strings.TrimSpace(msg.SessionID) == "" {
		msg.SessionID = sessionID
	}
	if !strings.EqualFold(strings.TrimSpace(msg.SessionID), sessionID) {
		log.Printf("warning: ignore managed-host run stream frame for mismatched session_id=%q expected=%q event_type=%q", strings.TrimSpace(msg.SessionID), sessionID, eventType)
		return
	}
	encoded, err := json.Marshal(msg)
	if err != nil {
		log.Printf("warning: marshal managed-host run stream frame failed session_id=%q event_type=%q: %v", sessionID, eventType, err)
		return
	}
	var payloadMap map[string]any
	if err := json.Unmarshal(encoded, &payloadMap); err != nil {
		log.Printf("warning: decode managed-host run stream frame payload failed session_id=%q event_type=%q: %v", sessionID, eventType, err)
		return
	}
	env, err := s.sessions.StoreMirroredEvent(sessionID, eventType, payloadMap, strings.TrimSpace(msg.RunID), "")
	if err != nil {
		log.Printf("warning: store managed-host run stream event failed session_id=%q event_type=%q: %v", sessionID, eventType, err)
	} else if env.GlobalSeq != 0 && s.hub != nil {
		s.hub.Publish(env)
	}
	if err := s.storeMirroredEventPayloadLifecycle(sessionID, payloadMap); err != nil {
		log.Printf("warning: store managed-host run stream lifecycle failed session_id=%q event_type=%q: %v", sessionID, eventType, err)
	}
	if err := s.storeMirroredEventPayloadMessage(sessionID, payloadMap); err != nil {
		log.Printf("warning: store managed-host run stream message failed session_id=%q event_type=%q: %v", sessionID, eventType, err)
	}
	if err := s.storeMirroredEventPayloadTitle(sessionID, eventType, payloadMap); err != nil {
		log.Printf("warning: store managed-host run stream title failed session_id=%q event_type=%q: %v", sessionID, eventType, err)
	}
	if err := s.storeMirroredEventPayloadPermission(sessionID, payloadMap); err != nil {
		log.Printf("warning: store managed-host run stream permission failed session_id=%q event_type=%q: %v", sessionID, eventType, err)
	}
	if err := s.publishManagedHostSessionEventToPrimaryRunStream(sessionID, eventType, payloadMap); err != nil {
		log.Printf("warning: publish managed-host run stream event to local replay failed session_id=%q event_type=%q: %v", sessionID, eventType, err)
	}
}

func (s *Server) storeMirroredEventPayloadTitle(sessionID, eventType string, payload map[string]any) error {
	if s == nil || s.sessions == nil || len(payload) == 0 {
		return nil
	}
	eventType = strings.TrimSpace(eventType)
	if !isManagedHostSessionTitleEvent(eventType) {
		return nil
	}
	var envelope struct {
		Type       string `json:"type"`
		SessionID  string `json:"session_id"`
		Title      string `json:"title"`
		TitleStage string `json:"title_stage"`
		UpdatedAt  int64  `json:"updated_at"`
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	if eventType == "" {
		eventType = strings.TrimSpace(envelope.Type)
	}
	if !isManagedHostSessionTitleEvent(eventType) {
		return nil
	}
	title := strings.TrimSpace(envelope.Title)
	if title == "" {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if strings.TrimSpace(envelope.SessionID) == "" {
		envelope.SessionID = sessionID
	}
	if sessionID == "" || !strings.EqualFold(strings.TrimSpace(envelope.SessionID), sessionID) {
		return nil
	}
	_, err = s.sessions.StoreMirroredTitle(sessionID, title, envelope.UpdatedAt)
	return err
}

func isManagedHostSessionTitleEvent(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case runruntime.StreamEventSessionTitle, "run.session.title.updated":
		return true
	default:
		return false
	}
}

func managedHostRunStreamStartPayloadWithSession(raw []byte, sessionID string) ([]byte, error) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	if strings.TrimSpace(sessionID) != "" {
		payload["session_id"] = strings.TrimSpace(sessionID)
	}
	return json.Marshal(payload)
}

func (s *Server) handlePeerManagedHostSessionRunStream(w http.ResponseWriter, r *http.Request) {
	if !s.requirePeerAuth(w, r) {
		return
	}
	if r.Method == http.MethodPost {
		raw, err := readRequestBody(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		var control managedHostPermissionControlRequest
		if err := json.Unmarshal(raw, &control); err == nil && isManagedHostPermissionControlType(control.Type) {
			if strings.TrimSpace(control.SessionID) == "" {
				writeError(w, http.StatusBadRequest, errors.New("session_id is required"))
				return
			}
			response := s.applyManagedHostPermissionControl(r.Context(), control)
			status := http.StatusOK
			if !response.OK {
				status = http.StatusBadRequest
			}
			writeJSON(w, status, response)
			return
		}
		inbound, err := decodeRunStreamInbound(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		sessionID := strings.TrimSpace(inbound.SessionID)
		if sessionID == "" {
			writeError(w, http.StatusBadRequest, errors.New("session_id is required"))
			return
		}
		if s.runner == nil || s.runStreams == nil {
			writeError(w, http.StatusInternalServerError, errors.New("run service not configured"))
			return
		}
		r = s.requestWithTrustedSessionPrincipal(r, sessionID)
		if _, _, ok := s.verifySessionOwnershipForRequest(w, r, sessionID); !ok {
			return
		}
		switch strings.ToLower(strings.TrimSpace(inbound.Type)) {
		case "run.start", "start":
			accepted, status, err := s.startPeerManagedHostSessionRun(managedHostSessionRunRequest{SessionID: sessionID, RunRequest: inbound.RunRequest})
			if err != nil {
				writeError(w, status, err)
				return
			}
			writeJSON(w, http.StatusAccepted, accepted)
		case "run.stop", "stop":
			if strings.TrimSpace(inbound.RunID) == "" {
				writeError(w, http.StatusBadRequest, errors.New("run_id is required for stop"))
				return
			}
			s.runStreams.setStopReason(inbound.RunID, "run stopped by user")
			if err := s.runner.StopSessionRun(sessionID, inbound.RunID, "run stopped by user"); err != nil {
				writeError(w, http.StatusConflict, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "run_id": inbound.RunID})
		default:
			writeError(w, http.StatusBadRequest, fmt.Errorf("unsupported run stream control type %q", strings.TrimSpace(inbound.Type)))
		}
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusUpgradeRequired, errors.New("run stream requires websocket upgrade (GET) or control POST"))
		return
	}
	if s.runner == nil || s.runStreams == nil {
		writeError(w, http.StatusInternalServerError, errors.New("run service not configured"))
		return
	}
	conn, err := transportws.Accept(w, r)
	if err != nil {
		if errors.Is(err, transportws.ErrUpgradeRequired) {
			writeError(w, http.StatusUpgradeRequired, errors.New("websocket upgrade required"))
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	defer conn.Close()
	raw, err := conn.ReadText()
	if err != nil {
		return
	}
	inbound, err := decodeRunStreamInbound(raw)
	if err != nil {
		s.sendRunStreamControl(conn, runStreamControlMessage{Type: "error", OK: false, Error: err.Error()})
		return
	}
	sessionID := strings.TrimSpace(inbound.SessionID)
	if sessionID == "" {
		s.sendRunStreamControl(conn, runStreamControlMessage{Type: "error", OK: false, Error: "session_id is required"})
		return
	}
	r = s.requestWithTrustedSessionPrincipal(r, sessionID)
	if _, _, ok := s.verifySessionOwnershipForRequest(w, r, sessionID); !ok {
		s.sendRunStreamControl(conn, runStreamControlMessage{Type: "error", OK: false, SessionID: sessionID, Error: "session not found"})
		return
	}
	if isManagedHostPermissionControlType(inbound.Type) {
		var control managedHostPermissionControlRequest
		if err := json.Unmarshal(raw, &control); err != nil {
			s.sendRunStreamControl(conn, runStreamControlMessage{Type: "error", OK: false, Error: err.Error()})
			return
		}
		if strings.TrimSpace(control.SessionID) == "" {
			control.SessionID = sessionID
		}
		response := s.applyManagedHostPermissionControl(r.Context(), control)
		rawResponse, err := json.Marshal(response)
		if err != nil {
			s.sendRunStreamControl(conn, runStreamControlMessage{Type: "error", OK: false, SessionID: sessionID, Error: err.Error()})
			return
		}
		_ = conn.WriteText(rawResponse)
		return
	}
	switch inbound.Type {
	case "run.start", "start":
		s.handlePeerManagedHostRunStreamStart(conn, sessionID, inbound)
	case "run.resume", "resume":
		s.handleRunStreamResume(conn, sessionID, inbound)
	case "run.stop", "stop":
		s.handleRunStreamStop(conn, sessionID, inbound)
	default:
		s.sendRunStreamControl(conn, runStreamControlMessage{Type: "error", OK: false, SessionID: sessionID, Error: fmt.Sprintf("unsupported run stream message type %q", inbound.Type)})
	}
}

func (s *Server) handlePeerManagedHostRunStreamStart(conn *transportws.Conn, sessionID string, inbound runStreamInboundMessage) {
	accepted, _, err := s.startPeerManagedHostSessionRun(managedHostSessionRunRequest{SessionID: sessionID, RunRequest: inbound.RunRequest})
	if err != nil {
		s.sendRunStreamControl(conn, runStreamControlMessage{Type: "error", OK: false, SessionID: sessionID, Error: err.Error()})
		return
	}
	_, sub, replay, err := s.runStreams.subscribe(accepted.RunID, inbound.LastSeq)
	if err != nil {
		s.sendRunStreamControl(conn, runStreamControlMessage{Type: "error", OK: false, SessionID: sessionID, RunID: accepted.RunID, Error: err.Error()})
		return
	}
	defer s.runStreams.unsubscribe(accepted.RunID, sub.id)
	if accepted.Background {
		raw, err := json.Marshal(accepted)
		if err == nil {
			_ = conn.WriteText(raw)
		}
		return
	}
	s.sendRunStreamControl(conn, runStreamControlMessage{Type: "run.accepted", OK: true, SessionID: sessionID, RunID: accepted.RunID, LastSeq: inbound.LastSeq})
	s.streamRunFrames(conn, accepted.RunID, sub, replay)
}

func (s *Server) startPeerManagedHostSessionRun(req managedHostSessionRunRequest) (managedHostSessionRunAccepted, int, error) {
	if s == nil || s.runner == nil || s.runStreams == nil {
		return managedHostSessionRunAccepted{}, http.StatusInternalServerError, errors.New("run service not configured")
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	if req.SessionID == "" {
		return managedHostSessionRunAccepted{}, http.StatusBadRequest, errors.New("session_id is required")
	}
	req.RunRequest = req.RunRequest.Normalized()
	if req.RunRequest.Prompt == "" && !req.RunRequest.Compact {
		return managedHostSessionRunAccepted{}, http.StatusBadRequest, errors.New("prompt is required")
	}
	if normalized := runruntime.NormalizeRunTargetKind(req.RunRequest.TargetKind); req.RunRequest.TargetKind != "" && normalized == "" {
		return managedHostSessionRunAccepted{}, http.StatusBadRequest, errors.New("unsupported target_kind")
	}
	state, err := s.runStreams.newRun(req.SessionID)
	if err != nil {
		return managedHostSessionRunAccepted{}, http.StatusInternalServerError, err
	}
	started := s.startManagedHostRunStreamExecution(state.runID, req.SessionID, req.RunRequest)
	if startErr := <-started; startErr != nil {
		return managedHostSessionRunAccepted{}, http.StatusBadRequest, startErr
	}
	return managedHostSessionRunAccepted{OK: true, SessionID: req.SessionID, RunID: state.runID, Status: "accepted", Background: req.RunRequest.Background, TargetKind: strings.TrimSpace(req.RunRequest.TargetKind), TargetName: strings.TrimSpace(req.RunRequest.TargetName), OwnerTransport: "managed_host_peer"}, http.StatusAccepted, nil
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
	r = s.requestWithTrustedSessionPrincipal(r, req.SessionID)
	if _, _, ok := s.verifySessionOwnershipForRequest(w, r, req.SessionID); !ok {
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
	r = s.requestWithTrustedSessionPrincipal(r, req.SessionID)
	if _, _, ok := s.verifySessionOwnershipForRequest(w, r, req.SessionID); !ok {
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
	if err := s.storeMirroredEventPayloadTitle(req.SessionID, req.EventType, req.Payload); err != nil {
		log.Printf("warning: store managed-host mirrored event title failed session_id=%q event_type=%q: %v", strings.TrimSpace(req.SessionID), strings.TrimSpace(req.EventType), err)
	}
	if err := s.storeMirroredEventPayloadPermission(req.SessionID, req.Payload); err != nil {
		log.Printf("warning: store managed-host mirrored event permission failed session_id=%q event_type=%q: %v", strings.TrimSpace(req.SessionID), strings.TrimSpace(req.EventType), err)
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
		principal, principalOK := s.principalForManagedHostSessionRunOK(sessionID)
		if !principalOK || !principal.Valid() {
			select {
			case started <- identity.ErrPrincipalRequired:
			default:
			}
			s.runStreams.publishError(runID, sessionID, identity.ErrPrincipalRequired)
			return
		}
		runCtx := identity.ContextWithPrincipal(s.runCtx, principal)
		result, err := s.runner.RunTurnStreaming(runCtx, sessionID, request, runruntime.RunStartMeta{RunID: runID, OwnerTransport: "managed_host_peer", Principal: principal}, func(event runruntime.StreamEvent) {
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

func (s *Server) principalForManagedHostSessionRun(sessionID string) identity.Principal {
	if principal, ok := s.principalForManagedHostSessionRunOK(sessionID); ok {
		return principal
	}
	return identity.Principal{}
}

func (s *Server) principalForManagedHostSessionRunOK(sessionID string) (identity.Principal, bool) {
	if s == nil {
		return identity.Principal{}, false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return identity.Principal{}, false
	}
	if s.sessions != nil {
		if session, ok, err := s.sessions.GetSession(sessionID); err != nil {
			return identity.Principal{}, false
		} else if ok {
			userID := strings.TrimSpace(session.UserID)
			accountScopeID := strings.TrimSpace(session.AccountScopeID)
			if userID != "" && accountScopeID != "" {
				return identity.Principal{Type: identity.PrincipalTypeUser, UserID: userID, AccountScopeID: accountScopeID, AccountScopeSource: identity.AccountScopeSourceSession, SessionID: sessionID}, true
			}
		}
	}
	if s.sessionRoutes != nil {
		if route, ok, err := s.sessionRoutes.Get(sessionID); err != nil {
			return identity.Principal{}, false
		} else if ok {
			userID := strings.TrimSpace(route.UserID)
			accountScopeID := strings.TrimSpace(route.AccountScopeID)
			if userID != "" && accountScopeID != "" {
				return identity.Principal{Type: identity.PrincipalTypeUser, UserID: userID, AccountScopeID: accountScopeID, AccountScopeSource: identity.AccountScopeSourceSession, SessionID: sessionID}, true
			}
		}
	}
	return identity.Principal{}, false
}

func requireManagedHostSessionMirrorOwnership(session *pebblestore.SessionSnapshot, principal identity.Principal) error {
	if session == nil {
		return errors.New("managed host session response is missing")
	}
	if !principal.Valid() {
		return identity.ErrPrincipalRequired
	}
	userID := strings.TrimSpace(principal.UserID)
	accountScopeID := strings.TrimSpace(principal.AccountScopeID)
	if strings.TrimSpace(session.UserID) == "" {
		session.UserID = userID
	}
	if strings.TrimSpace(session.AccountScopeID) == "" {
		session.AccountScopeID = accountScopeID
	}
	if strings.TrimSpace(session.UserID) != userID || strings.TrimSpace(session.AccountScopeID) != accountScopeID {
		return errors.New("managed host session account ownership mismatch")
	}
	return nil
}

func (s *Server) verifiedPeerManagedHostSessionOpenPrincipal(r *http.Request, req peerManagedHostSessionOpenRequest) (identity.Principal, bool) {
	if s == nil || r == nil || s.swarmStore == nil {
		return identity.Principal{}, false
	}
	userID := strings.TrimSpace(req.Route.UserID)
	accountScopeID := strings.TrimSpace(req.Route.AccountScopeID)
	if userID == "" || accountScopeID == "" {
		return identity.Principal{}, false
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return identity.Principal{}, false
	}
	if strings.TrimSpace(req.Route.PrimarySwarmID) == "" {
		return identity.Principal{}, false
	}
	peerSwarmID, authorizedPeer := authorizedPeerSwarmID(r)
	if !authorizedPeer && !isLocalTransportRequest(r) {
		return identity.Principal{}, false
	}
	pairing, ok, err := s.swarmStore.GetLocalPairing()
	if err != nil || !ok || !strings.EqualFold(strings.TrimSpace(pairing.PairingState), "paired") {
		return identity.Principal{}, false
	}
	if authorizedPeer {
		parentSwarmID := strings.TrimSpace(pairing.ParentSwarmID)
		if parentSwarmID == "" || !strings.EqualFold(parentSwarmID, strings.TrimSpace(peerSwarmID)) {
			return identity.Principal{}, false
		}
	}
	if strings.TrimSpace(pairing.UserID) != userID || strings.TrimSpace(pairing.AccountScopeID) != accountScopeID {
		return identity.Principal{}, false
	}
	if managedHostSwarmID := strings.TrimSpace(req.Route.ManagedHostSwarmID); managedHostSwarmID != "" && !s.isLocalSwarmID(managedHostSwarmID) {
		return identity.Principal{}, false
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: userID, AccountScopeID: accountScopeID, AccountScopeSource: identity.AccountScopeSourceSession, SessionID: sessionID}
	return principal, principal.Valid()
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

func (s *Server) managedHostTargetForSessionRequest(r *http.Request, sessionID, targetSwarmID string) (*swarmTarget, int, error) {
	if s == nil || s.sessions == nil {
		return nil, http.StatusInternalServerError, errors.New("session service not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, http.StatusBadRequest, errors.New("session_id is required")
	}
	principal, principalOK := PrincipalFromRequest(r)
	if !principalOK || !principal.Valid() {
		return nil, http.StatusUnauthorized, identity.ErrPrincipalRequired
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	if !ok || strings.TrimSpace(session.AccountScopeID) == "" || strings.TrimSpace(session.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return nil, http.StatusNotFound, errors.New("session not found")
	}
	if sessionUserID := strings.TrimSpace(session.UserID); sessionUserID != "" && sessionUserID != strings.TrimSpace(principal.UserID) {
		return nil, http.StatusNotFound, errors.New("session not found")
	}
	targetSwarmID = strings.TrimSpace(targetSwarmID)
	if targetSwarmID == "" {
		targetSwarmID = managedHostSessionStringMetadata(session.Metadata, "swarm_managed_host_swarm_id")
	}
	if s.sessionRoutes != nil {
		route, routeFound, routeErr := s.sessionRoutes.Get(sessionID)
		if routeErr != nil {
			return nil, http.StatusBadRequest, routeErr
		}
		if routeFound {
			if strings.TrimSpace(route.AccountScopeID) == "" || strings.TrimSpace(route.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
				return nil, http.StatusNotFound, errors.New("session not found")
			}
			if routeUserID := strings.TrimSpace(route.UserID); routeUserID != "" && routeUserID != strings.TrimSpace(principal.UserID) {
				return nil, http.StatusNotFound, errors.New("session not found")
			}
			if targetSwarmID == "" {
				targetSwarmID = strings.TrimSpace(route.ChildSwarmID)
			} else if routeSwarmID := strings.TrimSpace(route.ChildSwarmID); routeSwarmID != "" && !strings.EqualFold(routeSwarmID, targetSwarmID) {
				return nil, http.StatusNotFound, errors.New("session not found")
			}
		}
	}
	if targetSwarmID == "" {
		return nil, http.StatusBadRequest, errors.New("target_swarm_id is required")
	}
	target, _, _, status, err := s.resolveManagedHostSessionTarget(requestWithSwarmTargetQuery(r, targetSwarmID), targetSwarmID)
	if err != nil {
		return nil, status, err
	}
	return target, http.StatusOK, nil
}

func managedHostTargetBySwarmID(targets []swarmTarget, targetSwarmID string) *swarmTarget {
	targetSwarmID = strings.TrimSpace(targetSwarmID)
	if targetSwarmID == "" {
		return nil
	}
	for i := range targets {
		if !strings.EqualFold(strings.TrimSpace(targets[i].SwarmID), targetSwarmID) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(targets[i].Relationship), swarmruntime.RelationshipManaged) && !strings.EqualFold(strings.TrimSpace(targets[i].Kind), "manager") && !strings.EqualFold(strings.TrimSpace(targets[i].Relationship), "self") {
			target := targets[i]
			return &target
		}
	}
	return nil
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
		if !strings.EqualFold(strings.TrimSpace(targets[i].SwarmID), targetSwarmID) {
			continue
		}
		if target == nil || targets[i].Selectable {
			targetCopy := targets[i]
			target = &targetCopy
		}
		if target.Selectable {
			break
		}
	}
	if target == nil {
		return nil, "", "", http.StatusBadRequest, errors.New("managed host target was not found")
	}
	if !strings.EqualFold(strings.TrimSpace(target.Relationship), swarmruntime.RelationshipManaged) || strings.EqualFold(strings.TrimSpace(target.Kind), "manager") || strings.EqualFold(strings.TrimSpace(target.Relationship), "self") {
		if byID := managedHostTargetBySwarmID(targets, targetSwarmID); byID != nil {
			target = byID
		}
	}
	if !strings.EqualFold(strings.TrimSpace(target.Relationship), swarmruntime.RelationshipManaged) || strings.EqualFold(strings.TrimSpace(target.Kind), "manager") || strings.EqualFold(strings.TrimSpace(target.Relationship), "self") {
		return nil, "", "", http.StatusBadRequest, fmt.Errorf("target must be a managed host (resolved swarm_id=%q name=%q relationship=%q kind=%q)", target.SwarmID, target.Name, target.Relationship, target.Kind)
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
	primarySwarmID := strings.TrimSpace(route.PrimarySwarmID)
	primaryBackendURL := strings.TrimSpace(route.PrimaryBackendURL)
	managedHostSwarmID := strings.TrimSpace(route.ManagedHostSwarmID)
	managedHostName := strings.TrimSpace(route.ManagedHostName)
	managedHostBackendURL := strings.TrimSpace(route.ManagedHostBackendURL)
	hostWorkspacePath := strings.TrimSpace(route.HostWorkspacePath)
	runtimeWorkspacePath := strings.TrimSpace(route.RuntimeWorkspacePath)
	routeID := ""
	if managedHostSwarmID != "" && runtimeWorkspacePath != "" {
		routeID = "swarm:" + managedHostSwarmID + ":" + runtimeWorkspacePath
	}
	extra := map[string]any{
		"swarm_managed_host_session":                             true,
		"swarm_managed_host_primary_swarm_id":                    primarySwarmID,
		"swarm_managed_host_primary_backend_url":                 primaryBackendURL,
		"swarm_managed_host_swarm_id":                            managedHostSwarmID,
		"swarm_managed_host_name":                                managedHostName,
		"swarm_managed_host_backend_url":                         managedHostBackendURL,
		"swarm_managed_host_host_workspace_path":                 hostWorkspacePath,
		"swarm_managed_host_runtime_workspace_path":              runtimeWorkspacePath,
		"owner_transport":                                        "managed_host_peer",
		"swarm_route_id":                                         routeID,
		"swarm_route_label":                                      firstNonEmpty(managedHostName, managedHostSwarmID),
		"swarm_route_target_kind":                                "host",
		"swarm_route_target_relationship":                        swarmruntime.RelationshipManaged,
		sessionruntime.HostedSessionMetadataEnabled:              true,
		sessionruntime.HostedSessionMetadataHostSwarmID:          primarySwarmID,
		sessionruntime.HostedSessionMetadataHostBackendURL:       primaryBackendURL,
		sessionruntime.HostedSessionMetadataHostWorkspacePath:    hostWorkspacePath,
		sessionruntime.HostedSessionMetadataRuntimeWorkspacePath: runtimeWorkspacePath,
		sessionruntime.HostedSessionMetadataChildSwarmID:         managedHostSwarmID,
	}
	merged := mergeSessionCreateMetadata(metadata, extra)
	for key, value := range extra {
		merged[key] = value
	}
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
