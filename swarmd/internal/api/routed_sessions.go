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

	"swarm/packages/swarmd/internal/flowdiaglog"
	"swarm/packages/swarmd/internal/identity"
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
	retired, err := s.retireStaleRoutedTopologySessionTarget(record)
	if err != nil {
		return nil, false, err
	}
	if retired {
		return nil, false, nil
	}
	runtimeSwarmID := strings.TrimSpace(record.RuntimeSwarmID)
	if runtimeSwarmID == "" || strings.TrimSpace(record.BackendURL) == "" {
		return nil, false, errors.New("routed session is missing canonical topology route details")
	}
	if s.isLocalSwarmID(runtimeSwarmID) {
		return nil, false, nil
	}
	runtimeRecord, _, err := s.topology.GetRuntimeForAccount(principal.AccountScopeID, runtimeSwarmID)
	if err != nil {
		return nil, false, err
	}
	var binding pebblestore.TopologyWorkspaceBindingRecord
	if strings.TrimSpace(record.WorkspaceBindingID) != "" {
		var err error
		binding, _, err = s.topology.GetWorkspaceBindingForAccount(principal.AccountScopeID, record.WorkspaceBindingID)
		if err != nil {
			return nil, false, err
		}
	}
	hostSwarmID := firstNonEmpty(strings.TrimSpace(record.HostSwarmID), strings.TrimSpace(binding.DestinationHostSwarmID), strings.TrimSpace(runtimeRecord.OwnerHostSwarmID))
	backendURL := strings.TrimSpace(record.BackendURL)
	if hostSwarmID != "" && !s.isLocalSwarmID(hostSwarmID) && isLoopbackBackendURL(backendURL) {
		if ownerRuntime, ok, err := s.topology.GetRuntimeForAccount(principal.AccountScopeID, hostSwarmID); err != nil {
			return nil, false, err
		} else if ok {
			if ownerBackendURL := strings.TrimSpace(ownerRuntime.BackendURL); ownerBackendURL != "" {
				backendURL = ownerBackendURL
			}
		}
	}
	flowRouteDiagLog("routed_session_target_lookup",
		"session_id", record.SessionID,
		"route_child_swarm_id", record.RuntimeSwarmID,
		"route_child_backend_url_present", strings.TrimSpace(record.BackendURL) != "",
		"route_child_backend_url_loopback", isLoopbackBackendURL(record.BackendURL),
		"route_proxy_backend_url", backendURL,
		"route_host_swarm_id", hostSwarmID,
		"route_host_container_id", firstNonEmpty(strings.TrimSpace(record.HostContainerID), strings.TrimSpace(binding.DestinationContainerID), strings.TrimSpace(runtimeRecord.OwnerHostContainerID)),
		"route_workspace_binding_id", record.WorkspaceBindingID,
		"route_host_workspace_path", record.HostWorkspacePath,
		"route_runtime_workspace_path", firstNonEmpty(strings.TrimSpace(record.RuntimeWorkspacePath), strings.TrimSpace(binding.DestinationWorkspacePath)),
	)
	target := &swarmTarget{
		SwarmID:      strings.TrimSpace(record.RuntimeSwarmID),
		Name:         firstNonEmpty(strings.TrimSpace(runtimeRecord.Name), strings.TrimSpace(record.RuntimeSwarmID)),
		Role:         firstNonEmpty(strings.TrimSpace(runtimeRecord.Role), "child"),
		Relationship: firstNonEmpty(strings.TrimSpace(runtimeRecord.Relationship), "child"),
		Kind:         firstNonEmpty(strings.TrimSpace(binding.LegacyTargetKind), swarmTargetKindForRoutedSession(runtimeRecord)),
		DeploymentID: strings.TrimPrefix(strings.TrimSpace(binding.BindingID), "binding:replica:"),
		HostSwarmID:  hostSwarmID,
		Online:       true,
		Selectable:   true,
		Current:      true,
		BackendURL:   backendURL,
		DesktopURL:   strings.TrimSpace(runtimeRecord.DesktopURL),
	}
	return target, true, nil
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
	if err := s.deleteTopologySessionRoute(record.SessionID); err != nil {
		return false, err
	}
	log.Printf("retired stale routed session session_id=%q old_child_swarm_id=%q replacement_child_swarm_id=%q child_backend_url=%q", strings.TrimSpace(record.SessionID), strings.TrimSpace(record.ChildSwarmID), replacementChildSwarmID, normalizeRoutedSessionBackendURL(record.ChildBackendURL))
	return true, nil
}

