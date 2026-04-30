package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	remotedeploy "swarm/packages/swarmd/internal/remotedeploy"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

type sessionCreateRequest struct {
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

type peerSessionOpenRequest struct {
	SessionID string                                 `json:"session_id"`
	Request   sessionCreateRequest                   `json:"request"`
	Hosted    sessionruntime.HostedSessionDescriptor `json:"hosted"`
}

func (s *Server) routedSessionTarget(sessionID string) (*swarmTarget, bool, error) {
	if s == nil || s.sessionRoutes == nil {
		return nil, false, nil
	}
	record, ok, err := s.sessionRoutes.Get(sessionID)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	retired, err := s.retireStaleRoutedSessionTarget(record)
	if err != nil {
		return nil, false, err
	}
	if retired {
		return nil, false, nil
	}
	if strings.TrimSpace(record.ChildSwarmID) == "" || strings.TrimSpace(record.ChildBackendURL) == "" {
		return nil, false, errors.New("routed session is missing child route details")
	}
	target := &swarmTarget{
		SwarmID:      strings.TrimSpace(record.ChildSwarmID),
		Name:         strings.TrimSpace(record.ChildSwarmID),
		Role:         "child",
		Relationship: "child",
		Online:       true,
		Selectable:   true,
		Current:      true,
		BackendURL:   strings.TrimSpace(record.ChildBackendURL),
	}
	return target, true, nil
}

func normalizeRoutedSessionBackendURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

func (s *Server) replacementChildSwarmIDForRoutedSession(record pebblestore.SessionRouteRecord) (string, error) {
	if s == nil || s.deployContainers == nil {
		return "", nil
	}
	recordBackendURL := normalizeRoutedSessionBackendURL(record.ChildBackendURL)
	recordChildSwarmID := strings.TrimSpace(record.ChildSwarmID)
	if recordBackendURL == "" || recordChildSwarmID == "" {
		return "", nil
	}
	items, err := s.deployContainers.List(context.Background())
	if err != nil {
		return "", err
	}
	for _, item := range items {
		if !strings.EqualFold(strings.TrimSpace(item.AttachStatus), "attached") {
			continue
		}
		itemBackendURL := normalizeRoutedSessionBackendURL(item.ChildBackendURL)
		itemChildSwarmID := strings.TrimSpace(item.ChildSwarmID)
		if itemBackendURL == "" || itemChildSwarmID == "" {
			continue
		}
		if itemBackendURL != recordBackendURL {
			continue
		}
		if strings.EqualFold(itemChildSwarmID, recordChildSwarmID) {
			return "", nil
		}
		return itemChildSwarmID, nil
	}
	return "", nil
}

func (s *Server) retireStaleRoutedSessionTarget(record pebblestore.SessionRouteRecord) (bool, error) {
	if s == nil || s.sessionRoutes == nil {
		return false, nil
	}
	replacementChildSwarmID, err := s.replacementChildSwarmIDForRoutedSession(record)
	if err != nil {
		return false, err
	}
	if replacementChildSwarmID == "" {
		return false, nil
	}
	if err := s.sessionRoutes.Delete(record.SessionID); err != nil {
		return false, err
	}
	log.Printf("retired stale routed session session_id=%q old_child_swarm_id=%q replacement_child_swarm_id=%q child_backend_url=%q", strings.TrimSpace(record.SessionID), strings.TrimSpace(record.ChildSwarmID), replacementChildSwarmID, normalizeRoutedSessionBackendURL(record.ChildBackendURL))
	return true, nil
}

func (s *Server) retireStaleSessionRoutesForChild(childSwarmID, childBackendURL string) error {
	if s == nil || s.sessionRoutes == nil {
		return nil
	}
	childSwarmID = strings.TrimSpace(childSwarmID)
	childBackendURL = normalizeRoutedSessionBackendURL(childBackendURL)
	if childSwarmID == "" || childBackendURL == "" {
		return nil
	}
	routes, err := s.sessionRoutes.List(5000)
	if err != nil {
		return err
	}
	for _, record := range routes {
		recordChildSwarmID := strings.TrimSpace(record.ChildSwarmID)
		recordBackendURL := normalizeRoutedSessionBackendURL(record.ChildBackendURL)
		if strings.TrimSpace(record.SessionID) == "" || recordChildSwarmID == "" || recordBackendURL == "" {
			continue
		}
		if recordBackendURL != childBackendURL || strings.EqualFold(recordChildSwarmID, childSwarmID) {
			continue
		}
		if err := s.sessionRoutes.Delete(record.SessionID); err != nil {
			return err
		}
		log.Printf("retired stale routed session session_id=%q old_child_swarm_id=%q replacement_child_swarm_id=%q child_backend_url=%q", strings.TrimSpace(record.SessionID), recordChildSwarmID, childSwarmID, childBackendURL)
	}
	return nil
}

func (s *Server) proxyRoutedSessionRequest(w http.ResponseWriter, r *http.Request, sessionID string) bool {
	target, ok, err := s.routedSessionTarget(sessionID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return true
	}
	if !ok {
		target, err = s.currentRemoteSwarmTargetForRequest(r)
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return true
		}
		if target == nil {
			return false
		}
	}
	if err := s.proxyRequestToSwarmTarget(w, r, *target); err != nil {
		writeError(w, http.StatusBadGateway, err)
	}
	return true
}

