package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	gorillaws "github.com/gorilla/websocket"
	"swarm/packages/swarmd/internal/identity"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	transportws "swarm/packages/swarmd/internal/transport/ws"
)

const sessionsV2LifecyclePrefix = "/v2/sessions/"

type sessionV2Authority struct {
	Principal identity.Principal
	Execution pebblestore.SessionExecutionV2Record
	Placement pebblestore.TopologyRuntimePlacementRecord
	Binding   *pebblestore.TopologyWorkspaceBindingRecord
	Mutating  bool
}

func validatePrimarySessionV2DispatchAuthority(authority sessionV2Authority) error {
	switch strings.TrimSpace(authority.Execution.ExecutionClass) {
	case sessionruntime.SessionExecutionClassPrimary:
		return nil
	case sessionruntime.SessionExecutionClassLocalContainer:
		return sessionV2InvalidClass("sessions v2 local-container lifecycle dispatch must use native runtime dispatch")
	default:
		return sessionV2InvalidClass("sessions v2 execution class %q is not supported", authority.Execution.ExecutionClass)
	}
}

func isMutatingSessionsV2LifecycleRequest(method, subpath string) bool {
	switch subpath {
	case "":
		return false
	case "messages", "metadata", "mode", "preference", "codex", "plans/active", "plans":
		return method == http.MethodPost
	case "permissions/resolve_all", "run", "run/stop/primary", "run/stop/local-container":
		return method == http.MethodPost
	case "run/stream":
		return false
	default:
		return strings.HasPrefix(subpath, "permissions/") && strings.HasSuffix(subpath, "/resolve") && method == http.MethodPost
	}
}

func isMutatingSessionsV2LifecycleHTTPRequest(r *http.Request, subpath string) bool {
	if r == nil {
		return false
	}
	if subpath != "run/stream" || r.Method != http.MethodPost {
		return isMutatingSessionsV2LifecycleRequest(r.Method, subpath)
	}
	if r.Body == nil {
		return false
	}
	raw, err := io.ReadAll(r.Body)
	if r.Body != nil {
		_ = r.Body.Close()
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if err != nil {
		return false
	}
	var inbound struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &inbound); err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(inbound.Type)) {
	case "run.start", "start", "run.stop", "stop":
		return true
	default:
		return false
	}
}

func localContainerRuntimeLifecycleAction(subpath string) (string, bool) {
	switch subpath {
	case "":
		return "", true
	case "messages", "metadata", "mode", "preference", "codex", "plans/active", "plans", "permissions", "permissions/resolve_all", "usage", "run", "run/stream":
		return subpath, true
	case "run/stop/local-container":
		return "run/stop", true
	default:
		if strings.HasPrefix(subpath, "plans/") || strings.HasPrefix(subpath, "permissions/") && strings.HasSuffix(subpath, "/resolve") {
			return subpath, true
		}
		return "", false
	}
}

func validatePrimarySessionV2RunRequest(req runruntime.RunRequest) error {
	if req.ToolScope != nil {
		return sessionV2BadRequest("primary sessions v2 run request cannot override tool_scope")
	}
	normalized := req.Normalized()
	if strings.TrimSpace(normalized.TargetKind) != "" {
		return sessionV2BadRequest("primary sessions v2 run request cannot override target_kind")
	}
	if strings.TrimSpace(normalized.TargetName) != "" {
		return sessionV2BadRequest("primary sessions v2 run request cannot override target_name")
	}
	if normalized.ExecutionContext != nil {
		ctx := *normalized.ExecutionContext
		switch {
		case strings.TrimSpace(ctx.WorkspacePath) != "":
			return sessionV2BadRequest("primary sessions v2 run request cannot override execution_context.workspace_path")
		case strings.TrimSpace(ctx.CWD) != "":
			return sessionV2BadRequest("primary sessions v2 run request cannot override execution_context.cwd")
		case strings.TrimSpace(ctx.WorktreeMode) != "":
			return sessionV2BadRequest("primary sessions v2 run request cannot override execution_context.worktree_mode")
		case strings.TrimSpace(ctx.WorktreeRootPath) != "":
			return sessionV2BadRequest("primary sessions v2 run request cannot override execution_context.worktree_root_path")
		case strings.TrimSpace(ctx.WorktreeBranch) != "":
			return sessionV2BadRequest("primary sessions v2 run request cannot override execution_context.worktree_branch")
		case strings.TrimSpace(ctx.WorktreeBaseBranch) != "":
			return sessionV2BadRequest("primary sessions v2 run request cannot override execution_context.worktree_base_branch")
		}
	}
	// background only changes the response/stream owner transport for the
	// already-authorized primary v2 run. Runtime and workspace remain fixed by
	// the frozen SessionExecutionV2 authority validated before execution.
	return nil
}

func decodePrimarySessionV2RunStreamInbound(raw []byte) (runStreamInboundMessage, error) {
	var inbound runStreamInboundMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decodeJSONObject(decoder, &inbound); err != nil {
		return runStreamInboundMessage{}, fmt.Errorf("decode run stream payload: %w", err)
	}
	inbound.Type = strings.ToLower(strings.TrimSpace(inbound.Type))
	inbound.RunID = strings.TrimSpace(inbound.RunID)
	return inbound, nil
}

func (s *Server) handlePrimarySessionV2ByID(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil || s.topology == nil {
		writeSessionsV2Error(w, errors.New("sessions v2 service is not configured"))
		return
	}
	sessionID, subpath, ok := parsePrimarySessionV2LifecyclePath(r.URL.Path)
	if !ok {
		writeSessionsV2Error(w, sessionV2BadRequest("invalid sessions v2 lifecycle path"))
		return
	}
	if s.handleLocalContainerSessionV2LifecycleIfNeeded(w, r, sessionID, subpath) {
		return
	}

	switch subpath {
	case "":
		s.handlePrimarySessionV2Get(w, r, sessionID)
	case "messages":
		s.handlePrimarySessionV2Messages(w, r, sessionID)
	case "metadata":
		s.handlePrimarySessionV2Metadata(w, r, sessionID)
	case "mode":
		s.handlePrimarySessionV2Mode(w, r, sessionID)
	case "preference":
		s.handlePrimarySessionV2Preference(w, r, sessionID)
	case "codex":
		s.handlePrimarySessionV2Codex(w, r, sessionID)
	case "plans/active":
		s.handlePrimarySessionV2ActivePlan(w, r, sessionID)
	case "plans":
		s.handlePrimarySessionV2Plans(w, r, sessionID)
	case "permissions":
		s.handlePrimarySessionV2Permissions(w, r, sessionID)
	case "permissions/resolve_all":
		s.handlePrimarySessionV2PermissionResolveAll(w, r, sessionID)
	case "usage":
		s.handlePrimarySessionV2Usage(w, r, sessionID)
	case "run":
		s.handlePrimarySessionV2Run(w, r, sessionID)
	case "run/stop/primary":
		if s.handleLocalContainerSessionV2LifecycleIfNeeded(w, r, sessionID, subpath) {
			return
		}
		s.handlePrimarySessionV2RunStopPrimary(w, r, sessionID)
	case "run/stop/local-container":
		s.handlePrimarySessionV2RunStopLocalContainer(w, r, sessionID)
	case "run/stream":
		s.handlePrimarySessionV2RunStream(w, r, sessionID)
	default:
		if strings.HasPrefix(subpath, "plans/") {
			s.handlePrimarySessionV2PlanByID(w, r, sessionID, strings.TrimPrefix(subpath, "plans/"))
			return
		}
		if strings.HasPrefix(subpath, "permissions/") && strings.HasSuffix(subpath, "/resolve") {
			s.handlePrimarySessionV2PermissionResolve(w, r, sessionID, strings.TrimSuffix(strings.TrimPrefix(subpath, "permissions/"), "/resolve"))
			return
		}
		writeSessionsV2Error(w, sessionV2BadRequest("unknown sessions v2 lifecycle path %q", subpath))
	}
}