func (s *Server) retireStaleRoutedTopologySessionTarget(record pebblestore.TopologySessionRouteRecord) (bool, error) {
	if s == nil || s.topology == nil {
		return false, nil
	}
	replacementChildSwarmID, err := s.replacementChildSwarmIDForRoutedSession(pebblestore.SessionRouteRecord{
		SessionID:            record.SessionID,
		ChildSwarmID:         record.RuntimeSwarmID,
		ChildBackendURL:      record.BackendURL,
		HostWorkspacePath:    record.HostWorkspacePath,
		RuntimeWorkspacePath: record.RuntimeWorkspacePath,
		CreatedAt:            record.CreatedAt,
		UpdatedAt:            record.UpdatedAt,
	})
	if err != nil {
		return false, err
	}
	if replacementChildSwarmID == "" {
		return false, nil
	}
	if err := s.deleteTopologySessionRoute(record.SessionID); err != nil {
		return false, err
	}
	if s.sessionRoutes != nil {
		if err := s.sessionRoutes.Delete(record.SessionID); err != nil {
			return false, err
		}
	}
	log.Printf("retired stale routed session session_id=%q old_child_swarm_id=%q replacement_child_swarm_id=%q child_backend_url=%q", strings.TrimSpace(record.SessionID), strings.TrimSpace(record.RuntimeSwarmID), replacementChildSwarmID, normalizeRoutedSessionBackendURL(record.BackendURL))
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
		if err := s.deleteTopologySessionRoute(record.SessionID); err != nil {
			return err
		}
		log.Printf("retired stale routed session session_id=%q old_child_swarm_id=%q replacement_child_swarm_id=%q child_backend_url=%q", strings.TrimSpace(record.SessionID), recordChildSwarmID, childSwarmID, childBackendURL)
	}
	return nil
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
	routeSource := "stored"
	if !ok {
		routeSource = "request"
		target, err = s.currentRemoteSwarmTargetForRequest(r)
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return true
		}
		if target == nil {
			return false
		}
	}
	log.Printf("proxy routed session request session_id=%q method=%s path=%q source=%s swarm_id=%q backend_url=%q", strings.TrimSpace(sessionID), r.Method, r.URL.Path, routeSource, strings.TrimSpace(target.SwarmID), strings.TrimSpace(target.BackendURL))
	flowRouteDiagLog("routed_session_proxy",
		"session_id", sessionID,
		"method", r.Method,
		"path", r.URL.Path,
		"source", routeSource,
		"target_swarm_id", target.SwarmID,
		"target_backend_url_present", strings.TrimSpace(target.BackendURL) != "",
	)
	if err := s.proxyRequestToSwarmTarget(w, r, *target); err != nil {
		writeError(w, http.StatusBadGateway, err)
	}
	return true
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
	if principal, ok := identity.PrincipalFromContext(ctx); ok && principal.Valid() {
		req.Header.Set("X-Swarm-Principal-User-ID", strings.TrimSpace(principal.UserID))
		req.Header.Set("X-Swarm-Principal-Account-Scope-ID", strings.TrimSpace(principal.AccountScopeID))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("routed peer request failed swarm_id=%q path=%q elapsed_ms=%d err=%v", strings.TrimSpace(target.SwarmID), strings.TrimSpace(path), time.Since(startedAt).Milliseconds(), err)
		flowdiaglog.Printf("peer_http_post_do_failed", "target_swarm_id=%q path=%q endpoint=%q elapsed_ms=%d err=%q", strings.TrimSpace(target.SwarmID), strings.TrimSpace(path), endpoint, time.Since(startedAt).Milliseconds(), err.Error())
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
			flowdiaglog.Printf("peer_http_post_status_failed", "target_swarm_id=%q path=%q endpoint=%q status=%d elapsed_ms=%d err=%q", strings.TrimSpace(target.SwarmID), strings.TrimSpace(path), endpoint, resp.StatusCode, time.Since(startedAt).Milliseconds(), strings.TrimSpace(failure.Error))
			return errors.New(strings.TrimSpace(failure.Error))
		}
		log.Printf("routed peer request failed swarm_id=%q path=%q status=%d elapsed_ms=%d err=%q", strings.TrimSpace(target.SwarmID), strings.TrimSpace(path), resp.StatusCode, time.Since(startedAt).Milliseconds(), resp.Status)
		flowdiaglog.Printf("peer_http_post_status_failed", "target_swarm_id=%q path=%q endpoint=%q status=%d elapsed_ms=%d err=%q", strings.TrimSpace(target.SwarmID), strings.TrimSpace(path), endpoint, resp.StatusCode, time.Since(startedAt).Milliseconds(), resp.Status)
		return errors.New(resp.Status)
	}
	log.Printf("routed peer request success swarm_id=%q path=%q status=%d elapsed_ms=%d", strings.TrimSpace(target.SwarmID), strings.TrimSpace(path), resp.StatusCode, time.Since(startedAt).Milliseconds())
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
	var req peerSessionOpenRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	if req.SessionID == "" {
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
	principal, principalOK := PrincipalFromRequest(r)
	if !principalOK || !principal.Valid() {
		principal, principalOK = s.verifiedPeerSessionOpenPrincipalClaim(r, req)
		if principalOK && principal.Valid() {
			ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, principal)
			ctx = identity.ContextWithPrincipal(ctx, principal)
			r = r.WithContext(ctx)
		}
	}
	if !principalOK || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	principal.Type = identity.PrincipalTypeUser
	principal.UserID = strings.TrimSpace(principal.UserID)
	principal.AccountScopeID = strings.TrimSpace(principal.AccountScopeID)
	// req.Principal is retained only as a forwarded claim for old callers.
	// Account authority on this peer boundary comes from persisted account-owned
	// session/topology state; a mismatched claim is rejected even when a request
	// principal was already established by the peer middleware.
	if req.Principal.Valid() {
		claim := req.Principal
		claim.UserID = strings.TrimSpace(claim.UserID)
		claim.AccountScopeID = strings.TrimSpace(claim.AccountScopeID)
		if claim.UserID != principal.UserID || claim.AccountScopeID != principal.AccountScopeID {
			writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
			return
		}
	}
	if routeUserID := strings.TrimSpace(req.Route.UserID); routeUserID != "" && routeUserID != principal.UserID {
		writeError(w, http.StatusBadRequest, errors.New("route user id does not match principal"))
		return
	}
	if routeAccountScopeID := strings.TrimSpace(req.Route.AccountScopeID); routeAccountScopeID != "" && routeAccountScopeID != principal.AccountScopeID {
		writeError(w, http.StatusBadRequest, errors.New("route account scope id does not match principal"))
		return
	}
	session, _, warning, modeWarning, err := s.createSessionFromRequestWithSessionID(childReq, req.Hosted.WithMetadata(nil), false, req.SessionID, principal, true)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Route.SessionID) != "" {
		routeRecord := req.Route
		if routeSessionID := strings.TrimSpace(routeRecord.SessionID); routeSessionID != req.SessionID {
			writeError(w, http.StatusBadRequest, errors.New("route session id does not match request session id"))
			return
		}
		routeRecord.UserID = principal.UserID
		routeRecord.AccountScopeID = principal.AccountScopeID
		if strings.TrimSpace(routeRecord.HostSwarmID) == "" {
			routeRecord.HostSwarmID = strings.TrimSpace(req.Hosted.HostSwarmID)
		}
		if strings.TrimSpace(routeRecord.HostWorkspacePath) == "" {
			routeRecord.HostWorkspacePath = strings.TrimSpace(req.Hosted.HostWorkspacePath)
		}
		if strings.TrimSpace(routeRecord.RuntimeWorkspacePath) == "" {
			routeRecord.RuntimeWorkspacePath = strings.TrimSpace(req.Hosted.RuntimeWorkspacePath)
		}
		if routeRecord.CreatedAt == 0 {
			routeRecord.CreatedAt = session.CreatedAt
		}
		if routeRecord.UpdatedAt == 0 {
			routeRecord.UpdatedAt = session.UpdatedAt
		}
		if s.sessionRoutes == nil {
			if cleanupErr := s.sessions.DeleteSession(session.ID); cleanupErr != nil {
				log.Printf("peer session route rollback failed session_id=%q err=%v", session.ID, cleanupErr)
			}
			writeError(w, http.StatusInternalServerError, errors.New("session route store not configured"))
			return
		}
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
		if s.shouldForwardPeerSessionOpenToRoutedChild(routeRecord) {
			if err := s.forwardPeerSessionOpenToRoutedChild(r.Context(), req, routeRecord); err != nil {
				if cleanupErr := s.rollbackHostedSessionCreate(session.ID); cleanupErr != nil {
					log.Printf("peer session child forward rollback failed session_id=%q err=%v", session.ID, cleanupErr)
				}
				writeError(w, http.StatusBadGateway, err)
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"session": session,
		"warning": strings.TrimSpace(strings.Join([]string{warning, modeWarning}, " ")),
	})
}

