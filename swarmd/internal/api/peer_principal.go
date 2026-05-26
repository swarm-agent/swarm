package api

import (
	"context"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Server) requestWithPeerSessionPrincipal(r *http.Request) *http.Request {
	if r == nil {
		return nil
	}
	if principal, ok := PrincipalFromRequest(r); ok && principal.Valid() {
		return r
	}
	if !isTrustedPeerOrLocalTransport(r) {
		return r
	}
	path := ""
	if r.URL != nil {
		path = r.URL.Path
	}
	return s.requestWithTrustedSessionPrincipal(r, peerSessionIDFromRequestPath(path))
}

func (s *Server) requestWithTrustedSessionPrincipal(r *http.Request, sessionID string) *http.Request {
	if r == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	path := ""
	if r.URL != nil {
		path = r.URL.Path
	}
	if principal, ok := PrincipalFromRequest(r); ok && principal.Valid() {
		principalSessionID := strings.TrimSpace(principal.SessionID)
		if sessionID == "" || principalSessionID == sessionID {
			return r
		}
		flowRouteDiagLog("trusted_session_principal_existing_mismatch", "path", path, "requested_session_id", sessionID, "principal_session_id", principalSessionID, "principal_user_id", principal.UserID, "principal_account_scope_id", principal.AccountScopeID, "principal_scope_source", principal.AccountScopeSource)
	}
	principal, ok := s.trustedSessionPrincipalForRequest(r, sessionID)
	if !ok || !principal.Valid() {
		flowRouteDiagLog("trusted_session_principal_not_attached", "path", path, "session_id", sessionID, "ok", ok, "principal_valid", principal.Valid(), "principal_user_id", principal.UserID, "principal_account_scope_id", principal.AccountScopeID)
		return r
	}
	flowRouteDiagLog("trusted_session_principal_attached", "path", path, "session_id", sessionID, "principal_user_id", principal.UserID, "principal_account_scope_id", principal.AccountScopeID)
	ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, principal)
	ctx = identity.ContextWithPrincipal(ctx, principal)
	return r.WithContext(ctx)
}

