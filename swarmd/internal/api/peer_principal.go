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
	if _, authorizedPeer := authorizedPeerSwarmID(r); !authorizedPeer {
		return r
	}
	if s == nil {
		return r
	}
	path := ""
	if r.URL != nil {
		path = r.URL.Path
	}
	sessionID := peerSessionIDFromRequestPath(path)
	if sessionID == "" {
		return r
	}
	userID := strings.TrimSpace(r.Header.Get("X-Swarm-Principal-User-ID"))
	accountScopeID := strings.TrimSpace(r.Header.Get("X-Swarm-Principal-Account-Scope-ID"))
	if s.sessions != nil {
		session, found, err := s.sessions.GetSession(sessionID)
		if err != nil {
			return r
		}
		if found {
			sessionUserID := strings.TrimSpace(session.UserID)
			sessionAccountScopeID := strings.TrimSpace(session.AccountScopeID)
			if sessionUserID != "" && sessionAccountScopeID != "" {
				userID = sessionUserID
				accountScopeID = sessionAccountScopeID
			}
		}
	}
	if (userID == "" || accountScopeID == "") && s.sessionRoutes != nil {
		route, ok, err := s.sessionRoutes.Get(sessionID)
		if err != nil {
			return r
		}
		if ok {
			userID = strings.TrimSpace(route.UserID)
			accountScopeID = strings.TrimSpace(route.AccountScopeID)
		}
	}
	principal := identity.Principal{
		Type:               identity.PrincipalTypeUser,
		UserID:             userID,
		AccountScopeID:     accountScopeID,
		AccountScopeSource: identity.AccountScopeSourceSession,
		SessionID:          sessionID,
	}
	if !principal.Valid() {
		return r
	}
	ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, principal)
	ctx = identity.ContextWithPrincipal(ctx, principal)
	return r.WithContext(ctx)
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