func parsePrimarySessionV2LifecyclePath(path string) (string, string, bool) {
	if !strings.HasPrefix(path, sessionsV2LifecyclePrefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(path, sessionsV2LifecyclePrefix)
	if rest == "" || strings.HasPrefix(rest, "/") || strings.HasSuffix(rest, "/") || strings.Contains(rest, "//") {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return "", "", false
		}
	}
	sessionID := strings.TrimSpace(parts[0])
	if sessionID == "" || isReservedPrimarySessionV2LifecycleID(sessionID) {
		return "", "", false
	}
	if len(parts) == 1 {
		return sessionID, "", true
	}
	return sessionID, strings.Join(parts[1:], "/"), true
}

func isReservedPrimarySessionV2LifecycleID(sessionID string) bool {
	switch strings.TrimSpace(sessionID) {
	case "primary", "local-containers", "local_container":
		return true
	default:
		return false
	}
}

func (s *Server) requirePrimarySessionV2DispatchAuthority(r *http.Request, sessionID string, mutating bool) (sessionV2Authority, error) {
	authority, err := s.requireSessionV2Authority(r, sessionID, mutating)
	if err != nil {
		return sessionV2Authority{}, err
	}
	if err := validatePrimarySessionV2DispatchAuthority(authority); err != nil {
		return sessionV2Authority{}, err
	}
	return authority, nil
}

func (s *Server) handleLocalContainerSessionV2LifecycleIfNeeded(w http.ResponseWriter, r *http.Request, sessionID, subpath string) bool {
	if strings.TrimSpace(subpath) == "run/stop/local-container" {
		return false
	}
	if strings.TrimSpace(subpath) == "run/stop/primary" {
		authority, err := s.requireSessionV2Authority(r, sessionID, isMutatingSessionsV2LifecycleHTTPRequest(r, subpath))
		if err != nil {
			writeSessionsV2Error(w, err)
			return true
		}
		if strings.TrimSpace(authority.Execution.ExecutionClass) == sessionruntime.SessionExecutionClassLocalContainer {
			if !methodAllowedForSessionsV2LifecycleSubpath(r.Method, subpath) {
				methodNotAllowed(w)
				return true
			}
			writeSessionsV2Error(w, sessionV2InvalidClass("sessions v2 local-container lifecycle dispatch must use native runtime dispatch"))
			return true
		}
		return false
	}
	action, supported := localContainerRuntimeLifecycleAction(subpath)
	if !supported {
		return false
	}
	authority, err := s.requireSessionV2Authority(r, sessionID, isMutatingSessionsV2LifecycleHTTPRequest(r, subpath))
	if err != nil {
		writeSessionsV2Error(w, err)
		return true
	}
	if strings.TrimSpace(authority.Execution.ExecutionClass) != sessionruntime.SessionExecutionClassLocalContainer {
		return false
	}
	if !methodAllowedForSessionsV2LifecycleSubpath(r.Method, subpath) {
		methodNotAllowed(w)
		return true
	}
	if err := s.dispatchLocalContainerSessionV2Lifecycle(w, r, authority, action); err != nil {
		writeSessionsV2Error(w, err)
	}
	return true
}

func methodAllowedForSessionsV2LifecycleSubpath(method, subpath string) bool {
	switch subpath {
	case "":
		return method == http.MethodGet
	case "messages", "metadata", "mode", "preference", "codex", "plans/active", "plans":
		return method == http.MethodGet || method == http.MethodPost
	case "permissions", "usage":
		return method == http.MethodGet
	case "permissions/resolve_all", "run", "run/stop/primary", "run/stop/local-container":
		return method == http.MethodPost
	case "run/stream":
		return method == http.MethodGet || method == http.MethodPost
	default:
		if strings.HasPrefix(subpath, "plans/") {
			return method == http.MethodGet
		}
		if strings.HasPrefix(subpath, "permissions/") && strings.HasSuffix(subpath, "/resolve") {
			return method == http.MethodPost
		}
		return false
	}
}

func (s *Server) dispatchLocalContainerSessionV2Lifecycle(w http.ResponseWriter, r *http.Request, authority sessionV2Authority, action string) error {
	if s == nil {
		return errors.New("sessions v2 service is not configured")
	}
	localNode, localOK, err := s.swarmLocalNode()
	if err != nil {
		return err
	}
	localSwarmID := strings.TrimSpace(localNode.SwarmID)
	if localOK && strings.EqualFold(localSwarmID, strings.TrimSpace(authority.Execution.RuntimeSwarmID)) {
		s.dispatchLocalRuntimeSessionV2Lifecycle(w, r, authority, action)
		return nil
	}
	return s.dispatchRemoteRuntimeSessionV2Lifecycle(w, r, authority, action)
}

func (s *Server) dispatchLocalRuntimeSessionV2Lifecycle(w http.ResponseWriter, r *http.Request, authority sessionV2Authority, action string) {
	sessionID := strings.TrimSpace(authority.Execution.SessionID)
	principal := authority.Principal
	principal.SessionID = sessionID
	ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, principal)
	ctx = identity.ContextWithPrincipal(ctx, principal)
	cloned := r.Clone(ctx)
	path := runtimeSessionsV2Prefix + sessionID
	if strings.TrimSpace(action) != "" {
		path += "/" + strings.Trim(strings.TrimSpace(action), "/")
	}
	cloned.URL.Path = path
	cloned.URL.RawPath = ""
	cloned.URL.RawQuery = r.URL.RawQuery
	cloned.RequestURI = ""
	s.handleRuntimeSessionsV2ByID(w, cloned)
}

func (s *Server) dispatchLocalContainerSessionV2RunStop(r *http.Request, authority sessionV2Authority, req sessionruntime.RuntimeSessionStopRequest) (sessionruntime.RuntimeSessionStopResponse, error) {
	if s == nil {
		return sessionruntime.RuntimeSessionStopResponse{}, errors.New("sessions v2 service is not configured")
	}
	localNode, localOK, err := s.swarmLocalNode()
	if err != nil {
		return sessionruntime.RuntimeSessionStopResponse{}, err
	}
	localSwarmID := strings.TrimSpace(localNode.SwarmID)
	var resp sessionruntime.RuntimeSessionStopResponse
	if localOK && strings.EqualFold(localSwarmID, strings.TrimSpace(authority.Execution.RuntimeSwarmID)) {
		principal := authority.Principal
		principal.SessionID = strings.TrimSpace(authority.Execution.SessionID)
		resp, err = s.stopRuntimeSessionV2Run(authority.Execution.SessionID, principal, req)
	} else {
		resp, err = s.dispatchRemoteRuntimeSessionV2RunStop(r, authority, req)
	}
	if err != nil {
		return sessionruntime.RuntimeSessionStopResponse{}, err
	}
	if resp.MirrorBatch == nil {
		return resp, nil
	}
	accepted, err := s.ingestRuntimeSessionV2MirrorBatch(authority.Execution, *resp.MirrorBatch, runtimeSessionV2MirrorIngestionOptions{})
	if err != nil {
		return sessionruntime.RuntimeSessionStopResponse{}, err
	}
	resp.MirrorAccepted = accepted
	resp.MirrorStatus = "accepted"
	resp.MirrorBatch = nil
	return resp, nil
}

