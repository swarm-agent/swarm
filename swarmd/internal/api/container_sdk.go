package api

import (
	"errors"
	"net/http"
	"strings"
)

// ContainerSDKHandler is not a proxy to privileged local transport. It admits
// only scoped bearer credentials and a small session API surface. No onboarding,
// attach credential, cookie, browser bootstrap, or account-selection fallback.
func (s *Server) ContainerSDKHandler() http.Handler {
	next := s.withVaultGate(s.withJSON(s.apiMux()))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if s.security == nil || s.identitySessions == nil {
			writeError(w, http.StatusServiceUnavailable, errors.New("SDK authentication unavailable"))
			return
		}
		auth := strings.Fields(r.Header.Get("Authorization"))
		if len(auth) != 2 || !strings.EqualFold(auth[0], "Bearer") || !strings.HasPrefix(auth[1], "swk_") {
			writeError(w, http.StatusUnauthorized, errors.New("scoped bearer token required"))
			return
		}
		rec, err := s.security.ValidateScopedToken(auth[1])
		if err != nil || rec == nil || rec.UserID == "" || rec.AccountScopeID == "" {
			writeError(w, http.StatusUnauthorized, errors.New("invalid scoped bearer token"))
			return
		}
		actor, err := s.identitySessions.ActorForUserID(rec.UserID)
		if err != nil || !isCompleteProductActor(actor) || actor.UserID != rec.UserID || actor.AccountScopeID != rec.AccountScopeID {
			writeError(w, http.StatusUnauthorized, errors.New("scoped token identity unavailable"))
			return
		}
		if strings.TrimSpace(rec.AgentName) != "" {
			// Agent-bound tokens get only their own gate: no MCP, no listing.
			if !s.gateAgentBoundToken(w, r, rec) {
				return
			}
			next.ServeHTTP(w, requestWithScopedToken(requestWithActorContext(r, actor), rec))
			return
		}
		if r.URL.Path == controlMCPPath {
			// Swarm Control MCP translates each tool into one request against
			// the allowlisted routes below, under this same verified identity.
			s.serveControlMCP(w, requestWithScopedToken(requestWithActorContext(r, actor), rec), next)
			return
		}
		if !containerSDKRouteAllowed(r) {
			writeError(w, http.StatusForbidden, errors.New("route unavailable on container SDK listener"))
			return
		}
		// Route handlers retain canonical scope checks, account authorization and
		// per-call permissions. Never send this request through local transport.
		next.ServeHTTP(w, requestWithScopedToken(requestWithActorContext(r, actor), rec))
	})
}

func containerSDKRouteAllowed(r *http.Request) bool {
	p := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(p) < 2 || p[0] != "v3" || p[1] != "sessions" {
		return false
	}
	if len(p) == 2 {
		return r.Method == http.MethodGet || r.Method == http.MethodPost
	}
	if p[2] == "" || p[2] == "." || p[2] == ".." {
		return false
	}
	if len(p) == 3 {
		return r.Method == http.MethodGet
	}
	if len(p) == 4 && p[3] == "messages" {
		return r.Method == http.MethodPost
	}
	if len(p) == 5 && p[3] == "run" && p[4] == "stop" {
		return r.Method == http.MethodPost
	}
	if len(p) == 6 && p[3] == "permissions" && p[4] != "" && p[4] != "." && p[4] != ".." && p[5] == "resolve" {
		return r.Method == http.MethodPost
	}
	return false
}