func (s *Server) trustedSessionPrincipalForRequest(r *http.Request, sessionID string) (identity.Principal, bool) {
	if s == nil || r == nil {
		flowRouteDiagLog("trusted_session_principal_reject", "reason", "nil_server_or_request", "session_id", sessionID)
		return identity.Principal{}, false
	}
	path := ""
	if r.URL != nil {
		path = r.URL.Path
	}
	peerSwarmID, authorizedPeer := authorizedPeerSwarmID(r)
	trustedTransport := isTrustedPeerOrLocalTransport(r)
	flowRouteDiagLog("trusted_session_principal_start", "path", path, "session_id", sessionID, "trusted_transport", trustedTransport, "authorized_peer", authorizedPeer, "peer_swarm_id", peerSwarmID, "local_transport", isLocalTransportRequest(r))
	if !trustedTransport {
		flowRouteDiagLog("trusted_session_principal_reject", "reason", "untrusted_transport", "path", path, "session_id", sessionID, "peer_swarm_id", peerSwarmID)
		return identity.Principal{}, false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		principal, ok := s.trustedPairingPrincipalForPeerRequest(r)
		flowRouteDiagLog("trusted_session_principal_pairing", "path", path, "ok", ok, "principal_valid", principal.Valid(), "principal_user_id", principal.UserID, "principal_account_scope_id", principal.AccountScopeID, "peer_swarm_id", peerSwarmID)
		return principal, ok
	}
	// Host/container peer auth and the local transport prove transport identity
	// only. Forwarded X-Swarm-Principal-* headers are claims, not authority;
	// the receiving runtime may mint request principal context only from
	// persisted account-owned session/route state for the addressed session.
	userID := ""
	accountScopeID := ""
	if s.sessions != nil {
		session, found, err := s.sessions.GetSession(sessionID)
		if err != nil {
			flowRouteDiagLog("trusted_session_principal_reject", "reason", "session_lookup_error", "path", path, "session_id", sessionID, "error", err)
			return identity.Principal{}, false
		}
		if found {
			userID = strings.TrimSpace(session.UserID)
			accountScopeID = strings.TrimSpace(session.AccountScopeID)
		}
		flowRouteDiagLog("trusted_session_principal_session_lookup", "path", path, "session_id", sessionID, "found", found, "session_user_id", userID, "session_account_scope_id", accountScopeID)
	} else {
		flowRouteDiagLog("trusted_session_principal_session_lookup", "path", path, "session_id", sessionID, "found", false, "reason", "sessions_store_nil")
	}
	routeFound := false
	if s.sessionRoutes != nil {
		route, found, err := s.sessionRoutes.Get(sessionID)
		if err != nil {
			flowRouteDiagLog("trusted_session_principal_reject", "reason", "route_lookup_error", "path", path, "session_id", sessionID, "error", err)
			return identity.Principal{}, false
		}
		if found {
			routeFound = true
			transportMatches := peerSessionTransportMatchesRoute(r, route.ChildSwarmID, route.HostSwarmID)
			pairedParentMatches := s.peerSessionTransportMatchesPairedParentForRoute(r, route)
			flowRouteDiagLog("trusted_session_principal_route_lookup", "path", path, "session_id", sessionID, "found", found, "route_user_id", route.UserID, "route_account_scope_id", route.AccountScopeID, "route_child_swarm_id", route.ChildSwarmID, "route_host_swarm_id", route.HostSwarmID, "peer_swarm_id", peerSwarmID, "transport_matches_route", transportMatches, "paired_parent_matches_route", pairedParentMatches)
			if !transportMatches && !pairedParentMatches {
				flowRouteDiagLog("trusted_session_principal_reject", "reason", "route_transport_mismatch", "path", path, "session_id", sessionID, "peer_swarm_id", peerSwarmID, "route_child_swarm_id", route.ChildSwarmID, "route_host_swarm_id", route.HostSwarmID)
				return identity.Principal{}, false
			}
			routeUserID := strings.TrimSpace(route.UserID)
			routeAccountScopeID := strings.TrimSpace(route.AccountScopeID)
			if userID == "" && routeUserID != "" {
				userID = routeUserID
			} else if routeUserID != "" && routeUserID != userID {
				flowRouteDiagLog("trusted_session_principal_reject", "reason", "route_user_mismatch", "path", path, "session_id", sessionID, "session_user_id", userID, "route_user_id", routeUserID)
				return identity.Principal{}, false
			}
			if accountScopeID == "" && routeAccountScopeID != "" {
				accountScopeID = routeAccountScopeID
			} else if routeAccountScopeID != "" && routeAccountScopeID != accountScopeID {
				flowRouteDiagLog("trusted_session_principal_reject", "reason", "route_account_scope_mismatch", "path", path, "session_id", sessionID, "session_account_scope_id", accountScopeID, "route_account_scope_id", routeAccountScopeID)
				return identity.Principal{}, false
			}
		} else {
			flowRouteDiagLog("trusted_session_principal_route_lookup", "path", path, "session_id", sessionID, "found", found, "peer_swarm_id", peerSwarmID)
		}
	} else {
		flowRouteDiagLog("trusted_session_principal_route_lookup", "path", path, "session_id", sessionID, "found", false, "reason", "session_routes_store_nil", "peer_swarm_id", peerSwarmID)
	}
	if !routeFound {
		flowRouteDiagLog("trusted_session_principal_reject", "reason", "route_not_found", "path", path, "session_id", sessionID, "peer_swarm_id", peerSwarmID)
		return identity.Principal{}, false
	}
	principal := identity.Principal{
		Type:               identity.PrincipalTypeUser,
		UserID:             userID,
		AccountScopeID:     accountScopeID,
		AccountScopeSource: identity.AccountScopeSourceSession,
		SessionID:          sessionID,
	}
	if !principal.Valid() {
		flowRouteDiagLog("trusted_session_principal_reject", "reason", "principal_invalid", "path", path, "session_id", sessionID, "principal_user_id", principal.UserID, "principal_account_scope_id", principal.AccountScopeID)
		return identity.Principal{}, false
	}
	flowRouteDiagLog("trusted_session_principal_success", "path", path, "session_id", sessionID, "principal_user_id", principal.UserID, "principal_account_scope_id", principal.AccountScopeID)
	return principal, true
}

