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
	WorkerID         string   `json:"worker_id,omitempty"`
	WorkerName       string   `json:"worker_name,omitempty"`
	// AgentName mints a token limited to one sealed agent's sessions; scopes
	// and worker fields must then be empty.
	AgentName         string `json:"agent_name,omitempty"`
	MessagesPerMinute int    `json:"messages_per_minute,omitempty"`
	SessionsPerHour   int    `json:"sessions_per_hour,omitempty"`
	// AIAccess mints an AI key for Swarm Control (/mcp): "read", "write" or "full".
	// Scopes are derived from the level; scopes, worker and agent stay empty.
	AIAccess string `json:"ai_access,omitempty"`
}

const (
	aiKeyDefaultLifetime = 30 * 24 * time.Hour
	aiKeyMaxLifetime     = 365 * 24 * time.Hour
)

// aiKeyScopes are the API scopes plus Swarm Control levels for an AI key.
// Read keys see and call only read tools; write keys may also start sessions,
// send messages and stop runs. Full keys are for the AI that runs this box:
// every Swarm Control tool, including approving tool calls, models, workers,
// limits, custom agents, client keys and ChatGPT sign-in. Any key with a
// Swarm Control level reaches only /mcp (gateAIKey), so admin here widens
// what the allowlisted tools may do, never the rest of the API.
func aiKeyScopes(level string) ([]string, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "read":
		return []string{"swarm:read", "sessions:read", "automations:read", "agents:read"}, nil
	case "write":
		return []string{"swarm:read", "swarm:write", "sessions:read", "sessions:write", "automations:read", "automations:write", "agents:read"}, nil
	case "full":
		return []string{"swarm:read", "swarm:write", "swarm:approve", "swarm:manage", "admin"}, nil
	default:
		return nil, errors.New(`ai_access must be "read", "write" or "full"`)
	}
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

		// A key minted with a scoped token (an admin key, or a full AI key's
		// create_client_key) never outlives that token.
		parent, _ := ScopedTokenFromRequest(r)
		var (
			rawToken string
			record   pebblestore.ScopedTokenRecord
			err      error
		)
		if level := strings.TrimSpace(req.AIAccess); level != "" {
			if len(req.Scopes) > 0 || req.WorkerID != "" || req.WorkerName != "" || strings.TrimSpace(req.AgentName) != "" {
				writeError(w, http.StatusBadRequest, errors.New("an AI key takes no scopes, worker or agent"))
				return
			}
			scopes, scopeErr := aiKeyScopes(level)
			if scopeErr != nil {
				writeError(w, http.StatusBadRequest, scopeErr)
				return
			}
			if expiresIn == 0 {
				expiresIn = aiKeyDefaultLifetime
			}
			if expiresIn > aiKeyMaxLifetime {
				writeError(w, http.StatusBadRequest, errors.New("an AI key lasts at most 365 days"))
				return
			}
			rawToken, record, err = s.security.CreateScopedTokenUnder(parent, req.Name, scopes, accountScopeID, principal.UserID, expiresIn, "", "")
		} else if agentName := strings.TrimSpace(req.AgentName); agentName != "" {
			if len(req.Scopes) > 0 || req.WorkerID != "" || req.WorkerName != "" {
				writeError(w, http.StatusBadRequest, errors.New("an agent-bound token takes no scopes or worker"))
				return
			}
			if sealedErr := s.sealedAgentError(accountScopeID, agentName); sealedErr != nil {
				writeError(w, http.StatusBadRequest, sealedErr)
				return
			}
			rawToken, record, err = s.security.CreateAgentBoundTokenUnder(parent, req.Name, accountScopeID, principal.UserID, expiresIn, agentName, req.MessagesPerMinute, req.SessionsPerHour)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
		} else {
			rawToken, record, err = s.security.CreateScopedTokenUnder(parent, req.Name, req.Scopes, accountScopeID, principal.UserID, expiresIn, req.WorkerID, req.WorkerName)
		}
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
