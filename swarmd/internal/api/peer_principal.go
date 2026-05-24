package api

import (
	"context"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
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
	if principal, ok := PrincipalFromRequest(r); ok && principal.Valid() {
		return r
	}
	principal, ok := s.trustedSessionPrincipalForRequest(r, sessionID)
	if !ok || !principal.Valid() {
		return r
	}
	ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, principal)
	ctx = identity.ContextWithPrincipal(ctx, principal)
	return r.WithContext(ctx)
}

func (s *Server) trustedSessionPrincipalForRequest(r *http.Request, sessionID string) (identity.Principal, bool) {
	if s == nil || r == nil {
		return identity.Principal{}, false
	}
	if !isTrustedPeerOrLocalTransport(r) {
		return identity.Principal{}, false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return s.trustedPairingPrincipalForPeerRequest(r)
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
			return identity.Principal{}, false
		}
		if found {
			userID = strings.TrimSpace(session.UserID)
			accountScopeID = strings.TrimSpace(session.AccountScopeID)
		}
	}
	routeFound := false
	if s.sessionRoutes != nil {
		route, found, err := s.sessionRoutes.Get(sessionID)
		if err != nil {
			return identity.Principal{}, false
		}
		if found {
			routeFound = true
			if !peerSessionTransportMatchesRoute(r, route.ChildSwarmID, route.HostSwarmID) {
				return identity.Principal{}, false
			}
			routeUserID := strings.TrimSpace(route.UserID)
			routeAccountScopeID := strings.TrimSpace(route.AccountScopeID)
			if userID == "" && routeUserID != "" {
				userID = routeUserID
			} else if routeUserID != "" && routeUserID != userID {
				return identity.Principal{}, false
			}
			if accountScopeID == "" && routeAccountScopeID != "" {
				accountScopeID = routeAccountScopeID
			} else if routeAccountScopeID != "" && routeAccountScopeID != accountScopeID {
				return identity.Principal{}, false
			}
		}
	}
	if !routeFound {
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
		return identity.Principal{}, false
	}
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