func (s *Server) dispatchRemoteRuntimeSessionV2RunStop(r *http.Request, authority sessionV2Authority, req sessionruntime.RuntimeSessionStopRequest) (sessionruntime.RuntimeSessionStopResponse, error) {
	if s.swarm == nil {
		return sessionruntime.RuntimeSessionStopResponse{}, sessionV2AuthorityNotFound("runtime session peer authority for %q is not configured", authority.Execution.RuntimeSwarmID)
	}
	localNode, localOK, err := s.swarmLocalNode()
	if err != nil {
		return sessionruntime.RuntimeSessionStopResponse{}, err
	}
	localSwarmID := strings.TrimSpace(localNode.SwarmID)
	if !localOK || localSwarmID == "" {
		return sessionruntime.RuntimeSessionStopResponse{}, sessionV2AuthorityNotFound("runtime session peer authority for %q is not configured", authority.Execution.RuntimeSwarmID)
	}
	conn, ok := s.ResolveAuthorityConnection(authority.Principal.AccountScopeID, authority.Execution.RuntimeSwarmID)
	if !ok || strings.TrimSpace(conn.endpoint()) == "" {
		return sessionruntime.RuntimeSessionStopResponse{}, sessionV2AuthorityNotFound("runtime session authority connection for %q was not found", authority.Execution.RuntimeSwarmID)
	}
	if strings.EqualFold(conn.TransportKind, authorityConnectionTransportLocal) {
		return sessionruntime.RuntimeSessionStopResponse{}, sessionV2StaleAuthority("runtime session authority connection for %q resolved local transport for non-local runtime", authority.Execution.RuntimeSwarmID)
	}
	peerToken, ok, err := s.swarm.OutgoingPeerAuthToken(strings.TrimSpace(authority.Execution.RuntimeSwarmID))
	if err != nil {
		return sessionruntime.RuntimeSessionStopResponse{}, err
	}
	if !ok || strings.TrimSpace(peerToken) == "" {
		return sessionruntime.RuntimeSessionStopResponse{}, sessionV2AuthorityNotFound("runtime session peer authority for %q is not configured", authority.Execution.RuntimeSwarmID)
	}
	endpoint := strings.TrimRight(conn.endpoint(), "/") + runtimeSessionsV2Prefix + strings.TrimSpace(authority.Execution.SessionID) + "/run/stop"
	client := &http.Client{Timeout: runtimeSessionOpenHTTPTimeout}
	var resp sessionruntime.RuntimeSessionStopResponse
	headers := map[string]string{peerAuthSwarmIDHeader: localSwarmID, peerAuthTokenHeader: peerToken}
	if err := remoteSwarmJSONRequestWithClientAndHeaders(http.MethodPost, endpoint, req, &resp, client, headers); err != nil {
		return sessionruntime.RuntimeSessionStopResponse{}, sessionV2StaleAuthority("runtime session stop failed: %v", err)
	}
	return resp, nil
}

