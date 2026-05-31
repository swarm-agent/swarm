package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	"swarm/packages/swarmd/internal/flowdiaglog"
	"swarm/packages/swarmd/internal/identity"
	remotedeploy "swarm/packages/swarmd/internal/remotedeploy"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

type sessionCreateRequest struct {
	Title                    string         `json:"title"`
	WorkspacePath            string         `json:"workspace_path"`
	HostWorkspacePath        string         `json:"host_workspace_path"`
	RuntimeWorkspacePath     string         `json:"runtime_workspace_path"`
	WorkspaceBindingID       string         `json:"workspace_binding_id,omitempty"`
	WorkspaceName            string         `json:"workspace_name"`
	Mode                     string         `json:"mode"`
	AgentName                string         `json:"agent_name"`
	WorktreeMode             string         `json:"worktree_mode,omitempty"`
	WorktreeUseCurrentBranch *bool          `json:"worktree_use_current_branch,omitempty"`
	WorktreeBaseBranch       string         `json:"worktree_base_branch,omitempty"`
	WorktreeBranchName       string         `json:"worktree_branch_name,omitempty"`
	Metadata                 map[string]any `json:"metadata"`
	Preference               struct {
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
	Route     pebblestore.SessionRouteRecord         `json:"route,omitempty"`
	Principal identity.Principal                     `json:"principal,omitempty"`
}

func (s *Server) routedSessionTarget(principal identity.Principal, sessionID string) (*swarmTarget, bool, error) {
	if s == nil || s.topology == nil {
		return nil, false, nil
	}
	if !principal.Valid() {
		return nil, false, identity.ErrPrincipalRequired
	}
	record, ok, err := s.topology.GetSessionRouteForAccount(principal.AccountScopeID, sessionID)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	runtimeSwarmID := strings.TrimSpace(record.RuntimeSwarmID)
	if runtimeSwarmID == "" {
		return nil, false, errors.New("routed session is missing selected runtime swarm id")
	}
	runtimeRecord, _, err := s.topology.GetRuntimeForAccount(principal.AccountScopeID, runtimeSwarmID)
	if err != nil {
		return nil, false, err
	}
	if s.isLocalSwarmID(runtimeSwarmID) && strings.TrimSpace(runtimeRecord.OwnerHostSwarmID) == "" {
		return nil, false, nil
	}
	hostSwarmID := strings.TrimSpace(record.HostSwarmID)
	placement, placementOK, err := s.topology.GetRuntimePlacementForAccount(principal.AccountScopeID, runtimeSwarmID)
	if err != nil {
		return nil, false, err
	}
	if placementOK {
		if strings.TrimSpace(placement.State) != pebblestore.TopologyRuntimePlacementStateActive {
			return nil, false, errors.New("routed session runtime placement is not active")
		}
		if record.PlacementGeneration > 0 && placement.PlacementGeneration > 0 && record.PlacementGeneration != placement.PlacementGeneration {
			return nil, false, errors.New("routed session placement generation is stale")
		}
		hostSwarmID = firstNonEmpty(hostSwarmID, strings.TrimSpace(placement.AuthorityHostSwarmID))
	}
	if hostSwarmID == "" {
		return nil, false, errors.New("routed session is missing authority host swarm id")
	}
	backendURL := s.backendURLForAuthorityHost(principal.AccountScopeID, hostSwarmID)
	if backendURL == "" {
		return nil, false, errors.New("routed session authority transport is unavailable")
	}
	flowRouteDiagLog("routed_session_target_lookup",
		"session_id", record.SessionID,
		"route_child_swarm_id", record.RuntimeSwarmID,
		"route_child_backend_url_present", false,
		"route_proxy_backend_url", backendURL,
		"route_host_swarm_id", hostSwarmID,
		"route_host_container_id", strings.TrimSpace(record.HostContainerID),
		"route_workspace_binding_id", record.WorkspaceBindingID,
		"route_host_workspace_path", record.HostWorkspacePath,
		"route_runtime_workspace_path", strings.TrimSpace(record.RuntimeWorkspacePath),
	)
	target := &swarmTarget{
		SwarmID:      strings.TrimSpace(record.RuntimeSwarmID),
		Name:         firstNonEmpty(strings.TrimSpace(runtimeRecord.Name), strings.TrimSpace(record.RuntimeSwarmID)),
		Role:         firstNonEmpty(strings.TrimSpace(runtimeRecord.Role), "child"),
		Relationship: firstNonEmpty(strings.TrimSpace(runtimeRecord.Relationship), "child"),
		Kind:         swarmTargetKindForRoutedSession(runtimeRecord),
		DeploymentID: "",
		HostSwarmID:  hostSwarmID,
		Online:       true,
		Selectable:   true,
		Current:      true,
		BackendURL:   backendURL,
		DesktopURL:   strings.TrimSpace(runtimeRecord.DesktopURL),
	}
	return target, true, nil
}

func (s *Server) backendURLForAuthorityHost(accountScopeID, authorityHostSwarmID string) string {
	authorityHostSwarmID = strings.TrimSpace(authorityHostSwarmID)
	if s == nil || authorityHostSwarmID == "" {
		return ""
	}
	if s.topology != nil {
		if runtimeRecord, ok, err := s.topology.GetRuntimeForAccount(accountScopeID, authorityHostSwarmID); err == nil && ok {
			if backendURL := strings.TrimSpace(runtimeRecord.BackendURL); backendURL != "" {
				return backendURL
			}
		}
	}
	if s.swarmNodes != nil {
		if node, ok, err := s.swarmNodes.Get(authorityHostSwarmID); err == nil && ok {
			if backendURL := strings.TrimSpace(node.BackendURL); backendURL != "" {
				return backendURL
			}
		}
	}
	return ""
}

func swarmTargetKindForRoutedSession(runtimeRecord pebblestore.TopologyRuntimeRecord) string {
	if strings.TrimSpace(runtimeRecord.OwnerHostSwarmID) != "" {
		return "mirrored"
	}
	return "remote"
}

func normalizeRoutedSessionBackendURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

func (s *Server) proxyRoutedSessionRequest(w http.ResponseWriter, r *http.Request, sessionID string) bool {
	principal, principalOK := PrincipalFromRequest(r)
	if !principalOK || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return true
	}
	target, ok, err := s.routedSessionTarget(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return true
	}
	if !ok {
		requiresRoute, routeErr := s.sessionRequiresCanonicalStoredRoute(sessionID)
		if routeErr != nil {
			writeError(w, http.StatusBadGateway, routeErr)
			return true
		}
		if requiresRoute {
			writeError(w, http.StatusBadGateway, errors.New("routed session is missing canonical stored route"))
			return true
		}
		return false
	}
	log.Printf("proxy routed session request session_id=%q method=%s path=%q source=stored swarm_id=%q backend_url=%q", strings.TrimSpace(sessionID), r.Method, r.URL.Path, strings.TrimSpace(target.SwarmID), strings.TrimSpace(target.BackendURL))
	flowRouteDiagLog("routed_session_proxy",
		"session_id", sessionID,
		"method", r.Method,
		"path", r.URL.Path,
		"source", "stored",
		"target_swarm_id", target.SwarmID,
		"target_backend_url_present", strings.TrimSpace(target.BackendURL) != "",
	)
	if err := s.proxyRequestToSwarmTarget(w, r, *target); err != nil {
		writeError(w, http.StatusBadGateway, err)
	}
	return true
}

