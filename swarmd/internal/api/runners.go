package api

import (
	"errors"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func runnerRouteScope(r *http.Request) string {
	if r.Method == "POST" && r.URL.Path == "/v3/runners/claim" {
		return "runners:claim"
	}
	if r.Method == "PUT" && r.URL.Path == "/v3/runners/status" {
		return "runners:report"
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v3/runners/landings/"), "/")
	if !strings.HasPrefix(r.URL.Path, "/v3/runners/landings/") || len(parts) != 2 || parts[0] == "" || parts[0] == "." || parts[0] == ".." {
		return ""
	}
	if r.Method == "GET" && parts[1] == "bundle" {
		return "runners:claim"
	}
	if r.Method == "PUT" && (parts[1] == "heartbeat" || parts[1] == "output" || parts[1] == "result") {
		return "runners:report"
	}
	return ""
}

func gateRunnerKey(w http.ResponseWriter, r *http.Request, rec *pebblestore.ScopedTokenRecord) bool {
	if rec == nil || rec.Kind != "runner" {
		return true
	}
	scope := runnerRouteScope(r)
	exactScopes := len(rec.Scopes) == 2 && ((rec.Scopes[0] == "runners:claim" && rec.Scopes[1] == "runners:report") || (rec.Scopes[1] == "runners:claim" && rec.Scopes[0] == "runners:report"))
	if scope == "" || !exactScopes || !rec.HasScope(scope) {
		writeError(w, http.StatusForbidden, errors.New("runner credential cannot access this route"))
		return false
	}
	return true
}

func (s *Server) handleRunners(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/v3/runners/join" && r.Method == "POST" {
		if s.security == nil {
			writeError(w, 503, errors.New("runner authentication unavailable"))
			return
		}
		var req struct {
			Token string `json:"token"`
		}
		if err := decodeJSONLimited(w, r, &req, 4096); err != nil {
			writeError(w, 400, err)
			return
		}
		token, rec, err := s.security.JoinRunner(req.Token)
		if err != nil {
			writeError(w, 401, err)
			return
		}
		s.runnerSignal("runner.joined", rec.AccountScopeID, rec.Name)
		writeJSON(w, 201, map[string]any{"credential": token, "name": rec.Name, "kind": "runner", "scopes": rec.Scopes})
		return
	}
	if rec, scoped := ScopedTokenFromRequest(r); scoped {
		if rec.Kind == "runner" && runnerRouteScope(r) != "" {
			if !gateRunnerKey(w, r, rec) {
				return
			}
			s.handleRunnerWork(w, r, rec)
			return
		}
		writeError(w, 403, errors.New("runner administration requires the owner"))
		return
	}
	p, ok := PrincipalFromRequest(r)
	if !ok || !p.Valid() {
		writeError(w, 401, identity.ErrPrincipalRequired)
		return
	}
	if p.Type != identity.PrincipalTypeUser {
		writeError(w, 403, errors.New("runner administration requires the owner"))
		return
	}
	if s.security == nil {
		writeError(w, 503, errors.New("runner authentication unavailable"))
		return
	}
	switch {
	case r.URL.Path == "/v3/runners/join-tokens" && r.Method == "POST":
		var req struct {
			Name string `json:"name"`
		}
		if err := decodeJSONLimited(w, r, &req, 4096); err != nil {
			writeError(w, 400, err)
			return
		}
		token, err := s.security.CreateRunnerJoinToken(p.AccountScopeID, p.UserID, req.Name)
		if err != nil {
			writeError(w, 400, err)
			return
		}
		writeJSON(w, 201, map[string]any{"token": token, "expires_in_seconds": 900})
	case r.URL.Path == "/v3/runners" && r.Method == "GET":
		records, err := s.security.ListScopedTokens(p.AccountScopeID)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		result := []map[string]any{}
		for _, rec := range records {
			if rec.Kind == "runner" {
				result = append(result, map[string]any{"name": rec.Name, "kind": rec.Kind, "created_at": rec.CreatedAt, "last_seen": rec.LastUsedAt, "revoked": rec.Revoked})
			}
		}
		writeJSON(w, 200, map[string]any{"runners": result})
	case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/v3/runners/"):
		name := strings.TrimPrefix(r.URL.Path, "/v3/runners/")
		records, err := s.security.ListScopedTokens(p.AccountScopeID)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		for _, rec := range records {
			if rec.Kind == "runner" && rec.Name == name && !rec.Revoked {
				if _, err = s.security.RevokeScopedToken(p.AccountScopeID, rec.ID); err != nil {
					writeError(w, 500, err)
					return
				}
				s.runnerSignal("runner.removed", rec.AccountScopeID, rec.Name)
				writeJSON(w, 200, map[string]any{"revoked": true})
				return
			}
		}
		writeError(w, 404, errors.New("runner not found"))
	default:
		writeError(w, 403, errors.New("runner credential required for runner work"))
	}
}

func (s *Server) runnerSignal(kind, account, name string) {
	if s.signals != nil {
		_, _ = s.signals.Append(pebblestore.Signal{Kind: kind, Severity: "info", Source: "runner", Account: account, Summary: kind, Refs: map[string]string{"runner": name}})
	}
}