func (s *Server) dispatchRemoteRuntimeSessionV2Lifecycle(w http.ResponseWriter, r *http.Request, authority sessionV2Authority, action string) error {
	if s.swarm == nil {
		return sessionV2AuthorityNotFound("runtime session peer authority for %q is not configured", authority.Execution.RuntimeSwarmID)
	}
	localNode, localOK, err := s.swarmLocalNode()
	if err != nil {
		return err
	}
	localSwarmID := strings.TrimSpace(localNode.SwarmID)
	if !localOK || localSwarmID == "" {
		return sessionV2AuthorityNotFound("runtime session peer authority for %q is not configured", authority.Execution.RuntimeSwarmID)
	}
	conn, ok := s.ResolveAuthorityConnection(authority.Principal.AccountScopeID, authority.Execution.RuntimeSwarmID)
	if !ok || strings.TrimSpace(conn.endpoint()) == "" {
		return sessionV2AuthorityNotFound("runtime session authority connection for %q was not found", authority.Execution.RuntimeSwarmID)
	}
	if strings.EqualFold(conn.TransportKind, authorityConnectionTransportLocal) {
		return sessionV2StaleAuthority("runtime session authority connection for %q resolved local transport for non-local runtime", authority.Execution.RuntimeSwarmID)
	}
	peerToken, ok, err := s.swarm.OutgoingPeerAuthToken(strings.TrimSpace(authority.Execution.RuntimeSwarmID))
	if err != nil {
		return err
	}
	if !ok || strings.TrimSpace(peerToken) == "" {
		return sessionV2AuthorityNotFound("runtime session peer authority for %q is not configured", authority.Execution.RuntimeSwarmID)
	}

	endpoint := strings.TrimRight(conn.endpoint(), "/") + runtimeSessionsV2Prefix + strings.TrimSpace(authority.Execution.SessionID)
	if strings.TrimSpace(action) != "" {
		endpoint += "/" + strings.Trim(strings.TrimSpace(action), "/")
	}
	if r.URL != nil && r.URL.RawQuery != "" {
		endpoint += "?" + r.URL.RawQuery
	}
	if strings.Trim(strings.TrimSpace(action), "/") == "run/stream" && r.Method == http.MethodGet && isWebsocketUpgradeRequest(r) {
		return s.dispatchRemoteRuntimeSessionV2RunStreamWebsocket(w, r, authority, endpoint, localSwarmID, peerToken)
	}
	forwardReq, err := http.NewRequestWithContext(r.Context(), r.Method, endpoint, r.Body)
	if err != nil {
		return err
	}
	forwardReq.Header.Set("Accept", "application/json")
	if contentType := strings.TrimSpace(r.Header.Get("Content-Type")); contentType != "" {
		forwardReq.Header.Set("Content-Type", contentType)
	}
	forwardReq.Header.Set(peerAuthSwarmIDHeader, localSwarmID)
	forwardReq.Header.Set(peerAuthTokenHeader, peerToken)
	resp, err := (&http.Client{Timeout: runtimeSessionOpenHTTPTimeout}).Do(forwardReq)
	if err != nil {
		return sessionV2StaleAuthority("runtime session lifecycle dispatch failed: %v", err)
	}
	defer resp.Body.Close()
	for key, values := range resp.Header {
		if strings.EqualFold(key, "Content-Length") {
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	return nil
}

func (s *Server) dispatchRemoteRuntimeSessionV2RunStreamWebsocket(w http.ResponseWriter, r *http.Request, authority sessionV2Authority, endpoint, localSwarmID, peerToken string) error {
	wsEndpoint, err := websocketEndpointForBackend(endpoint)
	if err != nil {
		return err
	}
	downstream, err := transportws.Accept(w, r)
	if err != nil {
		return err
	}
	defer downstream.Close()
	raw, err := downstream.ReadText()
	if err != nil {
		log.Printf("sessions v2 local-container run stream initial read failed session_id=%s remote_addr=%s err=%v", authority.Execution.SessionID, strings.TrimSpace(r.RemoteAddr), err)
		return nil
	}
	inbound, err := decodePrimarySessionV2RunStreamInbound(raw)
	if err != nil {
		s.sendRunStreamControl(downstream, runStreamControlMessage{Type: "error", OK: false, SessionID: authority.Execution.SessionID, Error: err.Error()})
		return nil
	}
	switch inbound.Type {
	case "run.start", "start":
		if _, err := s.requireSessionV2Authority(r, authority.Execution.SessionID, true); err != nil {
			s.sendRunStreamControl(downstream, runStreamControlMessage{Type: "error", OK: false, SessionID: authority.Execution.SessionID, Error: err.Error()})
			return nil
		}
		if err := validatePrimarySessionV2RunRequest(inbound.RunRequest); err != nil {
			s.sendRunStreamControl(downstream, runStreamControlMessage{Type: "error", OK: false, SessionID: authority.Execution.SessionID, Error: err.Error()})
			return nil
		}
	case "run.stop", "stop":
		if _, err := s.requireSessionV2Authority(r, authority.Execution.SessionID, true); err != nil {
			s.sendRunStreamControl(downstream, runStreamControlMessage{Type: "error", OK: false, SessionID: authority.Execution.SessionID, Error: err.Error()})
			return nil
		}
	}
	headers := cloneHeaderForUpstreamWebsocket(r.Header)
	headers.Set(peerAuthSwarmIDHeader, strings.TrimSpace(localSwarmID))
	headers.Set(peerAuthTokenHeader, strings.TrimSpace(peerToken))
	upstream, resp, err := gorillaws.DefaultDialer.DialContext(r.Context(), wsEndpoint, headers)
	if err != nil {
		s.sendRunStreamControl(downstream, runStreamControlMessage{Type: "error", OK: false, SessionID: authority.Execution.SessionID, Error: summarizeWebsocketDialError(err, resp).Error()})
		return nil
	}
	defer upstream.Close()
	if err := upstream.WriteMessage(gorillaws.TextMessage, raw); err != nil {
		s.sendRunStreamControl(downstream, runStreamControlMessage{Type: "error", OK: false, SessionID: authority.Execution.SessionID, Error: err.Error()})
		return nil
	}
	bridgeWebsocketText(downstream, upstream)
	return nil
}

func (s *Server) requireSessionV2Authority(r *http.Request, sessionID string, mutating bool) (sessionV2Authority, error) {
	if s == nil || s.sessions == nil || s.sessions.Store() == nil || s.topology == nil {
		return sessionV2Authority{}, errors.New("sessions v2 service is not configured")
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		return sessionV2Authority{}, identity.ErrPrincipalRequired
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return sessionV2Authority{}, sessionV2BadRequest("session id is required")
	}

	execution, executionOK, err := s.sessions.Store().GetSessionExecutionV2(sessionID)
	if err != nil {
		return sessionV2Authority{}, err
	}
	if !executionOK {
		return sessionV2Authority{}, sessionV2AuthorityNotFound("sessions v2 execution for %q was not found", sessionID)
	}
	if strings.TrimSpace(execution.SessionID) != sessionID {
		return sessionV2Authority{}, sessionV2StaleAuthority("sessions v2 execution session id mismatch")
	}
	if strings.TrimSpace(execution.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return sessionV2Authority{}, sessionV2AccessDenied("sessions v2 execution account scope does not match principal")
	}
	if strings.TrimSpace(execution.UserID) != "" && strings.TrimSpace(execution.UserID) != strings.TrimSpace(principal.UserID) {
		return sessionV2Authority{}, sessionV2AccessDenied("sessions v2 execution user does not match principal")
	}

	placement, placementOK, err := s.topology.GetRuntimePlacementForAccount(principal.AccountScopeID, execution.RuntimeSwarmID)
	if err != nil {
		return sessionV2Authority{}, err
	}
	if !placementOK {
		return sessionV2Authority{}, sessionV2AuthorityNotFound("sessions v2 runtime placement for %q was not found", execution.RuntimeSwarmID)
	}
	if strings.TrimSpace(placement.RuntimeSwarmID) != strings.TrimSpace(execution.RuntimeSwarmID) {
		return sessionV2Authority{}, sessionV2StaleAuthority("sessions v2 runtime placement id mismatch")
	}
	if strings.TrimSpace(placement.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return sessionV2Authority{}, sessionV2AccessDenied("sessions v2 runtime placement account scope does not match principal")
	}
	if strings.TrimSpace(placement.State) != pebblestore.TopologyRuntimePlacementStateActive {
		return sessionV2Authority{}, sessionV2StaleAuthority("sessions v2 runtime placement is not active")
	}
	if placement.PlacementGeneration != execution.PlacementGeneration {
		return sessionV2Authority{}, sessionV2StaleAuthority("sessions v2 runtime placement generation mismatch")
	}
	authority := sessionV2Authority{Principal: principal, Execution: execution, Placement: placement, Mutating: mutating}
	switch strings.TrimSpace(execution.ExecutionClass) {
	case sessionruntime.SessionExecutionClassPrimary:
		return s.validatePrimarySessionV2Authority(authority)
	case sessionruntime.SessionExecutionClassLocalContainer:
		return s.validateLocalContainerSessionV2Authority(authority)
	default:
		return sessionV2Authority{}, sessionV2InvalidClass("sessions v2 execution class %q is not supported", execution.ExecutionClass)
	}
}

func (s *Server) validatePrimarySessionV2Authority(authority sessionV2Authority) (sessionV2Authority, error) {
	execution := authority.Execution
	placement := authority.Placement

	localNode, localOK, err := s.swarmLocalNode()
	if err != nil {
		return sessionV2Authority{}, err
	}
	localSwarmID := strings.TrimSpace(localNode.SwarmID)
	if !localOK || localSwarmID == "" {
		return sessionV2Authority{}, sessionV2AuthorityNotFound("sessions v2 primary local node identity is required")
	}
	if strings.TrimSpace(execution.RuntimeSwarmID) != localSwarmID || strings.TrimSpace(execution.AuthorityHostSwarmID) != localSwarmID {
		return sessionV2Authority{}, sessionV2StaleAuthority("sessions v2 execution is not owned by this primary runtime")
	}
	if strings.TrimSpace(execution.RuntimeKind) != pebblestore.TopologyRuntimeKindHost || strings.TrimSpace(execution.AuthorityContainerID) != "" {
		return sessionV2Authority{}, sessionV2InvalidClass("sessions v2 primary execution must target host runtime authority")
	}
	if strings.TrimSpace(placement.RuntimeKind) != strings.TrimSpace(execution.RuntimeKind) || strings.TrimSpace(placement.AuthorityHostSwarmID) != strings.TrimSpace(execution.AuthorityHostSwarmID) || strings.TrimSpace(placement.AuthorityContainerID) != strings.TrimSpace(execution.AuthorityContainerID) {
		return sessionV2Authority{}, sessionV2StaleAuthority("sessions v2 runtime placement authority mismatch")
	}
	if strings.TrimSpace(placement.RuntimeSwarmID) != localSwarmID || strings.TrimSpace(placement.AuthorityHostSwarmID) != localSwarmID {
		return sessionV2Authority{}, sessionV2StaleAuthority("sessions v2 primary placement is not local self-authority")
	}
	if strings.TrimSpace(placement.RuntimeKind) != pebblestore.TopologyRuntimeKindHost || strings.TrimSpace(placement.AuthorityContainerID) != "" {
		return sessionV2Authority{}, sessionV2InvalidClass("sessions v2 primary placement must be host self-placement")
	}
	if strings.TrimSpace(execution.WorkspaceBindingID) == "" {
		if err := validatePrimarySessionV2TUICWDExecution(execution); err != nil {
			return sessionV2Authority{}, err
		}
		return authority, nil
	}
	if strings.TrimSpace(execution.SourceWorkspaceID) == "" || execution.SourceWorkspaceGeneration <= 0 || execution.PlacementGeneration <= 0 || execution.BindingGeneration <= 0 {
		return sessionV2Authority{}, sessionV2StaleAuthority("sessions v2 execution authority identity is incomplete")
	}

	binding, err := s.requireSessionV2WorkspaceBinding(authority)
	if err != nil {
		return sessionV2Authority{}, err
	}
	if strings.TrimSpace(binding.DestinationRuntimeSwarmID) != localSwarmID || strings.TrimSpace(binding.DestinationAuthorityHostSwarmID) != localSwarmID {
		return sessionV2Authority{}, sessionV2StaleAuthority("sessions v2 workspace binding destination is not local primary self-authority")
	}
	if strings.TrimSpace(binding.DestinationRuntimeKind) != pebblestore.TopologyRuntimeKindHost || strings.TrimSpace(binding.DestinationContainerID) != "" {
		return sessionV2Authority{}, sessionV2InvalidClass("sessions v2 workspace binding destination must be host runtime")
	}
	authority.Binding = &binding
	return authority, nil
}

func (s *Server) validateLocalContainerSessionV2Authority(authority sessionV2Authority) (sessionV2Authority, error) {
	execution := authority.Execution
	placement := authority.Placement

	localNode, localOK, err := s.swarmLocalNode()
	if err != nil {
		return sessionV2Authority{}, err
	}
	localSwarmID := strings.TrimSpace(localNode.SwarmID)
	if !localOK || localSwarmID == "" {
		return sessionV2Authority{}, sessionV2AuthorityNotFound("sessions v2 primary local node identity is required")
	}
	if strings.TrimSpace(execution.AuthorityHostSwarmID) != localSwarmID {
		return sessionV2Authority{}, sessionV2StaleAuthority("sessions v2 local-container execution is not owned by this primary runtime")
	}
	if strings.TrimSpace(execution.RuntimeKind) != pebblestore.TopologyRuntimeKindContainer || strings.TrimSpace(execution.AuthorityContainerID) == "" {
		return sessionV2Authority{}, sessionV2InvalidClass("sessions v2 local-container execution must target container runtime authority")
	}
	if strings.TrimSpace(placement.RuntimeKind) != strings.TrimSpace(execution.RuntimeKind) || strings.TrimSpace(placement.AuthorityHostSwarmID) != strings.TrimSpace(execution.AuthorityHostSwarmID) || strings.TrimSpace(placement.AuthorityContainerID) != strings.TrimSpace(execution.AuthorityContainerID) {
		return sessionV2Authority{}, sessionV2StaleAuthority("sessions v2 runtime placement authority mismatch")
	}
	if strings.TrimSpace(placement.RuntimeKind) != pebblestore.TopologyRuntimeKindContainer || strings.TrimSpace(placement.AuthorityHostSwarmID) != localSwarmID || strings.TrimSpace(placement.AuthorityContainerID) == "" {
		return sessionV2Authority{}, sessionV2InvalidClass("sessions v2 local-container placement must be container authority owned by this primary runtime")
	}
	if strings.TrimSpace(execution.WorkspaceBindingID) == "" || strings.TrimSpace(execution.SourceWorkspaceID) == "" || execution.SourceWorkspaceGeneration <= 0 || execution.PlacementGeneration <= 0 || execution.BindingGeneration <= 0 {
		return sessionV2Authority{}, sessionV2StaleAuthority("sessions v2 execution authority identity is incomplete")
	}

	binding, err := s.requireSessionV2WorkspaceBinding(authority)
	if err != nil {
		return sessionV2Authority{}, err
	}
	if strings.TrimSpace(binding.DestinationRuntimeSwarmID) != strings.TrimSpace(execution.RuntimeSwarmID) || strings.TrimSpace(binding.DestinationAuthorityHostSwarmID) != localSwarmID {
		return sessionV2Authority{}, sessionV2StaleAuthority("sessions v2 local-container workspace binding destination mismatch")
	}
	if strings.TrimSpace(binding.DestinationRuntimeKind) != pebblestore.TopologyRuntimeKindContainer || strings.TrimSpace(binding.DestinationContainerID) != strings.TrimSpace(execution.AuthorityContainerID) {
		return sessionV2Authority{}, sessionV2InvalidClass("sessions v2 local-container workspace binding destination must match container authority")
	}
	if strings.TrimSpace(binding.DestinationWorkspacePath) == "" || strings.TrimSpace(execution.RuntimeWorkspacePath) == "" {
		return sessionV2Authority{}, sessionV2StaleAuthority("sessions v2 local-container runtime workspace identity is incomplete")
	}
	authority.Binding = &binding
	return authority, nil
}

func (s *Server) requireSessionV2WorkspaceBinding(authority sessionV2Authority) (pebblestore.TopologyWorkspaceBindingRecord, error) {
	execution := authority.Execution
	binding, bindingOK, err := s.topology.GetWorkspaceBindingForAccount(authority.Principal.AccountScopeID, execution.WorkspaceBindingID)
	if err != nil {
		return pebblestore.TopologyWorkspaceBindingRecord{}, err
	}
	if !bindingOK {
		return pebblestore.TopologyWorkspaceBindingRecord{}, sessionV2AuthorityNotFound("sessions v2 workspace binding %q was not found", execution.WorkspaceBindingID)
	}
	if strings.TrimSpace(binding.BindingID) == "" || strings.TrimSpace(binding.SourceWorkspaceID) == "" || binding.SourceWorkspaceGeneration <= 0 || binding.PlacementGeneration <= 0 || binding.BindingGeneration <= 0 {
		return pebblestore.TopologyWorkspaceBindingRecord{}, sessionV2StaleAuthority("sessions v2 workspace binding authority identity is incomplete")
	}
	if strings.TrimSpace(binding.State) != pebblestore.TopologyWorkspaceBindingStateBound {
		return pebblestore.TopologyWorkspaceBindingRecord{}, sessionV2StaleAuthority("sessions v2 workspace binding is not bound")
	}
	if strings.TrimSpace(binding.AccountScopeID) != strings.TrimSpace(authority.Principal.AccountScopeID) {
		return pebblestore.TopologyWorkspaceBindingRecord{}, sessionV2AccessDenied("sessions v2 workspace binding account scope does not match principal")
	}
	if strings.TrimSpace(binding.BindingID) != strings.TrimSpace(execution.WorkspaceBindingID) {
		return pebblestore.TopologyWorkspaceBindingRecord{}, sessionV2StaleAuthority("sessions v2 workspace binding id mismatch")
	}
	if strings.TrimSpace(binding.AttestedByHostSwarmID) != strings.TrimSpace(authority.Placement.AuthorityHostSwarmID) {
		return pebblestore.TopologyWorkspaceBindingRecord{}, sessionV2StaleAuthority("sessions v2 workspace binding attesting host does not match authority host")
	}
	if binding.PlacementGeneration != execution.PlacementGeneration || binding.BindingGeneration != execution.BindingGeneration {
		return pebblestore.TopologyWorkspaceBindingRecord{}, sessionV2StaleAuthority("sessions v2 workspace binding generation mismatch")
	}
	if strings.TrimSpace(binding.SourceWorkspaceID) != strings.TrimSpace(execution.SourceWorkspaceID) || binding.SourceWorkspaceGeneration != execution.SourceWorkspaceGeneration {
		return pebblestore.TopologyWorkspaceBindingRecord{}, sessionV2StaleAuthority("sessions v2 workspace binding source identity mismatch")
	}
	accessMode := strings.TrimSpace(binding.AccessMode)
	if authority.Mutating {
		if accessMode != pebblestore.TopologyWorkspaceBindingAccessModeReadWrite || !binding.Writable {
			return pebblestore.TopologyWorkspaceBindingRecord{}, sessionV2AccessDenied("sessions v2 workspace binding is read-only")
		}
	} else if accessMode != pebblestore.TopologyWorkspaceBindingAccessModeReadWrite && accessMode != pebblestore.TopologyWorkspaceBindingAccessModeReadOnly {
		return pebblestore.TopologyWorkspaceBindingRecord{}, sessionV2StaleAuthority("sessions v2 workspace binding access mode is invalid")
	}
	return binding, nil
}

func isPrimarySessionV2TUICWDExecution(execution pebblestore.SessionExecutionV2Record) bool {
	return strings.TrimSpace(execution.ExecutionClass) == sessionruntime.SessionExecutionClassPrimary &&
		strings.TrimSpace(execution.WorkspaceBindingID) == "" &&
		strings.HasPrefix(strings.TrimSpace(execution.SourceWorkspaceID), sessionruntime.SessionExecutionTUICWDSourceIDPrefix)
}

func validatePrimarySessionV2TUICWDExecution(execution pebblestore.SessionExecutionV2Record) error {
	if !isPrimarySessionV2TUICWDExecution(execution) {
		return sessionV2StaleAuthority("sessions v2 execution authority identity is incomplete")
	}
	if strings.TrimSpace(execution.SourceWorkspacePath) == "" || strings.TrimSpace(execution.RuntimeWorkspacePath) == "" || strings.TrimSpace(execution.SourceWorkspacePath) != strings.TrimSpace(execution.RuntimeWorkspacePath) {
		return sessionV2StaleAuthority("sessions v2 tui cwd execution workspace identity is incomplete")
	}
	if execution.SourceWorkspaceGeneration != sessionruntime.SessionExecutionTUICWDSourceGeneration || execution.BindingGeneration != sessionruntime.SessionExecutionTUICWDBindingGeneration {
		return sessionV2StaleAuthority("sessions v2 tui cwd execution generation mismatch")
	}
	return nil
}

func (s *Server) handlePrimarySessionV2Get(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	authority, err := s.requirePrimarySessionV2DispatchAuthority(r, sessionID, false)
	if err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !ok {
		writeSessionNotFound(w)
		return
	}
	fields := gitStatusResponseForSession(session)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"session": struct {
			pebblestore.SessionSnapshot
			gitStatusResponseFields
			GitCommitDetected bool                            `json:"git_commit_detected,omitempty"`
			GitCommitCount    int                             `json:"git_commit_count,omitempty"`
			SessionExecution  sessionruntime.SessionExecution `json:"session_execution,omitempty"`
		}{
			SessionSnapshot:         session,
			gitStatusResponseFields: fields,
			GitCommitDetected:       gitCommitDetectedForSession(session, fields),
			GitCommitCount:          gitCommitCountForSession(session, fields),
			SessionExecution:        runtimeSessionsV2ExecutionFromRecord(authority.Execution),
		},
	})
}