func (s *Server) shouldForwardPeerSessionOpenToRoutedChild(route pebblestore.SessionRouteRecord) bool {
	if s == nil {
		return false
	}
	childSwarmID := strings.TrimSpace(route.ChildSwarmID)
	childBackendURL := strings.TrimSpace(route.ChildBackendURL)
	if childSwarmID == "" || childBackendURL == "" {
		return false
	}
	return !s.isLocalSwarmID(childSwarmID)
}

func (s *Server) forwardPeerSessionOpenToRoutedChild(ctx context.Context, req peerSessionOpenRequest, route pebblestore.SessionRouteRecord) error {
	if s == nil {
		return errors.New("server is not configured")
	}
	route.SessionID = strings.TrimSpace(route.SessionID)
	route.ChildSwarmID = strings.TrimSpace(route.ChildSwarmID)
	route.ChildBackendURL = strings.TrimSpace(route.ChildBackendURL)
	route.HostSwarmID = strings.TrimSpace(route.HostSwarmID)
	route.HostWorkspacePath = strings.TrimSpace(route.HostWorkspacePath)
	route.RuntimeWorkspacePath = strings.TrimSpace(route.RuntimeWorkspacePath)
	if route.SessionID == "" || route.ChildSwarmID == "" || route.ChildBackendURL == "" {
		return errors.New("routed child session open requires session id, child swarm id, and child backend url")
	}
	forwardReq := req
	forwardReq.SessionID = route.SessionID
	forwardReq.Route = route
	forwardReq.Hosted.ChildSwarmID = route.ChildSwarmID
	if route.HostSwarmID != "" {
		forwardReq.Hosted.HostSwarmID = route.HostSwarmID
	}
	if route.HostWorkspacePath != "" {
		forwardReq.Hosted.HostWorkspacePath = route.HostWorkspacePath
	}
	if route.RuntimeWorkspacePath != "" {
		forwardReq.Hosted.RuntimeWorkspacePath = route.RuntimeWorkspacePath
		forwardReq.Request.WorkspacePath = route.RuntimeWorkspacePath
		forwardReq.Request.HostWorkspacePath = route.RuntimeWorkspacePath
		forwardReq.Request.RuntimeWorkspacePath = route.RuntimeWorkspacePath
	}
	childTarget := swarmTarget{
		SwarmID:      route.ChildSwarmID,
		Name:         route.ChildSwarmID,
		Role:         "child",
		Relationship: "child",
		Kind:         "container",
		HostSwarmID:  route.HostSwarmID,
		Online:       true,
		Selectable:   true,
		BackendURL:   route.ChildBackendURL,
	}
	flowRouteDiagLog("peer_session_open_forward_child", "session_id", route.SessionID, "child_swarm_id", route.ChildSwarmID, "child_backend_url", route.ChildBackendURL, "host_swarm_id", route.HostSwarmID, "runtime_workspace_path", route.RuntimeWorkspacePath)
	var childResp struct {
		OK      bool                        `json:"ok"`
		Session pebblestore.SessionSnapshot `json:"session"`
		Warning string                      `json:"warning,omitempty"`
	}
	if err := s.postPeerJSONToSwarmTarget(ctx, childTarget, "/v1/swarm/peer/sessions/open", forwardReq, &childResp); err != nil {
		flowRouteDiagLog("peer_session_open_forward_child_failed", "session_id", route.SessionID, "child_swarm_id", route.ChildSwarmID, "child_backend_url", route.ChildBackendURL, "error", err)
		return err
	}
	flowRouteDiagLog("peer_session_open_forward_child_success", "session_id", route.SessionID, "child_swarm_id", route.ChildSwarmID, "child_session_id", childResp.Session.ID, "warning", childResp.Warning)
	return nil
}

