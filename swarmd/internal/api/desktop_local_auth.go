package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
)

const (
	desktopLocalSessionCookieName = "swarm_desktop_session"
	desktopLocalSessionTTL        = identity.LocalProductSessionTTL
)

type desktopLocalAuthContextKey string

type productActorContextKey string

type productPrincipalContextKey string

const (
	desktopLocalAuthIssuedTokenKey    desktopLocalAuthContextKey = "desktop-local-auth-issued-token"
	localTransportAuthEnabledKey      desktopLocalAuthContextKey = "local-transport-auth-enabled"
	productActorRequestContextKey     productActorContextKey     = "product-actor"
	productPrincipalRequestContextKey productPrincipalContextKey = "product-principal"
)

type desktopLocalSessionManager struct {
	server *Server
}

func newDesktopLocalSessionManager() *desktopLocalSessionManager {
	return &desktopLocalSessionManager{}
}

func (m *desktopLocalSessionManager) Ensure(_ time.Time) (string, time.Time, error) {
	if m == nil || m.server == nil || m.server.identitySessions == nil {
		return "", time.Time{}, errors.New("legacy singleton desktop local session token has been removed; use product JWT sessions")
	}
	issued, err := m.server.identitySessions.IssueForCurrentSelection()
	if err != nil {
		return "", time.Time{}, err
	}
	return issued.Token, issued.ExpiresAt, nil
}

func (m *desktopLocalSessionManager) Validate(token string, _ time.Time) bool {
	if m == nil || m.server == nil || m.server.identitySessions == nil {
		return false
	}
	_, err := m.server.identitySessions.Validate(token)
	return err == nil
}

func (s *Server) withDesktopLocalSession(next http.Handler) http.Handler {
	if s == nil || s.desktopLocalSessions == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if shouldBootstrapDesktopLocalSession(r) {
			var err error
			r, err = s.issueDesktopLocalSession(w, r)
			if err != nil {
				if errors.Is(err, identity.ErrProductIdentityRequired) {
					next.ServeHTTP(w, r)
					return
				}
				status := http.StatusInternalServerError
				if errors.Is(err, identity.ErrSessionServiceNotConfigured) {
					status = http.StatusUnauthorized
				}
				writeError(w, status, err)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func shouldUseDesktopLocalSessionAuth(r *http.Request) bool {
	return isSameOriginBrowserRequest(r) && isLocalDesktopBrowserRequest(r)
}

func isLocalDesktopBrowserRequest(r *http.Request) bool {
	ip := remoteRequestIP(r)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	if ip.To4() != nil {
		for _, candidate := range detectLANAddresses() {
			candidateIP := net.ParseIP(strings.TrimSpace(candidate))
			if candidateIP == nil {
				continue
			}
			if candidateIP.Equal(ip) {
				return true
			}
		}
	}
	tailscale := detectTailscale()
	for _, candidate := range tailscale.IPs {
		candidateIP := net.ParseIP(strings.TrimSpace(candidate))
		if candidateIP == nil {
			continue
		}
		if candidateIP.Equal(ip) {
			return true
		}
	}
	return false
}

func markLocalTransportRequest(r *http.Request) *http.Request {
	if r == nil {
		return nil
	}
	ctx := context.WithValue(r.Context(), localTransportAuthEnabledKey, true)
	return r.WithContext(ctx)
}

func shouldBootstrapDesktopLocalSession(r *http.Request) bool {
	if !shouldUseDesktopLocalSessionAuth(r) {
		return false
	}
	return shouldServeDesktopAsset(r)
}

func (s *Server) issueDesktopLocalSession(w http.ResponseWriter, r *http.Request) (*http.Request, error) {
	if s == nil || s.identitySessions == nil {
		return r, identity.ErrSessionServiceNotConfigured
	}
	issued, err := s.identitySessions.IssueForCurrentSelection()
	if err != nil {
		return r, err
	}
	http.SetCookie(w, buildDesktopLocalSessionCookie(issued.Token, issued.ExpiresAt, requestScheme(r) == "https"))
	if r != nil {
		r = requestWithActorContext(r.WithContext(context.WithValue(r.Context(), desktopLocalAuthIssuedTokenKey, issued.Token)), issued.Actor)
	}
	return r, nil
}

func desktopLocalSessionTokenFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if issued, _ := r.Context().Value(desktopLocalAuthIssuedTokenKey).(string); strings.TrimSpace(issued) != "" {
		return strings.TrimSpace(issued)
	}
	cookie, err := r.Cookie(desktopLocalSessionCookieName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cookie.Value)
}

func buildDesktopLocalSessionCookie(token string, expiresAt time.Time, secure bool) *http.Cookie {
	maxAge := int(time.Until(expiresAt).Seconds())
	if maxAge < 0 {
		maxAge = 0
	}
	return &http.Cookie{
		Name:     desktopLocalSessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   secure,
		Expires:  expiresAt,
		MaxAge:   maxAge,
	}
}

func (s *Server) handleDesktopLocalSessionBootstrap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !shouldUseDesktopLocalSessionAuth(r) && !isLocalTransportRequest(r) {
		writeError(w, http.StatusForbidden, errors.New("desktop local session bootstrap requires a same-origin browser request from this machine or the local transport"))
		return
	}
	var err error
	r, err = s.issueDesktopLocalSession(w, r)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, identity.ErrProductIdentityRequired) || errors.Is(err, identity.ErrSessionServiceNotConfigured) {
			status = http.StatusUnauthorized
		}
		writeError(w, status, err)
		return
	}
	actor, _ := productActorFromRequest(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"token":      desktopLocalSessionTokenFromRequest(r),
		"user_id":    actor.UserID,
		"username":   actor.User.Username,
		"expires_at": actor.TokenExpires,
	})
}