func (s *Server) handlePrimarySessionV2Messages(w http.ResponseWriter, r *http.Request, sessionID string) {
	requireWrite := r.Method == http.MethodPost
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if _, err := s.requirePrimarySessionV2DispatchAuthority(r, sessionID, requireWrite); err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		afterSeq, limit, ok := parseAfterSeqAndLimit(w, r, 500)
		if !ok {
			return
		}
		messages, err := s.sessions.ListMessages(sessionID, afterSeq, limit)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "messages": messages})
	case http.MethodPost:
		var req struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		message, updatedSession, event, err := s.sessions.AppendMessage(sessionID, req.Role, req.Content, nil)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if event != nil && s.hub != nil {
			s.hub.Publish(*event)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": message, "session": updatedSession})
	}
}

func parseAfterSeqAndLimit(w http.ResponseWriter, r *http.Request, defaultLimit int) (uint64, int, bool) {
	afterSeq := uint64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("after_seq")); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("after_seq must be an unsigned integer"))
			return 0, 0, false
		}
		afterSeq = parsed
	}
	limit := defaultLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
			return 0, 0, false
		}
		limit = parsed
	}
	return afterSeq, limit, true
}

func parseSessionsV2PositiveLimit(w http.ResponseWriter, r *http.Request, defaultLimit int) (int, bool) {
	limit := defaultLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
			return 0, false
		}
		limit = parsed
	}
	return limit, true
}