func (s *Server) verifiedPeerSessionOpenPrincipalClaim(r *http.Request, req peerSessionOpenRequest) (identity.Principal, bool) {
	claim := req.Principal
	if !claim.Valid() || s == nil || s.topology == nil {
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
	if _, ok, err := s.topology.GetRuntimeForAccount(claim.AccountScopeID, runtimeSwarmID); err == nil && ok {
		return claim, true
	}
	if s.verifiedLocalChildPeerSessionOpenClaim(r, req, claim, runtimeSwarmID) {
		return claim, true
	}
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

func (s *Server) decodeSessionCreateRequest(r *http.Request) (sessionCreateRequest, identity.Principal, bool, error) {
	var req sessionCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		return sessionCreateRequest{}, identity.Principal{}, false, err
	}
	principal, principalOK := PrincipalFromRequest(r)
	if strings.TrimSpace(req.HostWorkspacePath) == "" {
		req.HostWorkspacePath = strings.TrimSpace(req.WorkspacePath)
	}
	if strings.TrimSpace(req.RuntimeWorkspacePath) == "" {
		req.RuntimeWorkspacePath = firstNonEmpty(strings.TrimSpace(req.WorkspacePath), strings.TrimSpace(req.HostWorkspacePath))
	}
	if strings.TrimSpace(req.HostWorkspacePath) == "" {
		if !principalOK {
			return sessionCreateRequest{}, identity.Principal{}, false, identity.ErrPrincipalRequired
		}
		current, ok, err := s.workspace.CurrentBindingForPrincipal(principal)
		if err != nil {
			return sessionCreateRequest{}, identity.Principal{}, false, err
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
	return req, principal, principalOK, nil
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
	if _, hosted := sessionruntime.HostedSessionFromMetadata(createMetadata); hosted {
		workspacePath = firstNonEmpty(strings.TrimSpace(req.RuntimeWorkspacePath), workspacePath)
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
		nextWarning, worktreeErr := s.applySessionCreateWorktree(&createOptions, sessionID, requestedWorktreeMode, principal, principalOK)
		if worktreeErr != nil {
			return pebblestore.SessionSnapshot{}, nil, "", "", worktreeErr
		}
		warning = nextWarning
		if descriptor, hosted := sessionruntime.HostedSessionFromMetadata(createOptions.Metadata); hosted && strings.TrimSpace(createOptions.WorkspacePath) != "" {
			if strings.TrimSpace(descriptor.RuntimeWorkspacePath) == "" {
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
	}
	return session, event, warning, modeWarning, nil
}

func (s *Server) applySessionCreateWorktree(createOptions *sessionruntime.CreateSessionOptions, sessionID, rawRequestedMode string, principal identity.Principal, principalOK bool) (string, error) {
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
