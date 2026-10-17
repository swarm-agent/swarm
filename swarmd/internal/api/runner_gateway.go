package api

import (
	"errors"
	"net/http"
	"strings"
)

// RunnerGatewayHandler exposes only the runner protocol. Tailscale capability
// headers, local-owner authentication and Swarm Control are never consulted.
func (s *Server) RunnerGatewayHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		join := r.Method == http.MethodPost && r.URL.Path == "/v3/runners/join"
		if !join && runnerRouteScope(r) == "" {
			http.NotFound(w, r)
			return
		}
		if join && r.Header.Get("Authorization") == "" {
			s.handleRunners(w, r)
			return
		}
		auth := strings.Fields(r.Header.Get("Authorization"))
		if len(auth) != 2 || !strings.EqualFold(auth[0], "Bearer") {
			writeError(w, 401, errors.New("runner bearer credential required"))
			return
		}
		if !strings.HasPrefix(auth[1], "swk_") {
			writeError(w, 403, errors.New("runner credential required"))
			return
		}
		if s.security == nil {
			writeError(w, 503, errors.New("runner authentication unavailable"))
			return
		}
		rec, err := s.security.ValidateScopedToken(auth[1])
		if err != nil || rec == nil || rec.UserID == "" || rec.AccountScopeID == "" {
			writeError(w, 401, errors.New("invalid runner credential"))
			return
		}
		if rec.Kind != "runner" {
			writeError(w, 403, errors.New("runner credential required"))
			return
		}
		if !join && !gateRunnerKey(w, r, rec) {
			return
		}
		s.handleRunners(w, requestWithScopedToken(r, rec))
	})
}