func (s *Server) handlePrimarySessionV2Metadata(w http.ResponseWriter, r *http.Request, sessionID string) {
	requireWrite := r.Method == http.MethodPost
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if _, err := s.requirePrimarySessionV2DispatchAuthority(r, sessionID, requireWrite); err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	if r.Method == http.MethodGet {
		session, ok, err := s.sessions.GetSession(sessionID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !ok {
			writeSessionNotFound(w)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "metadata": session.Metadata, "updated_at": session.UpdatedAt})
		return
	}
	var req struct {
		Metadata map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validateSessionsV2Metadata(req.Metadata); err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	session, event, err := s.sessions.UpdateMetadata(sessionID, req.Metadata)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": session})
}

func (s *Server) handlePrimarySessionV2Mode(w http.ResponseWriter, r *http.Request, sessionID string) {
	requireWrite := r.Method == http.MethodPost
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	authority, err := s.requirePrimarySessionV2DispatchAuthority(r, sessionID, requireWrite)
	if err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	if r.Method == http.MethodGet {
		mode, err := s.sessions.GetMode(sessionID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "mode": mode})
		return
	}
	var req struct {
		Mode string `json:"mode"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	profile, profileErr := s.agents.ResolvePrimaryForAccount(authority.Principal.AccountScopeID, "")
	if profileErr != nil {
		writeError(w, http.StatusBadRequest, profileErr)
		return
	}
	requestedMode := sessionruntime.NormalizeMode(req.Mode)
	modeWarning := ""
	if !pebblestore.AgentExitPlanModeEnabled(profile) {
		setting, ok := pebblestore.AgentExecutionSetting(profile)
		if !ok {
			agentName := strings.TrimSpace(profile.Name)
			if agentName == "" {
				agentName = "active primary agent"
			}
			writeError(w, http.StatusBadRequest, fmt.Errorf("%s has plan mode disabled but no execution_setting is configured", agentName))
			return
		}
		if requestedMode != setting {
			modeWarning = fmt.Sprintf("active primary agent %q has plan mode disabled; ignoring requested session mode %q and using execution setting %q", strings.TrimSpace(profile.Name), requestedMode, setting)
		}
		req.Mode = setting
	}
	session, event, err := s.sessions.SetMode(sessionID, req.Mode)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "mode": session.Mode, "updated_at": session.UpdatedAt, "warning": modeWarning})
}

func (s *Server) handlePrimarySessionV2Preference(w http.ResponseWriter, r *http.Request, sessionID string) {
	requireWrite := r.Method == http.MethodPost
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if _, err := s.requirePrimarySessionV2DispatchAuthority(r, sessionID, requireWrite); err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	if r.Method == http.MethodGet {
		pref, err := s.sessions.GetSessionPreference(sessionID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		resolved, err := s.model.ResolvePreference(pref)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, resolved)
		return
	}
	var req struct {
		Provider    *string `json:"provider,omitempty"`
		Model       *string `json:"model,omitempty"`
		Thinking    *string `json:"thinking,omitempty"`
		ServiceTier *string `json:"service_tier,omitempty"`
		ContextMode *string `json:"context_mode,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	pref, event, err := s.sessions.SetSessionPreference(sessionID, sessionruntime.SessionPreferenceUpdate{Provider: req.Provider, Model: req.Model, Thinking: req.Thinking, ServiceTier: req.ServiceTier, ContextMode: req.ContextMode})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	resolved, err := s.model.ResolvePreference(pref)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, resolved)
}

func (s *Server) handlePrimarySessionV2Codex(w http.ResponseWriter, r *http.Request, sessionID string) {
	requireWrite := r.Method == http.MethodPost
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if _, err := s.requirePrimarySessionV2DispatchAuthority(r, sessionID, requireWrite); err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	if r.Method == http.MethodGet {
		config, err := s.sessions.GetCodexConfig(sessionID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, s.codexSessionConfigResponse(sessionID, config))
		return
	}
	var req struct {
		ServiceTier *string `json:"service_tier,omitempty"`
		ContextMode *string `json:"context_mode,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	config, event, err := s.sessions.SetCodexConfig(sessionID, sessionruntime.SessionCodexConfigUpdate{ServiceTier: req.ServiceTier, ContextMode: req.ContextMode})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	writeJSON(w, http.StatusOK, s.codexSessionConfigResponse(sessionID, config))
}

func (s *Server) handlePrimarySessionV2ActivePlan(w http.ResponseWriter, r *http.Request, sessionID string) {
	requireWrite := r.Method == http.MethodPost
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if _, err := s.requirePrimarySessionV2DispatchAuthority(r, sessionID, requireWrite); err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	if r.Method == http.MethodGet {
		plan, ok, err := s.sessions.GetActivePlan(sessionID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !ok {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "has_active": false, "active_plan": nil})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "has_active": true, "active_plan": plan})
		return
	}
	var req struct {
		PlanID string `json:"plan_id"`
		ID     string `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	planID := strings.TrimSpace(req.PlanID)
	if planID == "" {
		planID = strings.TrimSpace(req.ID)
	}
	plan, event, err := s.sessions.SetActivePlan(sessionID, planID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "active_plan": plan})
}

func (s *Server) handlePrimarySessionV2PlanByID(w http.ResponseWriter, r *http.Request, sessionID, tail string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if _, err := s.requirePrimarySessionV2DispatchAuthority(r, sessionID, false); err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	if strings.HasSuffix(tail, "/history") {
		planID := strings.TrimSpace(strings.TrimSuffix(tail, "/history"))
		if planID == "" || strings.Contains(planID, "/") {
			writeError(w, http.StatusBadRequest, errors.New("plan id is required"))
			return
		}
		limit, ok := parseSessionsV2PositiveLimit(w, r, 100)
		if !ok {
			return
		}
		revisions, err := s.sessions.ListPlanRevisions(sessionID, planID, limit)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "plan_id": planID, "count": len(revisions), "revisions": revisions})
		return
	}
	planID := strings.TrimSpace(tail)
	if planID == "" || strings.Contains(planID, "/") {
		writeError(w, http.StatusBadRequest, errors.New("plan id is required"))
		return
	}
	plan, ok, err := s.sessions.GetPlan(sessionID, planID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "plan not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "plan": plan})
}