func (s *Server) trustedPairingPrincipalForPeerRequest(r *http.Request) (identity.Principal, bool) {
	if s == nil || r == nil || s.swarmStore == nil {
		return identity.Principal{}, false
	}
	peerSwarmID, authorizedPeer := authorizedPeerSwarmID(r)
	if !authorizedPeer && !isLocalTransportRequest(r) {
		return identity.Principal{}, false
	}
	pairing, ok, err := s.swarmStore.GetLocalPairing()
	if err != nil || !ok {
		return identity.Principal{}, false
	}
	if authorizedPeer {
		parentSwarmID := strings.TrimSpace(pairing.ParentSwarmID)
		if parentSwarmID == "" || !strings.EqualFold(strings.TrimSpace(peerSwarmID), parentSwarmID) {
			return identity.Principal{}, false
		}
	}
	// Peer/local transport authenticates only the channel. The request principal
	// for non-session managed-host routes is minted from the persisted local
	// pairing identity established during managed pairing, not from forwarded
	// X-Swarm-Principal-* header claims.
	principal := identity.Principal{
		Type:               identity.PrincipalTypeUser,
		UserID:             strings.TrimSpace(pairing.UserID),
		AccountScopeID:     strings.TrimSpace(pairing.AccountScopeID),
		AccountScopeSource: identity.AccountScopeSourceServerState,
	}
	if !principal.Valid() {
		return identity.Principal{}, false
	}
	return principal, true
}

func isTrustedPeerOrLocalTransport(r *http.Request) bool {
	if r == nil {
		return false
	}
	if _, authorizedPeer := authorizedPeerSwarmID(r); authorizedPeer {
		return true
	}
	return isLocalTransportRequest(r)
}

func peerSessionTransportMatchesRoute(r *http.Request, childSwarmID, hostSwarmID string) bool {
	if isLocalTransportRequest(r) {
		return true
	}
	peerSwarmID, authorizedPeer := authorizedPeerSwarmID(r)
	if !authorizedPeer {
		return false
	}
	peerSwarmID = strings.TrimSpace(peerSwarmID)
	childSwarmID = strings.TrimSpace(childSwarmID)
	hostSwarmID = strings.TrimSpace(hostSwarmID)
	if childSwarmID == "" && hostSwarmID == "" {
		return true
	}
	return strings.EqualFold(peerSwarmID, childSwarmID) || strings.EqualFold(peerSwarmID, hostSwarmID)
}

func (s *Server) localSwarmIDFromState() string {
	if s == nil || s.swarm == nil {
		return ""
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return ""
	}
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(state.Node.SwarmID)
}

func (s *Server) peerSessionTransportMatchesPairedParentForRoute(r *http.Request, route pebblestore.SessionRouteRecord) bool {
	if s == nil || r == nil || s.swarmStore == nil {
		return false
	}
	peerSwarmID, authorizedPeer := authorizedPeerSwarmID(r)
	if !authorizedPeer || strings.TrimSpace(peerSwarmID) == "" {
		return false
	}
	localSwarmID := strings.TrimSpace(s.localSwarmIDFromState())
	if localSwarmID == "" {
		localNode, localOK, err := s.swarmStore.GetLocalNode()
		if err != nil || !localOK {
			return false
		}
		localSwarmID = strings.TrimSpace(localNode.SwarmID)
	}
	if !strings.EqualFold(localSwarmID, strings.TrimSpace(route.ChildSwarmID)) && !strings.EqualFold(localSwarmID, strings.TrimSpace(route.HostSwarmID)) {
		return false
	}
	pairing, pairingOK, err := s.swarmStore.GetLocalPairing()
	if err != nil || !pairingOK || !strings.EqualFold(strings.TrimSpace(pairing.PairingState), "paired") {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(pairing.ParentSwarmID), strings.TrimSpace(peerSwarmID)) {
		return false
	}
	return strings.TrimSpace(pairing.UserID) == strings.TrimSpace(route.UserID) && strings.TrimSpace(pairing.AccountScopeID) == strings.TrimSpace(route.AccountScopeID)
}

func peerSessionIDFromRequestPath(path string) string {
	path = strings.TrimSpace(path)
	const prefix = "/v1/sessions/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	rest := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	if rest == "" {
		return ""
	}
	if slash := strings.Index(rest, "/"); slash >= 0 {
		rest = rest[:slash]
	}
	return strings.TrimSpace(rest)
}