func (s *Server) postPeerJSONToSwarmTarget(ctx context.Context, target swarmTarget, path string, payload any, out any) error {
	startedAt := time.Now()
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
	peerToken, err := s.outgoingPeerAuthTokenForTarget(nil, target)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(strings.TrimSpace(target.BackendURL), "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(peerAuthSwarmIDHeader, strings.TrimSpace(state.Node.SwarmID))
	req.Header.Set(peerAuthTokenHeader, peerToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("routed peer request failed swarm_id=%q path=%q elapsed_ms=%d err=%v", strings.TrimSpace(target.SwarmID), strings.TrimSpace(path), time.Since(startedAt).Milliseconds(), err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&failure)
		if strings.TrimSpace(failure.Error) != "" {
			log.Printf("routed peer request failed swarm_id=%q path=%q status=%d elapsed_ms=%d err=%q", strings.TrimSpace(target.SwarmID), strings.TrimSpace(path), resp.StatusCode, time.Since(startedAt).Milliseconds(), strings.TrimSpace(failure.Error))
			return errors.New(strings.TrimSpace(failure.Error))
		}
		log.Printf("routed peer request failed swarm_id=%q path=%q status=%d elapsed_ms=%d err=%q", strings.TrimSpace(target.SwarmID), strings.TrimSpace(path), resp.StatusCode, time.Since(startedAt).Milliseconds(), resp.Status)
		return errors.New(resp.Status)
	}
	log.Printf("routed peer request success swarm_id=%q path=%q status=%d elapsed_ms=%d", strings.TrimSpace(target.SwarmID), strings.TrimSpace(path), resp.StatusCode, time.Since(startedAt).Milliseconds())
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (s *Server) handlePeerSessionOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("session service not configured"))
		return
	}
	var req peerSessionOpenRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.SessionID) == "" {
		writeError(w, http.StatusBadRequest, errors.New("session id is required"))
		return
	}
	if strings.TrimSpace(req.Hosted.HostSwarmID) == "" {
		writeError(w, http.StatusBadRequest, errors.New("hosted host swarm id is required"))
		return
	}
	childReq := req.Request
	childWorkspacePath := firstNonEmpty(
		strings.TrimSpace(childReq.RuntimeWorkspacePath),
		strings.TrimSpace(childReq.WorkspacePath),
		strings.TrimSpace(req.Hosted.RuntimeWorkspacePath),
	)
	if childWorkspacePath == "" {
		writeError(w, http.StatusBadRequest, errors.New("runtime workspace path is required"))
		return
	}
	childReq.WorkspacePath = childWorkspacePath
	childReq.HostWorkspacePath = childWorkspacePath
	childReq.RuntimeWorkspacePath = childWorkspacePath
	if strings.TrimSpace(childReq.WorkspaceName) == "" {
		childReq.WorkspaceName = filepath.Base(childWorkspacePath)
	}
	session, _, warning, modeWarning, err := s.createSessionFromRequestWithSessionID(childReq, req.Hosted.WithMetadata(nil), true, req.SessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"session": session,
		"warning": strings.TrimSpace(strings.Join([]string{warning, modeWarning}, " ")),
	})
}

func (s *Server) handlePeerSessionAppendMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("session service not configured"))
		return
	}
	var req struct {
		SessionID string         `json:"session_id"`
		Role      string         `json:"role"`
		Content   string         `json:"content"`
		Metadata  map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	message, session, event, err := s.sessions.AppendMessage(req.SessionID, req.Role, req.Content, req.Metadata)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil {
		s.hub.Publish(*event)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": message, "session": session})
}

func (s *Server) handlePeerSessionMode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("session service not configured"))
		return
	}
	var req struct {
		SessionID string `json:"session_id"`
		Mode      string `json:"mode"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	session, event, err := s.sessions.SetMode(req.SessionID, req.Mode)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil {
		s.hub.Publish(*event)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": session})
}

func (s *Server) handlePeerSessionTitle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("session service not configured"))
		return
	}
	var req struct {
		SessionID string `json:"session_id"`
		Title     string `json:"title"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	session, event, err := s.sessions.SetTitle(req.SessionID, req.Title)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil {
		s.hub.Publish(*event)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": session})
}

func (s *Server) handlePeerSessionMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("session service not configured"))
		return
	}
	var req struct {
		SessionID string         `json:"session_id"`
		Metadata  map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	session, event, err := s.sessions.UpdateMetadata(req.SessionID, req.Metadata)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil {
		s.hub.Publish(*event)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": session})
}

func (s *Server) handlePeerSessionLifecycle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("session service not configured"))
		return
	}
	var req struct {
		Lifecycle pebblestore.SessionLifecycleSnapshot `json:"lifecycle"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.sessions.StoreMirroredLifecycle(req.Lifecycle); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event, err := mirroredLifecycleEvent(req.Lifecycle); err == nil && event != nil {
		if s.hub != nil {
			s.hub.Publish(*event)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handlePeerSessionEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("session service not configured"))
		return
	}
	var req struct {
		SessionID     string         `json:"session_id"`
		EventType     string         `json:"event_type"`
		Payload       map[string]any `json:"payload"`
		CausationID   string         `json:"causation_id"`
		CorrelationID string         `json:"correlation_id"`
	}
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
		log.Printf("warning: store mirrored event lifecycle failed session_id=%q event_type=%q: %v", strings.TrimSpace(req.SessionID), strings.TrimSpace(req.EventType), err)
	}
	if err := s.storeMirroredEventPayloadMessage(req.SessionID, req.Payload); err != nil {
		log.Printf("warning: store mirrored event message failed session_id=%q event_type=%q: %v", strings.TrimSpace(req.SessionID), strings.TrimSpace(req.EventType), err)
	}
	if s.hub != nil {
		s.hub.Publish(env)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "event": env})
}

func (s *Server) storeMirroredEventPayloadLifecycle(sessionID string, payload map[string]any) error {
	if s == nil || s.sessions == nil || len(payload) == 0 {
		return nil
	}
	rawLifecycle, ok := payload["lifecycle"]
	if !ok || rawLifecycle == nil {
		return nil
	}
	encoded, err := json.Marshal(rawLifecycle)
	if err != nil {
		return err
	}
	var lifecycle pebblestore.SessionLifecycleSnapshot
	if err := json.Unmarshal(encoded, &lifecycle); err != nil {
		return err
	}
	sessionID = strings.TrimSpace(sessionID)
	if lifecycle.SessionID == "" {
		lifecycle.SessionID = sessionID
	}
	if sessionID == "" || !strings.EqualFold(strings.TrimSpace(lifecycle.SessionID), sessionID) {
		return nil
	}
	return s.sessions.StoreMirroredLifecycle(lifecycle)
}

func (s *Server) storeMirroredEventPayloadMessage(sessionID string, payload map[string]any) error {
	if s == nil || s.sessions == nil || len(payload) == 0 {
		return nil
	}
	rawMessage, ok := payload["message"]
	if !ok || rawMessage == nil {
		return nil
	}
	encoded, err := json.Marshal(rawMessage)
	if err != nil {
		return err
	}
	var message pebblestore.MessageSnapshot
	if err := json.Unmarshal(encoded, &message); err != nil {
		return err
	}
	sessionID = strings.TrimSpace(sessionID)
	if message.SessionID == "" {
		message.SessionID = sessionID
	}
	if sessionID == "" || !strings.EqualFold(strings.TrimSpace(message.SessionID), sessionID) || message.GlobalSeq == 0 {
		return nil
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok {
		return err
	}
	_, err = s.sessions.StoreMirroredMessage(session, message)
	return err
}

func mirroredLifecycleEvent(snapshot pebblestore.SessionLifecycleSnapshot) (*pebblestore.EventEnvelope, error) {
	sessionID := strings.TrimSpace(snapshot.SessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	payload, err := json.Marshal(map[string]any{
		"type":            "session.lifecycle.updated",
		"session_id":      sessionID,
		"run_id":          strings.TrimSpace(snapshot.RunID),
		"lifecycle":       snapshot,
		"active":          snapshot.Active,
		"phase":           strings.TrimSpace(snapshot.Phase),
		"started_at":      snapshot.StartedAt,
		"ended_at":        snapshot.EndedAt,
		"updated_at":      snapshot.UpdatedAt,
		"generation":      snapshot.Generation,
		"stop_reason":     strings.TrimSpace(snapshot.StopReason),
		"error":           strings.TrimSpace(snapshot.Error),
		"owner_transport": strings.TrimSpace(snapshot.OwnerTransport),
	})
	if err != nil {
		return nil, err
	}
	return &pebblestore.EventEnvelope{
		Stream:    "session:" + sessionID,
		EventType: "session.lifecycle.updated",
		EntityID:  sessionID,
		Payload:   payload,
		TsUnixMs:  time.Now().UnixMilli(),
	}, nil
}

func (s *Server) decodeSessionCreateRequest(r *http.Request) (sessionCreateRequest, error) {
	var req sessionCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		return sessionCreateRequest{}, err
	}
	if strings.TrimSpace(req.HostWorkspacePath) == "" {
		req.HostWorkspacePath = strings.TrimSpace(req.WorkspacePath)
	}
	if strings.TrimSpace(req.RuntimeWorkspacePath) == "" {
		req.RuntimeWorkspacePath = firstNonEmpty(strings.TrimSpace(req.WorkspacePath), strings.TrimSpace(req.HostWorkspacePath))
	}
	if strings.TrimSpace(req.HostWorkspacePath) == "" {
		current, ok, err := s.workspace.CurrentBinding()
		if err != nil {
			return sessionCreateRequest{}, err
		}
		if ok {
			req.HostWorkspacePath = current.ResolvedPath
			if strings.TrimSpace(req.WorkspaceName) == "" {
				req.WorkspaceName = current.WorkspaceName
			}
		}
	}
	if strings.TrimSpace(req.RuntimeWorkspacePath) == "" {
		req.RuntimeWorkspacePath = strings.TrimSpace(req.HostWorkspacePath)
	}
	if strings.TrimSpace(req.WorkspaceName) == "" && strings.TrimSpace(req.HostWorkspacePath) != "" {
		req.WorkspaceName = filepath.Base(strings.TrimSpace(req.HostWorkspacePath))
	}
	return req, nil
}

func (s *Server) resolveRemoteRuntimeWorkspacePath(ctx context.Context, target swarmTarget, hostWorkspacePath, workspaceName string) string {
	if s == nil || s.remoteDeploys == nil {
		return ""
	}
	hostWorkspacePath = strings.TrimSpace(hostWorkspacePath)
	workspaceName = strings.TrimSpace(workspaceName)
	items, err := s.remoteDeploys.ListCached(ctx)
	if err != nil {
		return ""
	}
	for _, item := range items {
		if !matchesRemoteDeployTarget(item, target) {
			continue
		}
		for _, payload := range item.Preflight.Payloads {
			targetPath := strings.TrimSpace(payload.TargetPath)
			if targetPath == "" {
				continue
			}
			if hostWorkspacePath != "" {
				if strings.EqualFold(strings.TrimSpace(payload.WorkspacePath), hostWorkspacePath) ||
					strings.EqualFold(strings.TrimSpace(payload.SourcePath), hostWorkspacePath) ||
					strings.EqualFold(strings.TrimSpace(payload.GitRoot), hostWorkspacePath) {
					return targetPath
				}
			}
			if workspaceName != "" && strings.EqualFold(strings.TrimSpace(payload.WorkspaceName), workspaceName) {
				return targetPath
			}
		}
	}
	return ""
}

func (s *Server) resolveRemoteHostBackendURL(ctx context.Context, target swarmTarget) string {
	if s == nil || s.remoteDeploys == nil {
		return ""
	}
	items, err := s.remoteDeploys.ListCached(ctx)
	if err != nil {
		return ""
	}
	for _, item := range items {
		if !matchesRemoteDeployTarget(item, target) {
			continue
		}
		if endpoint := strings.TrimSpace(item.HostAPIBaseURL); endpoint != "" {
			return endpoint
		}
		if endpoint := strings.TrimSpace(item.MasterTailscaleURL); endpoint != "" {
			return endpoint
		}
	}
	return ""
}

func matchesRemoteDeployTarget(item remotedeploy.Session, target swarmTarget) bool {
	if strings.TrimSpace(item.ChildSwarmID) == "" {
		return false
	}
	if strings.TrimSpace(target.DeploymentID) != "" && !strings.EqualFold(strings.TrimSpace(item.ID), strings.TrimSpace(target.DeploymentID)) {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(item.ChildSwarmID), strings.TrimSpace(target.SwarmID))
}

func (s *Server) createSessionFromRequest(req sessionCreateRequest, overrideMetadata map[string]any, allowWorktree bool) (pebblestore.SessionSnapshot, *pebblestore.EventEnvelope, string, string, error) {
	return s.createSessionFromRequestWithSessionID(req, overrideMetadata, allowWorktree, "")
}

func (s *Server) createSessionFromRequestWithSessionID(req sessionCreateRequest, overrideMetadata map[string]any, allowWorktree bool, sessionIDOverride string) (pebblestore.SessionSnapshot, *pebblestore.EventEnvelope, string, string, error) {
	createOptions := sessionruntime.CreateSessionOptions{
		Title:         req.Title,
		WorkspacePath: strings.TrimSpace(req.HostWorkspacePath),
		WorkspaceName: req.WorkspaceName,
		Mode:          req.Mode,
		Preference: &pebblestore.ModelPreference{
			Provider:    req.Preference.Provider,
			Model:       req.Preference.Model,
			Thinking:    req.Preference.Thinking,
			ServiceTier: req.Preference.ServiceTier,
			ContextMode: req.Preference.ContextMode,
		},
	}
	requestedWorktreeMode := strings.TrimSpace(req.WorktreeMode)
	modeWarning := ""
	if s.agents == nil {
		return pebblestore.SessionSnapshot{}, nil, "", "", errors.New("agent service not configured")
	}
	profile, profileErr := s.agents.ResolveAgent(strings.TrimSpace(req.AgentName))
	if profileErr != nil {
		return pebblestore.SessionSnapshot{}, nil, "", "", profileErr
	}
	agentName := strings.TrimSpace(profile.Name)
	if agentName == "" {
		agentName = "swarm"
	}
	if !pebblestore.AgentExitPlanModeEnabled(profile) {
		setting, ok := pebblestore.AgentExecutionSetting(profile)
		if !ok {
			return pebblestore.SessionSnapshot{}, nil, "", "", errors.New(agentName + " has plan mode disabled but no execution_setting is configured")
		}
		if sessionruntime.NormalizeMode(req.Mode) != setting {
			modeWarning = "agent " + strconv.Quote(agentName) + " has plan mode disabled; ignoring requested session mode " + strconv.Quote(sessionruntime.NormalizeMode(req.Mode)) + " and using execution setting " + strconv.Quote(setting)
		}
		createOptions.Mode = setting
	}
	sessionID := strings.TrimSpace(sessionIDOverride)
	if sessionID == "" {
		sessionID = sessionruntime.NewSessionID()
	}
	createOptions.SessionID = sessionID
	createOptions.Metadata = mergeSessionCreateMetadata(map[string]any{
		"workspace_id":  worktreeruntime.WorkspaceIdentityForSession(sessionID),
		"runtime_state": "standby",
		"title_pending": true,
		"agent_name":    agentName,
		"agent_mode":    strings.TrimSpace(profile.Mode),
	}, mergeSessionCreateMetadata(req.Metadata, overrideMetadata))
	warning := ""
	if allowWorktree {
		nextWarning, worktreeErr := s.applySessionCreateWorktree(&createOptions, sessionID, requestedWorktreeMode)
		if worktreeErr != nil {
			return pebblestore.SessionSnapshot{}, nil, "", "", worktreeErr
		}
		warning = nextWarning
		if descriptor, hosted := sessionruntime.HostedSessionFromMetadata(createOptions.Metadata); hosted && strings.TrimSpace(createOptions.WorkspacePath) != "" {
			descriptor.RuntimeWorkspacePath = strings.TrimSpace(createOptions.WorkspacePath)
			createOptions.Metadata = descriptor.WithMetadata(createOptions.Metadata)
		}
	}
	session, event, err := s.sessions.CreateSessionWithOptions(createOptions)
	if err != nil {
		return pebblestore.SessionSnapshot{}, nil, "", "", err
	}
	if allowWorktree && s.worktrees != nil {
		session, event, err = sessionruntime.AttachCreatedWorktreeBranch(s.sessions, s.worktrees, session)
		if err != nil {
			return pebblestore.SessionSnapshot{}, nil, "", "", err
		}
	}
	return session, event, warning, modeWarning, nil
}

func (s *Server) applySessionCreateWorktree(createOptions *sessionruntime.CreateSessionOptions, sessionID, rawRequestedMode string) (string, error) {
	if createOptions == nil {
		return "", nil
	}
	requestedMode := runruntime.NormalizeRunWorktreeMode(rawRequestedMode)
	if strings.TrimSpace(rawRequestedMode) != "" && requestedMode == "" {
		return "", errors.New("unsupported worktree_mode " + strconv.Quote(strings.TrimSpace(rawRequestedMode)))
	}
	if s == nil || s.worktrees == nil {
		if requestedMode == runruntime.RunWorktreeModeOn {
			return "", errors.New("worktree service not configured")
		}
		return "", nil
	}

	config, cfgErr := s.worktrees.GetConfig(createOptions.WorkspacePath)
	if cfgErr != nil {
		return "", cfgErr
	}
	switch requestedMode {
	case "", runruntime.RunWorktreeModeInherit:
		if !config.Enabled {
			return "", nil
		}
		return s.allocateSessionCreateDetachedWorkspace(createOptions, sessionID, func() (worktreeruntime.Allocation, error) {
			return s.worktrees.AllocateDetachedWorkspace(createOptions.WorkspacePath, sessionID)
		})
	case runruntime.RunWorktreeModeOff:
		return "", nil
	case runruntime.RunWorktreeModeOn:
		if config.Enabled {
			return s.allocateSessionCreateDetachedWorkspace(createOptions, sessionID, func() (worktreeruntime.Allocation, error) {
				return s.worktrees.AllocateDetachedWorkspace(createOptions.WorkspacePath, sessionID)
			})
		}
		return s.allocateSessionCreateDetachedWorkspace(createOptions, sessionID, func() (worktreeruntime.Allocation, error) {
			return s.worktrees.AllocateDetachedWorkspaceRequested(createOptions.WorkspacePath, sessionID, "", "")
		})
	default:
		return "", errors.New("unsupported worktree_mode " + strconv.Quote(strings.TrimSpace(rawRequestedMode)))
	}
}

func (s *Server) allocateSessionCreateDetachedWorkspace(createOptions *sessionruntime.CreateSessionOptions, sessionID string, allocate func() (worktreeruntime.Allocation, error)) (string, error) {
	if createOptions == nil || allocate == nil {
		return "", nil
	}
	if strings.TrimSpace(createOptions.WorkspaceName) == "" {
		createOptions.WorkspaceName = filepath.Base(strings.TrimSpace(createOptions.WorkspacePath))
	}
	allocation, allocErr := allocate()
	if allocErr != nil {
		warning := worktreeruntime.DetachedWorkspaceFallbackWarning(allocErr)
		if warning == "" {
			return "", allocErr
		}
		return warning, nil
	}
	createOptions.WorkspacePath = allocation.WorkspacePath
	createOptions.Worktree = &sessionruntime.CreateSessionWorktree{
		RootPath:    allocation.RepoRoot,
		BaseBranch:  allocation.BaseBranch,
		BranchName:  allocation.BranchName,
		WorkspaceID: allocation.WorkspaceID,
	}
	return "", nil
}