func (s *Server) handlePrimarySessionV2Plans(w http.ResponseWriter, r *http.Request, sessionID string) {
	requireWrite := r.Method == http.MethodPost
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if _, err := s.requirePrimarySessionV2DispatchAuthority(r, sessionID, requireWrite); err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	if r.Method == http.MethodGet {
		limit, ok := parseSessionsV2PositiveLimit(w, r, 100)
		if !ok {
			return
		}
		plans, activeID, err := s.sessions.ListPlans(sessionID, limit)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "active_plan_id": activeID, "count": len(plans), "plans": plans})
		return
	}
	var req struct {
		ID            string `json:"id"`
		PlanID        string `json:"plan_id"`
		Title         string `json:"title"`
		Plan          string `json:"plan"`
		Status        string `json:"status"`
		ApprovalState string `json:"approval_state"`
		UpdateSummary string `json:"update_summary"`
		UpdateScope   string `json:"update_scope"`
		Scope         string `json:"scope"`
		UpdateKind    string `json:"update_kind"`
		Checkpoint    bool   `json:"checkpoint"`
		Activate      *bool  `json:"activate"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	planID := strings.TrimSpace(req.PlanID)
	if planID == "" {
		planID = strings.TrimSpace(req.ID)
	}
	activate := true
	if req.Activate != nil {
		activate = *req.Activate
	}
	updateScope := strings.TrimSpace(req.UpdateScope)
	if updateScope == "" {
		updateScope = strings.TrimSpace(req.Scope)
	}
	plan, event, err := s.sessions.SavePlanWithMetadata(sessionID, planID, req.Title, req.Plan, req.Status, req.ApprovalState, activate, sessionruntime.PlanSaveMetadata{UpdateSummary: req.UpdateSummary, UpdateScope: updateScope, UpdateKind: req.UpdateKind, Checkpoint: req.Checkpoint})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "plan": plan})
}

func (s *Server) handlePrimarySessionV2Permissions(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.perm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("permission service is not configured"))
		return
	}
	if _, err := s.requirePrimarySessionV2DispatchAuthority(r, sessionID, false); err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	limit, ok := parseSessionsV2PositiveLimit(w, r, 200)
	if !ok {
		return
	}
	var permissions []pebblestore.PermissionRecord
	var err error
	switch status := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status"))); status {
	case "", "all":
		permissions, err = s.perm.ListPermissions(sessionID, limit)
	case pebblestore.PermissionStatusPending:
		permissions, err = s.perm.ListPending(sessionID, limit)
	default:
		writeError(w, http.StatusBadRequest, errors.New("unsupported permission status"))
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "count": len(permissions), "permissions": permissions})
}

func (s *Server) handlePrimarySessionV2PermissionResolve(w http.ResponseWriter, r *http.Request, sessionID, permissionID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.perm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("permission service is not configured"))
		return
	}
	permissionID = strings.Trim(permissionID, "/")
	if permissionID == "" || strings.Contains(permissionID, "/") {
		writeError(w, http.StatusBadRequest, errors.New("permission id is required"))
		return
	}
	if _, err := s.requirePrimarySessionV2DispatchAuthority(r, sessionID, true); err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	var req struct {
		Action            string          `json:"action"`
		Reason            string          `json:"reason"`
		ApprovedArguments json.RawMessage `json:"approved_arguments,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	record, savedRule, err := s.perm.ResolveWithPolicyAndArguments(sessionID, permissionID, req.Action, req.Reason, string(req.ApprovedArguments))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "permission": record, "saved_rule": savedRule})
}

func (s *Server) handlePrimarySessionV2PermissionResolveAll(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.perm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("permission service is not configured"))
		return
	}
	if _, err := s.requirePrimarySessionV2DispatchAuthority(r, sessionID, true); err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	var req struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
		Limit  int    `json:"limit"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resolved, err := s.perm.ResolveAll(sessionID, req.Action, req.Reason, req.Limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "count": len(resolved), "resolved": resolved})
}

func (s *Server) handlePrimarySessionV2Usage(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if _, err := s.requirePrimarySessionV2DispatchAuthority(r, sessionID, false); err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	limit, ok := parseSessionsV2PositiveLimit(w, r, 50)
	if !ok {
		return
	}
	summary, hasSummary, err := s.sessions.GetUsageSummary(sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	turns, err := s.sessions.ListTurnUsage(sessionID, limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var summaryPayload any
	if hasSummary {
		summaryPayload = summary
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "has_usage_summary": hasSummary, "usage_summary": summaryPayload, "turn_usage_records": turns})
}

func (s *Server) handlePrimarySessionV2Run(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	authority, err := s.requirePrimarySessionV2DispatchAuthority(r, sessionID, true)
	if err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	s.handleNativeSessionV2Run(w, r, sessionID, authority.Principal)
}

func (s *Server) handlePrimarySessionV2RunStopLocalContainer(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	authority, err := s.requireSessionV2Authority(r, sessionID, true)
	if err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	if strings.TrimSpace(authority.Execution.ExecutionClass) != sessionruntime.SessionExecutionClassLocalContainer {
		writeSessionsV2Error(w, sessionV2InvalidClass("local-container stop requires local_container execution class"))
		return
	}
	var req sessionruntime.RuntimeSessionStopRequest
	if err := decodeJSON(r, &req); err != nil {
		writeSessionsV2Error(w, sessionV2BadRequest("invalid local-container stop request: %v", err))
		return
	}
	resp, err := s.dispatchLocalContainerSessionV2RunStop(r, authority, req)
	if err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handlePrimarySessionV2RunStopPrimary(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	authority, err := s.requirePrimarySessionV2DispatchAuthority(r, sessionID, true)
	if err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	if s.runner == nil {
		writeError(w, http.StatusInternalServerError, errors.New("run service not configured"))
		return
	}
	if s.runStreams == nil {
		writeError(w, http.StatusInternalServerError, errors.New("run stream manager not configured"))
		return
	}
	var req struct {
		Type          string `json:"type,omitempty"`
		TargetSwarmID string `json:"target_swarm_id"`
		RunID         string `json:"run_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	runID := strings.TrimSpace(req.RunID)
	if runID == "" {
		writeError(w, http.StatusBadRequest, errors.New("run_id is required for stop"))
		return
	}
	if targetSwarmID := strings.TrimSpace(req.TargetSwarmID); targetSwarmID == "" {
		writeError(w, http.StatusBadRequest, errors.New("target_swarm_id is required for primary stop"))
		return
	} else if !strings.EqualFold(targetSwarmID, strings.TrimSpace(authority.Execution.RuntimeSwarmID)) {
		writeSessionsV2Error(w, sessionV2InvalidClass("primary stop target swarm %q does not match primary execution runtime %q", targetSwarmID, authority.Execution.RuntimeSwarmID))
		return
	}
	s.runStreams.setStopReason(runID, "run stopped by user")
	if err := s.runner.StopSessionRun(sessionID, runID, "run stopped by user"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "run_id": runID, "status": "stop_requested", "target_swarm_id": strings.TrimSpace(authority.Execution.RuntimeSwarmID)})
}

func requireSessionV2Mutation(requireMutation func() error) error {
	if requireMutation == nil {
		return nil
	}
	return requireMutation()
}

