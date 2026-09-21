package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type createScopedTokenRequest struct {
	Name             string   `json:"name"`
	Scopes           []string `json:"scopes"`
	ExpiresInSeconds int64    `json:"expires_in_seconds,omitempty"`
}

func (s *Server) handleAuthTokens(w http.ResponseWriter, r *http.Request) {
	if s.security == nil {
		writeError(w, http.StatusInternalServerError, errors.New("security service not configured"))
		return
	}

	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, errors.New("trusted principal required"))
		return
	}

	if !s.requireScope(w, r, "admin") {
		return
	}

	accountScopeID := principal.AccountScopeID
	if accountScopeID == "" {
		writeError(w, http.StatusBadRequest, errors.New("account scope required"))
		return
	}

	subpath := strings.TrimPrefix(r.URL.Path, "/v3/auth/tokens")
	subpath = strings.Trim(subpath, "/")
	if subpath != "" {
		s.handleAuthTokenSubpath(w, r, accountScopeID, subpath)
		return
	}

	switch r.Method {
	case http.MethodGet:
		tokens, err := s.security.ListScopedTokens(accountScopeID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if tokens == nil {
			tokens = []pebblestore.ScopedTokenRecord{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"tokens": tokens,
		})

	case http.MethodPost:
		var req createScopedTokenRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid json payload"))
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		if req.Name == "" {
			writeError(w, http.StatusBadRequest, errors.New("token name is required"))
			return
		}

		expiresIn := time.Duration(0)
		if req.ExpiresInSeconds > 0 {
			expiresIn = time.Duration(req.ExpiresInSeconds) * time.Second
		}

		rawToken, record, err := s.security.CreateScopedToken(req.Name, req.Scopes, accountScopeID, principal.UserID, expiresIn)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"token":  rawToken,
			"record": record,
		})

	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleAuthTokenSubpath(w http.ResponseWriter, r *http.Request, accountScopeID, subpath string) {
	tokenID := subpath
	isRevoke := false
	if strings.HasSuffix(tokenID, "/revoke") {
		isRevoke = true
		tokenID = strings.TrimSuffix(tokenID, "/revoke")
	}

	if tokenID == "" {
		writeError(w, http.StatusBadRequest, errors.New("token id is required"))
		return
	}

	switch r.Method {
	case http.MethodDelete:
		if r.URL.Query().Get("purge") == "true" {
			if err := s.security.DeleteScopedToken(accountScopeID, tokenID); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": true})
			return
		}
		record, err := s.security.RevokeScopedToken(accountScopeID, tokenID)
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "record": record})

	case http.MethodPost:
		if !isRevoke {
			writeError(w, http.StatusBadRequest, errors.New("invalid subpath operation"))
			return
		}
		record, err := s.security.RevokeScopedToken(accountScopeID, tokenID)
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "record": record})

	default:
		methodNotAllowed(w)
	}
}