func (s *Server) actorFromDesktopLocalSession(r *http.Request) (identity.ActorContext, bool) {
	if s == nil || s.identitySessions == nil {
		return identity.ActorContext{}, false
	}
	actor, err := s.identitySessions.Validate(desktopLocalSessionTokenFromRequest(r))
	if err != nil {
		return identity.ActorContext{}, false
	}
	return actor, true
}

func requestWithActorContext(r *http.Request, actor identity.ActorContext) *http.Request {
	if r == nil {
		return nil
	}
	ctx := context.WithValue(r.Context(), productActorRequestContextKey, actor)
	if principal, err := identity.PrincipalFromActor(actor); err == nil {
		ctx = context.WithValue(ctx, productPrincipalRequestContextKey, principal)
		ctx = identity.ContextWithPrincipal(ctx, principal)
	}
	return r.WithContext(ctx)
}

func productActorFromRequest(r *http.Request) (identity.ActorContext, bool) {
	if r == nil {
		return identity.ActorContext{}, false
	}
	actor, ok := r.Context().Value(productActorRequestContextKey).(identity.ActorContext)
	if !ok || !isCompleteProductActor(actor) {
		return identity.ActorContext{}, false
	}
	return actor, true
}

func PrincipalFromRequest(r *http.Request) (identity.Principal, bool) {
	if r == nil {
		return identity.Principal{}, false
	}
	principal, ok := r.Context().Value(productPrincipalRequestContextKey).(identity.Principal)
	if ok && principal.Valid() {
		return principal, true
	}
	actor, ok := productActorFromRequest(r)
	if !ok {
		return identity.Principal{}, false
	}
	principal, err := identity.PrincipalFromActor(actor)
	if err != nil || !principal.Valid() {
		return identity.Principal{}, false
	}
	return principal, true
}

func productSessionTokenFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if issued, _ := r.Context().Value(desktopLocalAuthIssuedTokenKey).(string); strings.TrimSpace(issued) != "" {
		return strings.TrimSpace(issued)
	}
	if token := strings.TrimSpace(r.Header.Get("X-Swarm-Token")); token != "" {
		return token
	}
	if authz := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(strings.ToLower(authz), "bearer ") {
		return strings.TrimSpace(authz[7:])
	}
	if cookie, err := r.Cookie(desktopLocalSessionCookieName); err == nil {
		return strings.TrimSpace(cookie.Value)
	}
	return ""
}

func isCompleteProductActor(actor identity.ActorContext) bool {
	if strings.TrimSpace(actor.UserID) == "" ||
		strings.TrimSpace(actor.AccountScopeID) == "" ||
		strings.TrimSpace(actor.User.ID) == "" ||
		strings.TrimSpace(actor.AccountScope.ID) == "" ||
		strings.TrimSpace(actor.AccountUser.UserID) == "" ||
		strings.TrimSpace(actor.AccountUser.AccountScopeID) == "" ||
		strings.TrimSpace(actor.Selection.UserID) == "" {
		return false
	}
	if strings.TrimSpace(actor.TeamID) == "" {
		return true
	}
	return strings.TrimSpace(actor.Team.ID) != "" &&
		strings.TrimSpace(actor.Membership.UserID) != "" &&
		strings.TrimSpace(actor.Membership.TeamID) != "" &&
		strings.TrimSpace(actor.Selection.TeamID) != ""
}

func (s *Server) requireProductActor(w http.ResponseWriter, r *http.Request) (identity.ActorContext, bool) {
	if actor, ok := productActorFromRequest(r); ok {
		return actor, true
	}
	writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
	return identity.ActorContext{}, false
}

func isLocalTransportRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	enabled, _ := r.Context().Value(localTransportAuthEnabledKey).(bool)
	return enabled
}