func (s *Server) handleNativeSessionV2Run(w http.ResponseWriter, r *http.Request, sessionID string, principal identity.Principal) {
	if s.runner == nil {
		writeError(w, http.StatusInternalServerError, errors.New("run service not configured"))
		return
	}
	if s.isShuttingDown() {
		writeError(w, http.StatusServiceUnavailable, errors.New("daemon is shutting down"))
		return
	}
	var req runruntime.RunRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validatePrimarySessionV2RunRequest(req); err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	req = req.Normalized()
	integrationCtx, err := s.applyIntegrationBuilderRunContext(principal, sessionID, &sessionRunRequestAdapter{agentName: func() string { return req.AgentName }, setAgentName: func(value string) { req.AgentName = value }, instructions: func() string { return req.Instructions }, setInstructions: func(value string) { req.Instructions = value }})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.beginActiveRun()
	defer s.endActiveRun()
	result, err := s.runner.RunTurn(identity.ContextWithPrincipal(r.Context(), principal), sessionID, req, runruntime.RunStartMeta{IntegrationFlow: integrationCtx.IntegrationFlow, Principal: principal})
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, runruntime.ErrSessionAlreadyActive) {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	if s.hub != nil {
		for _, event := range result.Events {
			s.hub.Publish(event)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func (s *Server) handlePrimarySessionV2RunStream(w http.ResponseWriter, r *http.Request, sessionID string) {
	authority, err := s.requirePrimarySessionV2DispatchAuthority(r, sessionID, false)
	if err != nil {
		writeSessionsV2Error(w, err)
		return
	}
	s.handleAuthorizedSessionV2RunStream(w, r, sessionID, authority.Principal, func() error {
		_, err := s.requirePrimarySessionV2DispatchAuthority(r, sessionID, true)
		return err
	})
}

func (s *Server) handleAuthorizedSessionV2RunStream(w http.ResponseWriter, r *http.Request, sessionID string, principal identity.Principal, requireMutation func() error) {
	switch r.Method {
	case http.MethodGet:
		s.handleAuthorizedSessionV2RunStreamWebsocket(w, r, sessionID, principal, requireMutation)
	case http.MethodPost:
		s.handleAuthorizedSessionV2RunStreamControl(w, r, sessionID, principal, requireMutation)
	default:
		writeError(w, http.StatusUpgradeRequired, errors.New("run stream requires websocket upgrade (GET) or control POST"))
	}
}

func (s *Server) handleAuthorizedSessionV2RunStreamWebsocket(w http.ResponseWriter, r *http.Request, sessionID string, principal identity.Principal, requireMutation func() error) {
	if s.runner == nil {
		writeError(w, http.StatusInternalServerError, errors.New("run service not configured"))
		return
	}
	if s.runStreams == nil {
		writeError(w, http.StatusInternalServerError, errors.New("run stream manager not configured"))
		return
	}
	if s.isShuttingDown() {
		writeError(w, http.StatusServiceUnavailable, errors.New("daemon is shutting down"))
		return
	}
	conn, err := transportws.Accept(w, r)
	if err != nil {
		log.Printf("sessions v2 run stream websocket accept failed session_id=%s remote_addr=%s path=%s err=%v", sessionID, strings.TrimSpace(r.RemoteAddr), r.URL.Path, err)
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
		log.Printf("sessions v2 run stream websocket initial read failed session_id=%s remote_addr=%s err=%v", sessionID, strings.TrimSpace(r.RemoteAddr), err)
		return
	}
	inbound, err := decodePrimarySessionV2RunStreamInbound(raw)
	if err != nil {
		log.Printf("sessions v2 run stream websocket decode failed session_id=%s remote_addr=%s err=%v", sessionID, strings.TrimSpace(r.RemoteAddr), err)
		s.sendRunStreamControl(conn, runStreamControlMessage{Type: "error", OK: false, SessionID: sessionID, Error: err.Error()})
		return
	}
	switch inbound.Type {
	case "run.start", "start":
		if err := requireSessionV2Mutation(requireMutation); err != nil {
			s.sendRunStreamControl(conn, runStreamControlMessage{Type: "error", OK: false, SessionID: sessionID, Error: err.Error()})
			return
		}
		if err := validatePrimarySessionV2RunRequest(inbound.RunRequest); err != nil {
			s.sendRunStreamControl(conn, runStreamControlMessage{Type: "error", OK: false, SessionID: sessionID, Error: err.Error()})
			return
		}
		inbound.RunRequest = inbound.RunRequest.Normalized()
		s.handleRunStreamStart(conn, sessionID, inbound, principal)
	case "run.resume", "resume":
		// Resume is read-only observation: the outer /run/stream entrypoint
		// already validated read authority, and handleRunStreamResume only
		// subscribes/replays frames for an existing run after a session match.
		s.handleRunStreamResume(conn, sessionID, inbound)
	case "run.stop", "stop":
		if err := requireSessionV2Mutation(requireMutation); err != nil {
			s.sendRunStreamControl(conn, runStreamControlMessage{Type: "error", OK: false, SessionID: sessionID, Error: err.Error()})
			return
		}
		s.handleRunStreamStop(conn, sessionID, inbound)
	default:
		log.Printf("sessions v2 run stream websocket unsupported message session_id=%s remote_addr=%s type=%q", sessionID, strings.TrimSpace(r.RemoteAddr), inbound.Type)
		s.sendRunStreamControl(conn, runStreamControlMessage{Type: "error", OK: false, SessionID: sessionID, Error: fmt.Sprintf("unsupported run stream message type %q", inbound.Type)})
	}
}

func (s *Server) handleAuthorizedSessionV2RunStreamControl(w http.ResponseWriter, r *http.Request, sessionID string, principal identity.Principal, requireMutation func() error) {
	if s.runner == nil {
		writeError(w, http.StatusInternalServerError, errors.New("run service not configured"))
		return
	}
	if s.runStreams == nil {
		writeError(w, http.StatusInternalServerError, errors.New("run stream manager not configured"))
		return
	}
	var inbound runStreamInboundMessage
	if err := decodeJSON(r, &inbound); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	inbound.Type = strings.ToLower(strings.TrimSpace(inbound.Type))
	inbound.RunID = strings.TrimSpace(inbound.RunID)
	switch inbound.Type {
	case "run.resume", "resume":
		// Resume is read-only observation: the outer /run/stream entrypoint
		// already validated read authority. The control POST confirms the
		// existing run belongs to this session without mutating execution state.
		if inbound.RunID == "" {
			writeError(w, http.StatusBadRequest, errors.New("run_id is required for resume"))
			return
		}
		state, sub, _, err := s.runStreams.subscribe(inbound.RunID, inbound.LastSeq)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		defer s.runStreams.unsubscribe(inbound.RunID, sub.id)
		if state.sessionID != sessionID {
			writeError(w, http.StatusBadRequest, errors.New("run/session mismatch"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "run_id": inbound.RunID, "last_seq": inbound.LastSeq, "status": "resume_available"})
	case "run.start", "start":
		if err := requireSessionV2Mutation(requireMutation); err != nil {
			writeSessionsV2Error(w, err)
			return
		}
		if err := validatePrimarySessionV2RunRequest(inbound.RunRequest); err != nil {
			writeSessionsV2Error(w, err)
			return
		}
		inbound.RunRequest = inbound.RunRequest.Normalized()
		state, err := s.runStreams.newRun(sessionID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if state == nil {
			writeError(w, http.StatusInternalServerError, errors.New("unable to allocate run stream"))
			return
		}
		started := s.startRunStreamExecution(state.runID, sessionID, inbound, principal)
		if startErr := <-started; startErr != nil {
			status := http.StatusBadRequest
			if errors.Is(startErr, runruntime.ErrSessionAlreadyActive) {
				status = http.StatusConflict
			}
			writeError(w, status, startErr)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "session_id": sessionID, "run_id": state.runID, "status": "accepted", "background": inbound.RunRequest.Background, "target_kind": strings.TrimSpace(inbound.RunRequest.TargetKind), "target_name": strings.TrimSpace(inbound.RunRequest.TargetName), "owner_transport": "background_api"})
	case "run.stop", "stop":
		if err := requireSessionV2Mutation(requireMutation); err != nil {
			writeSessionsV2Error(w, err)
			return
		}
		if inbound.RunID == "" {
			writeError(w, http.StatusBadRequest, errors.New("run_id is required for stop"))
			return
		}
		s.runStreams.setStopReason(inbound.RunID, "run stopped by user")
		if err := s.runner.StopSessionRun(sessionID, inbound.RunID, "run stopped by user"); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "run_id": inbound.RunID, "status": "stop_requested"})
	default:
		writeError(w, http.StatusBadRequest, fmt.Errorf("unsupported run stream message type %q", inbound.Type))
	}
}