func (s *Server) sessionRequiresCanonicalStoredRoute(sessionID string) (bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false, nil
	}
	if s != nil && s.sessionRoutes != nil {
		if _, ok, err := s.sessionRoutes.Get(sessionID); err != nil {
			return false, err
		} else if ok {
			return true, nil
		}
	}
	if s == nil || s.sessions == nil {
		return false, nil
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok {
		return false, err
	}
	return sessionHasControllerOwnedRoutedMirrorMetadata(session.Metadata), nil
}

func (s *Server) postPeerJSONToSwarmTarget(ctx context.Context, target swarmTarget, path string, payload any, out any) error {
	startedAt := time.Now()
	if s.swarm == nil {
		flowdiaglog.Printf("peer_http_post_no_swarm_service", "target_swarm_id=%q path=%q backend_url_present=%t", target.SwarmID, path, strings.TrimSpace(target.BackendURL) != "")
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
		flowdiaglog.Printf("peer_http_post_auth_token_failed", "target_swarm_id=%q path=%q backend_url_present=%t err=%q", target.SwarmID, path, strings.TrimSpace(target.BackendURL) != "", err.Error())
		return err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		flowdiaglog.Printf("peer_http_post_marshal_failed", "target_swarm_id=%q path=%q err=%q", target.SwarmID, path, err.Error())
		return err
	}
	endpoint := strings.TrimRight(strings.TrimSpace(target.BackendURL), "/") + path
	flowdiaglog.Printf("peer_http_post_request", "source_swarm_id=%q target_swarm_id=%q path=%q endpoint=%q payload_bytes=%d", strings.TrimSpace(state.Node.SwarmID), target.SwarmID, path, endpoint, len(raw))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		flowdiaglog.Printf("peer_http_post_request_build_failed", "target_swarm_id=%q path=%q endpoint=%q err=%q", target.SwarmID, path, endpoint, err.Error())
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(peerAuthSwarmIDHeader, strings.TrimSpace(state.Node.SwarmID))
	req.Header.Set(peerAuthTokenHeader, peerToken)
	principalUserID := ""
	principalAccountScopeID := ""
	principalAttached := false
	if principal, ok := identity.PrincipalFromContext(ctx); ok && principal.Valid() {
		principalUserID = strings.TrimSpace(principal.UserID)
		principalAccountScopeID = strings.TrimSpace(principal.AccountScopeID)
		principalAttached = true
		req.Header.Set("X-Swarm-Principal-User-ID", principalUserID)
		req.Header.Set("X-Swarm-Principal-Account-Scope-ID", principalAccountScopeID)
	}
	flowRouteDiagLog("desktop_routed_peer_open_outbound",
		"target_swarm_id", target.SwarmID,
		"path", path,
		"endpoint", endpoint,
		"source_swarm_id", strings.TrimSpace(state.Node.SwarmID),
		"peer_auth_header", strings.TrimSpace(state.Node.SwarmID) != "" && strings.TrimSpace(peerToken) != "",
		"principal_attached", principalAttached,
		"principal_user_id", principalUserID,
		"principal_account_scope_id", principalAccountScopeID,
	)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("routed peer request failed swarm_id=%q path=%q elapsed_ms=%d err=%v", strings.TrimSpace(target.SwarmID), strings.TrimSpace(path), time.Since(startedAt).Milliseconds(), err)
		flowdiaglog.Printf("peer_http_post_do_failed", "target_swarm_id=%q path=%q endpoint=%q elapsed_ms=%d err=%q", strings.TrimSpace(target.SwarmID), strings.TrimSpace(path), endpoint, time.Since(startedAt).Milliseconds(), err.Error())
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		flowRouteDiagLog("desktop_routed_peer_open_response", "target_swarm_id", target.SwarmID, "path", path, "status", resp.StatusCode, "ok", false)
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&failure)
		if strings.TrimSpace(failure.Error) != "" {
			log.Printf("routed peer request failed swarm_id=%q path=%q status=%d elapsed_ms=%d err=%q", strings.TrimSpace(target.SwarmID), strings.TrimSpace(path), resp.StatusCode, time.Since(startedAt).Milliseconds(), strings.TrimSpace(failure.Error))
			flowdiaglog.Printf("peer_http_post_status_failed", "target_swarm_id=%q path=%q endpoint=%q status=%d elapsed_ms=%d err=%q", strings.TrimSpace(target.SwarmID), strings.TrimSpace(path), endpoint, resp.StatusCode, time.Since(startedAt).Milliseconds(), strings.TrimSpace(failure.Error))
			return errors.New(strings.TrimSpace(failure.Error))
		}
		log.Printf("routed peer request failed swarm_id=%q path=%q status=%d elapsed_ms=%d err=%q", strings.TrimSpace(target.SwarmID), strings.TrimSpace(path), resp.StatusCode, time.Since(startedAt).Milliseconds(), resp.Status)
		flowdiaglog.Printf("peer_http_post_status_failed", "target_swarm_id=%q path=%q endpoint=%q status=%d elapsed_ms=%d err=%q", strings.TrimSpace(target.SwarmID), strings.TrimSpace(path), endpoint, resp.StatusCode, time.Since(startedAt).Milliseconds(), resp.Status)
		return errors.New(resp.Status)
	}
	log.Printf("routed peer request success swarm_id=%q path=%q status=%d elapsed_ms=%d", strings.TrimSpace(target.SwarmID), strings.TrimSpace(path), resp.StatusCode, time.Since(startedAt).Milliseconds())
	flowRouteDiagLog("desktop_routed_peer_open_response", "target_swarm_id", target.SwarmID, "path", path, "status", resp.StatusCode, "ok", true)
	flowdiaglog.Printf("peer_http_post_status_success", "target_swarm_id=%q path=%q endpoint=%q status=%d elapsed_ms=%d", strings.TrimSpace(target.SwarmID), strings.TrimSpace(path), endpoint, resp.StatusCode, time.Since(startedAt).Milliseconds())
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		flowdiaglog.Printf("peer_http_post_decode_response_failed", "target_swarm_id=%q path=%q endpoint=%q status=%d err=%q", strings.TrimSpace(target.SwarmID), strings.TrimSpace(path), endpoint, resp.StatusCode, err.Error())
		return err
	}
	flowdiaglog.Printf("peer_http_post_decode_response_success", "target_swarm_id=%q path=%q endpoint=%q status=%d", strings.TrimSpace(target.SwarmID), strings.TrimSpace(path), endpoint, resp.StatusCode)
	return nil
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
	if s.sessionRoutes == nil {
		writeError(w, http.StatusInternalServerError, errors.New("session route store not configured"))
		return
	}
	var req peerSessionOpenRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	peerSwarmID, peerAuthorized := authorizedPeerSwarmID(r)
	requestPrincipal, requestPrincipalOK := PrincipalFromRequest(r)
	flowRouteDiagLog("desktop_routed_peer_open_received",
		"path", r.URL.Path,
		"session_id", req.SessionID,
		"peer_authorized", peerAuthorized,
		"peer_swarm_id", peerSwarmID,
		"request_principal_ok", requestPrincipalOK,
		"request_principal_valid", requestPrincipal.Valid(),
		"request_principal_user_id", requestPrincipal.UserID,
		"request_principal_account_scope_id", requestPrincipal.AccountScopeID,
		"claim_valid", req.Principal.Valid(),
		"claim_user_id", req.Principal.UserID,
		"claim_account_scope_id", req.Principal.AccountScopeID,
		"route_user_id", req.Route.UserID,
		"route_account_scope_id", req.Route.AccountScopeID,
		"route_host_swarm_id", req.Route.HostSwarmID,
		"route_child_swarm_id", req.Route.ChildSwarmID,
		"route_workspace_binding_id", req.Route.WorkspaceBindingID,
	)
	req.SessionID = strings.TrimSpace(req.SessionID)
	if req.SessionID == "" {
		writeError(w, http.StatusBadRequest, errors.New("session id is required"))
		return
	}
	hostedHostSwarmID := strings.TrimSpace(req.Hosted.HostSwarmID)
	if hostedHostSwarmID == "" {
		writeError(w, http.StatusBadRequest, errors.New("hosted host swarm id is required"))
		return
	}
	routeRecord, err := s.normalizedTerminalPeerSessionOpenRoute(r, req, hostedHostSwarmID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	childReq, err := normalizedTerminalPeerSessionOpenRequest(req.Request, routeRecord, req.Hosted)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	worktreeMode, allowWorktree, err := terminalPeerSessionOpenWorktreeMode(childReq)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	principal, principalOK := requestPrincipal, requestPrincipalOK
	if !principalOK || !principal.Valid() {
		principal, principalOK = s.verifiedPeerSessionOpenPrincipalClaim(r, peerSessionOpenRequest{
			SessionID: req.SessionID,
			Request:   childReq,
			Hosted:    req.Hosted,
			Route:     routeRecord,
			Principal: req.Principal,
		})
		flowRouteDiagLog("desktop_routed_peer_open_principal_claim_verified", "session_id", req.SessionID, "ok", principalOK, "principal_valid", principal.Valid(), "principal_user_id", principal.UserID, "principal_account_scope_id", principal.AccountScopeID)
		if principalOK && principal.Valid() {
			ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, principal)
			ctx = identity.ContextWithPrincipal(ctx, principal)
			r = r.WithContext(ctx)
		}
	}
	if !principalOK || !principal.Valid() {
		flowRouteDiagLog("desktop_routed_peer_open_reject", "session_id", req.SessionID, "reason", "principal_required", "peer_authorized", peerAuthorized, "peer_swarm_id", peerSwarmID, "claim_valid", req.Principal.Valid(), "claim_user_id", req.Principal.UserID, "claim_account_scope_id", req.Principal.AccountScopeID)
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	principal.Type = identity.PrincipalTypeUser
	principal.UserID = strings.TrimSpace(principal.UserID)
	principal.AccountScopeID = strings.TrimSpace(principal.AccountScopeID)
	if req.Principal.Valid() {
		claim := req.Principal
		claim.UserID = strings.TrimSpace(claim.UserID)
		claim.AccountScopeID = strings.TrimSpace(claim.AccountScopeID)
		if claim.UserID != principal.UserID || claim.AccountScopeID != principal.AccountScopeID {
			writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
			return
		}
	}
	if routeRecord.UserID == "" {
		writeError(w, http.StatusBadRequest, errors.New("route user id is required"))
		return
	}
	if routeRecord.UserID != principal.UserID {
		writeError(w, http.StatusBadRequest, errors.New("route user id does not match principal"))
		return
	}
	if routeRecord.AccountScopeID == "" {
		writeError(w, http.StatusBadRequest, errors.New("route account scope id is required"))
		return
	}
	if routeRecord.AccountScopeID != principal.AccountScopeID {
		writeError(w, http.StatusBadRequest, errors.New("route account scope id does not match principal"))
		return
	}
	if err := s.validateTerminalPeerSessionOpenPairing(r, routeRecord, principal); err != nil {
		flowRouteDiagLog("desktop_routed_peer_open_reject", "session_id", req.SessionID, "reason", "pairing_validation", "error", err, "principal_user_id", principal.UserID, "principal_account_scope_id", principal.AccountScopeID, "peer_swarm_id", peerSwarmID, "route_host_swarm_id", routeRecord.HostSwarmID, "route_child_swarm_id", routeRecord.ChildSwarmID)
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	session, _, warning, modeWarning, err := s.createSessionFromRequestWithSessionID(childReq, req.Hosted.WithMetadata(nil), allowWorktree, req.SessionID, principal, true)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validateTerminalPeerSessionOpenRealizedSession(session, worktreeMode); err != nil {
		if cleanupErr := s.sessions.DeleteSession(session.ID); cleanupErr != nil {
			log.Printf("peer terminal session create rollback failed session_id=%q err=%v", session.ID, cleanupErr)
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	routeRecord.CreatedAt = session.CreatedAt
	routeRecord.UpdatedAt = session.UpdatedAt
	if _, err := s.sessionRoutes.Put(routeRecord); err != nil {
		if cleanupErr := s.sessions.DeleteSession(session.ID); cleanupErr != nil {
			log.Printf("peer session route rollback failed session_id=%q err=%v", session.ID, cleanupErr)
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.upsertTopologySessionRoute(routeRecord); err != nil {
		if cleanupErr := s.rollbackHostedSessionCreate(session.ID); cleanupErr != nil {
			log.Printf("peer session route rollback failed session_id=%q err=%v", session.ID, cleanupErr)
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	flowRouteDiagLog("desktop_routed_peer_open_success", "session_id", req.SessionID, "principal_user_id", principal.UserID, "principal_account_scope_id", principal.AccountScopeID, "route_workspace_binding_id", routeRecord.WorkspaceBindingID, "workspace_path", session.WorkspacePath)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"session": session,
		"warning": strings.TrimSpace(strings.Join([]string{warning, modeWarning}, " ")),
	})
}

func normalizedTerminalPeerSessionOpenRequest(req sessionCreateRequest, route pebblestore.SessionRouteRecord, hosted sessionruntime.HostedSessionDescriptor) (sessionCreateRequest, error) {
	req.WorkspacePath = strings.TrimSpace(req.WorkspacePath)
	req.HostWorkspacePath = strings.TrimSpace(req.HostWorkspacePath)
	req.RuntimeWorkspacePath = strings.TrimSpace(req.RuntimeWorkspacePath)
	req.WorkspaceBindingID = strings.TrimSpace(req.WorkspaceBindingID)
	req.WorkspaceName = strings.TrimSpace(req.WorkspaceName)
	runtimeWorkspacePath := strings.TrimSpace(route.RuntimeWorkspacePath)
	if runtimeWorkspacePath == "" {
		return req, errors.New("route runtime workspace path is required")
	}
	if req.WorkspacePath != "" && !strings.EqualFold(req.WorkspacePath, runtimeWorkspacePath) {
		return req, errors.New("request workspace path does not match route runtime workspace path")
	}
	if req.HostWorkspacePath != "" && !strings.EqualFold(req.HostWorkspacePath, runtimeWorkspacePath) {
		return req, errors.New("request host workspace path does not match route runtime workspace path")
	}
	if req.RuntimeWorkspacePath != "" && !strings.EqualFold(req.RuntimeWorkspacePath, runtimeWorkspacePath) {
		return req, errors.New("request runtime workspace path does not match route runtime workspace path")
	}
	if hostedRuntimePath := strings.TrimSpace(hosted.RuntimeWorkspacePath); hostedRuntimePath != "" && !strings.EqualFold(hostedRuntimePath, runtimeWorkspacePath) {
		return req, errors.New("hosted runtime workspace path does not match route runtime workspace path")
	}
	workspaceBindingID := strings.TrimSpace(route.WorkspaceBindingID)
	if workspaceBindingID == "" {
		return req, errors.New("route workspace binding id is required")
	}
	if req.WorkspaceBindingID != "" && !strings.EqualFold(req.WorkspaceBindingID, workspaceBindingID) {
		return req, errors.New("request workspace binding id does not match route workspace binding id")
	}
	if req.WorkspaceName == "" {
		return req, errors.New("workspace_name is required")
	}
	req.WorkspaceBindingID = workspaceBindingID
	req.WorkspacePath = runtimeWorkspacePath
	req.HostWorkspacePath = runtimeWorkspacePath
	req.RuntimeWorkspacePath = runtimeWorkspacePath
	return req, nil
}

func terminalPeerSessionOpenWorktreeMode(req sessionCreateRequest) (string, bool, error) {
	rawMode := strings.TrimSpace(req.WorktreeMode)
	mode := runruntime.NormalizeRunWorktreeMode(rawMode)
	if rawMode == "" || mode == runruntime.RunWorktreeModeInherit {
		return "", false, errors.New("worktree_mode must be explicitly set to on or off")
	}
	if mode == "" {
		return "", false, errors.New("unsupported worktree_mode " + strconv.Quote(rawMode))
	}
	switch mode {
	case runruntime.RunWorktreeModeOn:
		return mode, true, nil
	case runruntime.RunWorktreeModeOff:
		if req.WorktreeUseCurrentBranch != nil || strings.TrimSpace(req.WorktreeBaseBranch) != "" || strings.TrimSpace(req.WorktreeBranchName) != "" {
			return "", false, errors.New("worktree fields are not allowed when worktree_mode is off")
		}
		return mode, false, nil
	default:
		return "", false, errors.New("unsupported worktree_mode " + strconv.Quote(rawMode))
	}
}

func (s *Server) normalizedTerminalPeerSessionOpenRoute(r *http.Request, req peerSessionOpenRequest, hostedHostSwarmID string) (pebblestore.SessionRouteRecord, error) {
	route := req.Route
	route.SessionID = strings.TrimSpace(route.SessionID)
	route.UserID = strings.TrimSpace(route.UserID)
	route.AccountScopeID = strings.TrimSpace(route.AccountScopeID)
	route.ChildSwarmID = strings.TrimSpace(route.ChildSwarmID)
	route.ChildBackendURL = ""
	route.HostSwarmID = strings.TrimSpace(route.HostSwarmID)
	route.HostContainerID = strings.TrimSpace(route.HostContainerID)
	route.HostWorkspacePath = ""
	route.RuntimeWorkspacePath = strings.TrimSpace(route.RuntimeWorkspacePath)
	route.WorkspaceBindingID = strings.TrimSpace(route.WorkspaceBindingID)
	if route.SessionID == "" {
		return route, errors.New("route session id is required")
	}
	if route.SessionID != strings.TrimSpace(req.SessionID) {
		return route, errors.New("route session id does not match request session id")
	}
	if route.HostSwarmID == "" {
		route.HostSwarmID = hostedHostSwarmID
	} else if !strings.EqualFold(route.HostSwarmID, hostedHostSwarmID) {
		return route, errors.New("route host swarm id does not match hosted host swarm id")
	}
	if route.HostSwarmID == "" {
		return route, errors.New("route host swarm id is required")
	}
	peerSwarmID, peerOK := authorizedPeerSwarmID(r)
	if !peerOK || strings.TrimSpace(peerSwarmID) == "" {
		return route, errors.New("authenticated peer swarm id is required")
	}
	if !strings.EqualFold(strings.TrimSpace(peerSwarmID), route.HostSwarmID) {
		return route, errors.New("authenticated peer swarm id does not match route host swarm id")
	}
	hostedChildSwarmID := strings.TrimSpace(req.Hosted.ChildSwarmID)
	if hostedChildSwarmID == "" {
		return route, errors.New("hosted child swarm id is required")
	}
	if route.ChildSwarmID == "" {
		return route, errors.New("route child swarm id is required")
	}
	if !strings.EqualFold(route.ChildSwarmID, hostedChildSwarmID) {
		return route, errors.New("route child swarm id does not match hosted child swarm id")
	}
	localSwarmID := strings.TrimSpace(s.terminalPeerSessionOpenLocalSwarmID())
	if localSwarmID == "" {
		return route, errors.New("local swarm id is required")
	}
	if !strings.EqualFold(route.ChildSwarmID, localSwarmID) {
		return route, errors.New("route child swarm id does not match local swarm id")
	}
	if route.WorkspaceBindingID == "" {
		return route, errors.New("route workspace binding id is required")
	}
	if route.RuntimeWorkspacePath == "" {
		return route, errors.New("route runtime workspace path is required")
	}
	return route, nil
}

func (s *Server) terminalPeerSessionOpenLocalSwarmID() string {
	if s == nil {
		return ""
	}
	if localSwarmID := strings.TrimSpace(s.localSwarmIDFromState()); localSwarmID != "" {
		return localSwarmID
	}
	if s.swarmStore == nil {
		return ""
	}
	localNode, ok, err := s.swarmStore.GetLocalNode()
	if err != nil || !ok {
		return ""
	}
	return strings.TrimSpace(localNode.SwarmID)
}

func (s *Server) validateTerminalPeerSessionOpenPairing(r *http.Request, route pebblestore.SessionRouteRecord, principal identity.Principal) error {
	if s == nil || s.swarmStore == nil {
		return errors.New("local peer pairing is required")
	}
	peerSwarmID, peerOK := authorizedPeerSwarmID(r)
	if !peerOK || strings.TrimSpace(peerSwarmID) == "" {
		return errors.New("authenticated peer swarm id is required")
	}
	pairing, ok, err := s.swarmStore.GetLocalPairing()
	if err != nil {
		return err
	}
	if !ok || !strings.EqualFold(strings.TrimSpace(pairing.PairingState), startupconfig.PairingStatePaired) {
		return errors.New("paired parent is required")
	}
	if !strings.EqualFold(strings.TrimSpace(pairing.ParentSwarmID), strings.TrimSpace(peerSwarmID)) {
		return errors.New("authenticated peer swarm id does not match paired parent swarm id")
	}
	if strings.TrimSpace(pairing.UserID) != principal.UserID || strings.TrimSpace(pairing.AccountScopeID) != principal.AccountScopeID {
		return identity.ErrPrincipalRequired
	}
	if !strings.EqualFold(strings.TrimSpace(route.HostSwarmID), strings.TrimSpace(peerSwarmID)) {
		return errors.New("authenticated peer swarm id does not match route host swarm id")
	}
	return nil
}

func validateTerminalPeerSessionOpenRealizedSession(session pebblestore.SessionSnapshot, worktreeMode string) error {
	switch worktreeMode {
	case runruntime.RunWorktreeModeOn:
		if !session.WorktreeEnabled || strings.TrimSpace(session.WorktreeRootPath) == "" || strings.TrimSpace(session.WorktreeBranch) == "" || strings.TrimSpace(session.WorkspacePath) == "" {
			return errors.New("worktree_mode on did not create canonical worktree session state")
		}
	case runruntime.RunWorktreeModeOff:
		if session.WorktreeEnabled || strings.TrimSpace(session.WorktreeRootPath) != "" || strings.TrimSpace(session.WorktreeBranch) != "" {
			return errors.New("worktree_mode off returned worktree session state")
		}
	default:
		return errors.New("unsupported worktree_mode " + strconv.Quote(strings.TrimSpace(worktreeMode)))
	}
	return nil
}

func syncRoutedSessionRouteWithRealizedSession(route pebblestore.SessionRouteRecord, session pebblestore.SessionSnapshot, requireWorktree bool) (pebblestore.SessionRouteRecord, bool, error) {
	if !session.WorktreeEnabled {
		if strings.TrimSpace(session.WorktreeRootPath) != "" || strings.TrimSpace(session.WorktreeBranch) != "" {
			return route, false, errors.New("regular session returned partial worktree state")
		}
		if requireWorktree {
			return route, false, errors.New("worktree_mode on returned a regular session")
		}
		return route, false, nil
	}
	realizedPath := strings.TrimSpace(session.WorkspacePath)
	if realizedPath == "" {
		return route, false, errors.New("worktree session is missing realized workspace path")
	}
	if strings.TrimSpace(session.WorktreeRootPath) == "" || strings.TrimSpace(session.WorktreeBranch) == "" {
		return route, false, errors.New("worktree session is missing canonical worktree state")
	}
	changed := false
	if realizedPath != strings.TrimSpace(route.RuntimeWorkspacePath) {
		route.RuntimeWorkspacePath = realizedPath
		changed = true
	}
	if !changed {
		return route, false, nil
	}
	now := time.Now().UnixMilli()
	if now > route.UpdatedAt {
		route.UpdatedAt = now
	}
	return route, true, nil
}

func (s *Server) verifiedPeerSessionOpenPrincipalClaim(r *http.Request, req peerSessionOpenRequest) (identity.Principal, bool) {
	claim := req.Principal
	if !claim.Valid() || s == nil {
		flowRouteDiagLog("desktop_routed_peer_open_claim_reject", "reason", "invalid_claim_or_server", "session_id", req.SessionID, "claim_valid", claim.Valid(), "server_nil", s == nil)
		return identity.Principal{}, false
	}
	claim.Type = identity.PrincipalTypeUser
	claim.UserID = strings.TrimSpace(claim.UserID)
	claim.AccountScopeID = strings.TrimSpace(claim.AccountScopeID)
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return identity.Principal{}, false
	}
	if routeSessionID := strings.TrimSpace(req.Route.SessionID); routeSessionID != "" && routeSessionID != sessionID {
		return identity.Principal{}, false
	}
	if routeUserID := strings.TrimSpace(req.Route.UserID); routeUserID != "" && routeUserID != claim.UserID {
		return identity.Principal{}, false
	}
	if routeAccountScopeID := strings.TrimSpace(req.Route.AccountScopeID); routeAccountScopeID != "" && routeAccountScopeID != claim.AccountScopeID {
		return identity.Principal{}, false
	}
	runtimeSwarmID := strings.TrimSpace(req.Route.ChildSwarmID)
	if runtimeSwarmID == "" {
		runtimeSwarmID = strings.TrimSpace(req.Hosted.ChildSwarmID)
	}
	if runtimeSwarmID == "" {
		return identity.Principal{}, false
	}
	if s.topology != nil {
		if _, ok, err := s.topology.GetRuntimeForAccount(claim.AccountScopeID, runtimeSwarmID); err == nil && ok {
			flowRouteDiagLog("desktop_routed_peer_open_claim_accept", "reason", "topology_runtime", "session_id", sessionID, "runtime_swarm_id", runtimeSwarmID, "claim_user_id", claim.UserID, "claim_account_scope_id", claim.AccountScopeID)
			return claim, true
		} else if err != nil {
			flowRouteDiagLog("desktop_routed_peer_open_claim_topology_error", "session_id", sessionID, "runtime_swarm_id", runtimeSwarmID, "error", err)
		}
	}
	if s.verifiedLocalChildPeerSessionOpenClaim(r, req, claim, runtimeSwarmID) {
		flowRouteDiagLog("desktop_routed_peer_open_claim_accept", "reason", "local_child_pairing", "session_id", sessionID, "runtime_swarm_id", runtimeSwarmID, "claim_user_id", claim.UserID, "claim_account_scope_id", claim.AccountScopeID)
		return claim, true
	}
	flowRouteDiagLog("desktop_routed_peer_open_claim_reject", "reason", "no_authoritative_state", "session_id", sessionID, "runtime_swarm_id", runtimeSwarmID, "claim_user_id", claim.UserID, "claim_account_scope_id", claim.AccountScopeID, "topology_configured", s.topology != nil, "swarm_store_configured", s.swarmStore != nil)
	return identity.Principal{}, false
}

func (s *Server) verifiedLocalChildPeerSessionOpenClaim(r *http.Request, req peerSessionOpenRequest, claim identity.Principal, runtimeSwarmID string) bool {
	if s == nil || r == nil || s.swarmStore == nil || !claim.Valid() {
		return false
	}
	// Host/container peer auth proves transport identity only. A forwarded
	// principal claim is accepted here only after it matches the persisted
	// local child pairing account and the authenticated peer is the paired
	// parent for a session explicitly targeting this child runtime.
	localNode, localOK, err := s.swarmStore.GetLocalNode()
	if err != nil {
		return false
	}
	if localOK && strings.TrimSpace(localNode.SwarmID) != "" {
		if !strings.EqualFold(strings.TrimSpace(localNode.SwarmID), runtimeSwarmID) {
			return false
		}
	} else if !s.isLocalSwarmID(runtimeSwarmID) {
		return false
	}
	peerSwarmID, peerOK := authorizedPeerSwarmID(r)
	if !peerOK || strings.TrimSpace(peerSwarmID) == "" {
		return false
	}
	pairing, ok, err := s.swarmStore.GetLocalPairing()
	if err != nil || !ok {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(pairing.PairingState), "paired") {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(pairing.ParentSwarmID), strings.TrimSpace(peerSwarmID)) {
		return false
	}
	if strings.TrimSpace(pairing.UserID) != claim.UserID || strings.TrimSpace(pairing.AccountScopeID) != claim.AccountScopeID {
		return false
	}
	if routeHostSwarmID := strings.TrimSpace(req.Route.HostSwarmID); routeHostSwarmID != "" && !strings.EqualFold(routeHostSwarmID, strings.TrimSpace(peerSwarmID)) {
		return false
	}
	if hostedHostSwarmID := strings.TrimSpace(req.Hosted.HostSwarmID); hostedHostSwarmID != "" && !strings.EqualFold(hostedHostSwarmID, strings.TrimSpace(peerSwarmID)) {
		return false
	}
	return true
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
	r = s.requestWithTrustedSessionPrincipal(r, req.SessionID)
	if _, _, ok := s.verifySessionOwnershipForRequest(w, r, req.SessionID); !ok {
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
	r = s.requestWithTrustedSessionPrincipal(r, req.SessionID)
	if _, _, ok := s.verifySessionOwnershipForRequest(w, r, req.SessionID); !ok {
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
	r = s.requestWithTrustedSessionPrincipal(r, req.SessionID)
	if _, _, ok := s.verifySessionOwnershipForRequest(w, r, req.SessionID); !ok {
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
	r = s.requestWithTrustedSessionPrincipal(r, req.SessionID)
	if _, _, ok := s.verifySessionOwnershipForRequest(w, r, req.SessionID); !ok {
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
	r = s.requestWithTrustedSessionPrincipal(r, req.Lifecycle.SessionID)
	if _, _, ok := s.verifySessionOwnershipForRequest(w, r, req.Lifecycle.SessionID); !ok {
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
	r = s.requestWithTrustedSessionPrincipal(r, req.SessionID)
	if _, _, ok := s.verifySessionOwnershipForRequest(w, r, req.SessionID); !ok {
		return
	}
	if err := s.validateMirroredEventPayloadLifecycle(req.SessionID, req.EventType, req.Payload); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.validateMirroredEventPayloadMessage(req.SessionID, req.EventType, req.Payload); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	env, err := s.sessions.StoreMirroredEvent(req.SessionID, req.EventType, req.Payload, req.CausationID, req.CorrelationID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.storeMirroredEventPayloadLifecycle(req.SessionID, req.EventType, req.Payload); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.storeMirroredEventPayloadMessage(req.SessionID, req.EventType, req.Payload); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if s.hub != nil {
		s.hub.Publish(env)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "event": env})
}

func (s *Server) validateMirroredEventPayloadLifecycle(sessionID, eventType string, payload map[string]any) error {
	_, ok, err := s.decodeMirroredEventPayloadLifecycle(sessionID, eventType, payload)
	if err != nil || !ok {
		return err
	}
	return nil
}

func (s *Server) storeMirroredEventPayloadLifecycle(sessionID, eventType string, payload map[string]any) error {
	lifecycle, ok, err := s.decodeMirroredEventPayloadLifecycle(sessionID, eventType, payload)
	if err != nil || !ok {
		return err
	}
	return s.sessions.StoreMirroredLifecycle(lifecycle)
}

func (s *Server) decodeMirroredEventPayloadLifecycle(sessionID, eventType string, payload map[string]any) (pebblestore.SessionLifecycleSnapshot, bool, error) {
	if s == nil || s.sessions == nil {
		return pebblestore.SessionLifecycleSnapshot{}, false, errors.New("session service not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return pebblestore.SessionLifecycleSnapshot{}, false, errors.New("session id is required")
	}
	rawLifecycle, ok := payload["lifecycle"]
	if !ok || rawLifecycle == nil {
		if mirroredEventRequiresLifecyclePayload(eventType) {
			return pebblestore.SessionLifecycleSnapshot{}, false, fmt.Errorf("event type %q requires lifecycle payload", strings.TrimSpace(eventType))
		}
		return pebblestore.SessionLifecycleSnapshot{}, false, nil
	}
	encoded, err := json.Marshal(rawLifecycle)
	if err != nil {
		return pebblestore.SessionLifecycleSnapshot{}, false, fmt.Errorf("marshal lifecycle payload: %w", err)
	}
	var lifecycle pebblestore.SessionLifecycleSnapshot
	if err := json.Unmarshal(encoded, &lifecycle); err != nil {
		return pebblestore.SessionLifecycleSnapshot{}, false, fmt.Errorf("decode lifecycle payload: %w", err)
	}
	if strings.TrimSpace(lifecycle.SessionID) == "" {
		return pebblestore.SessionLifecycleSnapshot{}, false, errors.New("lifecycle payload session_id is required")
	}
	if !strings.EqualFold(strings.TrimSpace(lifecycle.SessionID), sessionID) {
		return pebblestore.SessionLifecycleSnapshot{}, false, fmt.Errorf("lifecycle payload session_id %q does not match event session_id %q", strings.TrimSpace(lifecycle.SessionID), sessionID)
	}
	return lifecycle, true, nil
}

func (s *Server) validateMirroredEventPayloadMessage(sessionID, eventType string, payload map[string]any) error {
	_, _, ok, err := s.decodeMirroredEventPayloadMessage(sessionID, eventType, payload)
	if err != nil || !ok {
		return err
	}
	return nil
}

func (s *Server) storeMirroredEventPayloadMessage(sessionID, eventType string, payload map[string]any) error {
	message, session, ok, err := s.decodeMirroredEventPayloadMessage(sessionID, eventType, payload)
	if err != nil || !ok {
		return err
	}
	_, err = s.sessions.StoreMirroredMessage(session, message)
	return err
}

func (s *Server) decodeMirroredEventPayloadMessage(sessionID, eventType string, payload map[string]any) (pebblestore.MessageSnapshot, pebblestore.SessionSnapshot, bool, error) {
	if s == nil || s.sessions == nil {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, false, errors.New("session service not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, false, errors.New("session id is required")
	}
	rawMessage, ok := payload["message"]
	if !ok || rawMessage == nil {
		if mirroredEventRequiresMessagePayload(eventType) {
			return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, false, fmt.Errorf("event type %q requires message payload", strings.TrimSpace(eventType))
		}
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, false, nil
	}
	encoded, err := json.Marshal(rawMessage)
	if err != nil {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, false, fmt.Errorf("marshal message payload: %w", err)
	}
	var message pebblestore.MessageSnapshot
	if err := json.Unmarshal(encoded, &message); err != nil {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, false, fmt.Errorf("decode message payload: %w", err)
	}
	if strings.TrimSpace(message.SessionID) == "" {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, false, errors.New("message payload session_id is required")
	}
	if !strings.EqualFold(strings.TrimSpace(message.SessionID), sessionID) {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, false, fmt.Errorf("message payload session_id %q does not match event session_id %q", strings.TrimSpace(message.SessionID), sessionID)
	}
	if message.GlobalSeq == 0 {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, false, errors.New("message payload global_seq is required")
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, false, err
	}
	if !ok {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, false, fmt.Errorf("session %q not found", sessionID)
	}
	return message, session, true, nil
}

func mirroredEventRequiresLifecyclePayload(eventType string) bool {
	return strings.TrimSpace(eventType) == "session.lifecycle.updated"
}

func mirroredEventRequiresMessagePayload(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "run.message.stored", "run.message.updated":
		return true
	default:
		return false
	}
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

func (s *Server) decodeSessionCreateRequest(r *http.Request) (sessionCreateRequest, identity.Principal, bool, error) {
	var req sessionCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		return sessionCreateRequest{}, identity.Principal{}, false, err
	}
	req.WorkspacePath = strings.TrimSpace(req.WorkspacePath)
	req.HostWorkspacePath = strings.TrimSpace(req.HostWorkspacePath)
	req.RuntimeWorkspacePath = strings.TrimSpace(req.RuntimeWorkspacePath)
	req.WorkspaceBindingID = strings.TrimSpace(req.WorkspaceBindingID)
	req.WorkspaceName = strings.TrimSpace(req.WorkspaceName)
	principal, principalOK := PrincipalFromRequest(r)
	return req, principal, principalOK, nil
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

func (s *Server) resolveAccountRoutedSessionWorkspaceBinding(principal identity.Principal, req sessionCreateRequest, target swarmTarget) (pebblestore.TopologyWorkspaceBindingRecord, bool, error) {
	if s == nil || s.topology == nil || !principal.Valid() || strings.TrimSpace(target.SwarmID) == "" {
		return pebblestore.TopologyWorkspaceBindingRecord{}, false, nil
	}
	requestedBindingID := strings.TrimSpace(req.WorkspaceBindingID)
	if requestedBindingID == "" {
		return pebblestore.TopologyWorkspaceBindingRecord{}, false, nil
	}
	binding, ok, err := s.topology.GetWorkspaceBindingForAccount(principal.AccountScopeID, requestedBindingID)
	if err != nil {
		return pebblestore.TopologyWorkspaceBindingRecord{}, false, err
	}
	if !ok {
		return pebblestore.TopologyWorkspaceBindingRecord{}, false, fmt.Errorf("routed session workspace binding %q was not found", requestedBindingID)
	}
	if err := s.validateAccountSessionWorkspaceBinding(principal, binding, strings.TrimSpace(target.SwarmID), "routed session"); err != nil {
		return pebblestore.TopologyWorkspaceBindingRecord{}, false, err
	}
	return binding, true, nil
}

func (s *Server) validateAccountSessionWorkspaceBinding(principal identity.Principal, binding pebblestore.TopologyWorkspaceBindingRecord, selectedRuntimeSwarmID, contextLabel string) error {
	strictWorkspaceIdentity := s != nil && s.workspace != nil && strings.TrimSpace(binding.SourceWorkspaceID) != "" && binding.SourceWorkspaceGeneration > 0
	contextLabel = strings.TrimSpace(contextLabel)
	if contextLabel == "" {
		contextLabel = "session"
	}
	if !principal.Valid() {
		return identity.ErrPrincipalRequired
	}
	if strings.TrimSpace(selectedRuntimeSwarmID) == "" {
		return errors.New(contextLabel + " selected runtime swarm id is required")
	}
	if strings.TrimSpace(binding.BindingID) == "" {
		return errors.New(contextLabel + " workspace binding is missing binding id")
	}
	if !strings.EqualFold(strings.TrimSpace(binding.AccountScopeID), strings.TrimSpace(principal.AccountScopeID)) {
		return errors.New(contextLabel + " workspace binding account scope does not match principal")
	}
	if strings.TrimSpace(binding.State) != "" && !strings.EqualFold(strings.TrimSpace(binding.State), pebblestore.TopologyWorkspaceBindingStateBound) {
		return errors.New(contextLabel + " workspace binding is not bound")
	}
	if !strings.EqualFold(strings.TrimSpace(binding.DestinationRuntimeSwarmID), strings.TrimSpace(selectedRuntimeSwarmID)) {
		return errors.New(contextLabel + " workspace binding does not match selected runtime swarm id")
	}
	if strings.TrimSpace(binding.DestinationWorkspacePath) == "" {
		return errors.New(contextLabel + " workspace binding is missing destination workspace path")
	}
	if strings.TrimSpace(binding.SourceWorkspaceID) == "" && strictWorkspaceIdentity {
		return errors.New(contextLabel + " workspace binding is missing source workspace id")
	}
	if binding.SourceWorkspaceGeneration <= 0 && strictWorkspaceIdentity {
		return errors.New(contextLabel + " workspace binding is missing source workspace generation")
	}
	if binding.PlacementGeneration <= 0 && strictWorkspaceIdentity {
		return errors.New(contextLabel + " workspace binding is missing placement generation")
	}
	if binding.BindingGeneration <= 0 && strictWorkspaceIdentity {
		return errors.New(contextLabel + " workspace binding is missing binding generation")
	}
	if strings.TrimSpace(binding.DestinationAuthorityHostSwarmID) == "" && strictWorkspaceIdentity {
		return errors.New(contextLabel + " workspace binding is missing destination authority host swarm id")
	}
	if strings.TrimSpace(binding.DestinationRuntimeKind) == "" && strictWorkspaceIdentity {
		return errors.New(contextLabel + " workspace binding is missing destination runtime kind")
	}
	if strings.TrimSpace(binding.AttestedByHostSwarmID) == "" && strictWorkspaceIdentity {
		return errors.New(contextLabel + " workspace binding is missing attesting host swarm id")
	}
	if !strings.EqualFold(strings.TrimSpace(binding.AttestedByHostSwarmID), strings.TrimSpace(binding.DestinationAuthorityHostSwarmID)) {
		return errors.New(contextLabel + " workspace binding attesting host does not match authority host")
	}
	if strictWorkspaceIdentity {
		workspaceEntry, ok, err := s.workspace.GetByWorkspaceIDForPrincipal(principal, binding.SourceWorkspaceID)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New(contextLabel + " source workspace was not found")
		}
		if strings.TrimSpace(workspaceEntry.State) != "" && !strings.EqualFold(strings.TrimSpace(workspaceEntry.State), "active") {
			return errors.New(contextLabel + " source workspace is not active")
		}
		if workspaceEntry.WorkspaceGeneration != binding.SourceWorkspaceGeneration {
			return errors.New(contextLabel + " workspace binding source generation does not match workspace")
		}
	}
	return nil
}

func validateRoutedSessionCreateMetadata(metadata map[string]any) error {
	if len(metadata) == 0 {
		return nil
	}
	for _, key := range []string{
		sessionruntime.HostedSessionMetadataHostWorkspacePath,
		sessionruntime.HostedSessionMetadataRuntimeWorkspacePath,
		"swarm_routed_host_workspace_path",
		"swarm_routed_runtime_workspace_path",
		"swarm_route_id",
		"swarm_routed_workspace_binding_id",
	} {
		if _, ok := metadata[key]; ok {
			return fmt.Errorf("routed session create metadata must not include route authority key %q", key)
		}
	}
	return nil
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

func (s *Server) createSessionFromRequest(req sessionCreateRequest, principal identity.Principal, principalOK bool, overrideMetadata map[string]any, allowWorktree bool) (pebblestore.SessionSnapshot, *pebblestore.EventEnvelope, string, string, error) {
	return s.createSessionFromRequestWithSessionID(req, overrideMetadata, allowWorktree, "", principal, principalOK)
}

func (s *Server) createSessionFromRequestWithSessionID(req sessionCreateRequest, overrideMetadata map[string]any, allowWorktree bool, sessionIDOverride string, principalAndOK ...any) (pebblestore.SessionSnapshot, *pebblestore.EventEnvelope, string, string, error) {
	principal := identity.Principal{}
	principalOK := false
	if len(principalAndOK) >= 2 {
		if typed, ok := principalAndOK[0].(identity.Principal); ok {
			principal = typed
		}
		if typed, ok := principalAndOK[1].(bool); ok {
			principalOK = typed
		}
	}
	createMetadata := mergeSessionCreateMetadata(req.Metadata, overrideMetadata)
	workspacePath := strings.TrimSpace(req.HostWorkspacePath)
	if descriptor, hosted := sessionruntime.HostedSessionFromMetadata(createMetadata); hosted {
		if runtimeWorkspacePath := strings.TrimSpace(descriptor.RuntimeWorkspacePath); runtimeWorkspacePath != "" {
			workspacePath = runtimeWorkspacePath
		} else if !allowWorktree && strings.TrimSpace(req.WorkspacePath) != "" {
			workspacePath = strings.TrimSpace(req.WorkspacePath)
		}
	}
	createOptions := sessionruntime.CreateSessionOptions{
		Title:         req.Title,
		WorkspacePath: workspacePath,
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
	if principalOK && principal.Valid() {
		createOptions.UserID = principal.UserID
		createOptions.AccountScopeID = principal.AccountScopeID
	}
	requestedWorktreeMode := strings.TrimSpace(req.WorktreeMode)
	requestedWorktreeBaseBranch := strings.TrimSpace(req.WorktreeBaseBranch)
	requestedWorktreeBranchName := strings.TrimSpace(req.WorktreeBranchName)
	requestedWorktreeUseCurrentBranch := req.WorktreeUseCurrentBranch
	modeWarning := ""
	if s.agents == nil {
		return pebblestore.SessionSnapshot{}, nil, "", "", errors.New("agent service not configured")
	}
	var profile pebblestore.AgentProfile
	var profileErr error
	if principalOK && principal.Valid() {
		profile, profileErr = s.agents.ResolveAgentForAccount(principal.AccountScopeID, strings.TrimSpace(req.AgentName))
	} else {
		profile, profileErr = s.agents.ResolveAgent(strings.TrimSpace(req.AgentName))
	}
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
	}, createMetadata)
	warning := ""
	if allowWorktree {
		nextWarning, worktreeErr := s.applySessionCreateWorktree(&createOptions, sessionID, requestedWorktreeMode, requestedWorktreeUseCurrentBranch, requestedWorktreeBaseBranch, requestedWorktreeBranchName, principal, principalOK)
		if worktreeErr != nil {
			return pebblestore.SessionSnapshot{}, nil, "", "", worktreeErr
		}
		if runruntime.NormalizeRunWorktreeMode(requestedWorktreeMode) == runruntime.RunWorktreeModeOn && createOptions.Worktree == nil {
			return pebblestore.SessionSnapshot{}, nil, "", "", errors.New("worktree_mode on did not allocate a worktree")
		}
		warning = nextWarning
		if descriptor, hosted := sessionruntime.HostedSessionFromMetadata(createOptions.Metadata); hosted && strings.TrimSpace(createOptions.WorkspacePath) != "" {
			if createOptions.Worktree != nil || strings.TrimSpace(descriptor.RuntimeWorkspacePath) == "" {
				descriptor.RuntimeWorkspacePath = strings.TrimSpace(createOptions.WorkspacePath)
			}
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
		if runruntime.NormalizeRunWorktreeMode(requestedWorktreeMode) == runruntime.RunWorktreeModeOn {
			if !session.WorktreeEnabled || strings.TrimSpace(session.WorktreeRootPath) == "" || strings.TrimSpace(session.WorktreeBranch) == "" {
				return pebblestore.SessionSnapshot{}, nil, "", "", errors.New("worktree_mode on did not create canonical worktree session state")
			}
		}
		if session.WorktreeEnabled {
			if descriptor, hosted := sessionruntime.HostedSessionFromMetadata(session.Metadata); hosted && strings.TrimSpace(session.WorkspacePath) != "" {
				descriptor.RuntimeWorkspacePath = strings.TrimSpace(session.WorkspacePath)
				updatedMetadata := descriptor.WithMetadata(session.Metadata)
				updated, _, updateErr := s.sessions.UpdateMetadata(session.ID, updatedMetadata)
				if updateErr != nil {
					return pebblestore.SessionSnapshot{}, nil, "", "", updateErr
				}
				session = updated
			}
		}
	}
	return session, event, warning, modeWarning, nil
}

func (s *Server) applySessionCreateWorktree(createOptions *sessionruntime.CreateSessionOptions, sessionID, rawRequestedMode string, requestedUseCurrentBranch *bool, requestedBaseBranch, requestedBranchName string, principal identity.Principal, principalOK bool) (string, error) {
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

	if !principalOK || !principal.Valid() {
		switch requestedMode {
		case "", runruntime.RunWorktreeModeInherit, runruntime.RunWorktreeModeOff:
			return "", nil
		default:
			return "", identity.ErrPrincipalRequired
		}
	}
	config, cfgErr := s.worktrees.GetConfigForPrincipal(principal, createOptions.WorkspacePath)
	if cfgErr != nil {
		return "", cfgErr
	}
	switch requestedMode {
	case "", runruntime.RunWorktreeModeInherit:
		if !config.Enabled {
			return "", nil
		}
		return s.allocateSessionCreateDetachedWorkspace(createOptions, sessionID, func() (worktreeruntime.Allocation, error) {
			return s.worktrees.AllocateDetachedWorkspaceForPrincipal(principal, createOptions.WorkspacePath, sessionID)
		})
	case runruntime.RunWorktreeModeOff:
		return "", nil
	case runruntime.RunWorktreeModeOn:
		baseBranch := strings.TrimSpace(requestedBaseBranch)
		if requestedUseCurrentBranch != nil && *requestedUseCurrentBranch {
			baseBranch = ""
		}
		if requestedUseCurrentBranch != nil && !*requestedUseCurrentBranch && baseBranch == "" {
			return "", errors.New("worktree_base_branch is required when worktree_use_current_branch is false")
		}
		branchName := strings.TrimSpace(requestedBranchName)
		return s.allocateSessionCreateDetachedWorkspace(createOptions, sessionID, func() (worktreeruntime.Allocation, error) {
			return s.worktrees.AllocateDetachedWorkspaceRequestedForPrincipal(principal, createOptions.WorkspacePath, sessionID, baseBranch, branchName)
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
		return "", allocErr
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
