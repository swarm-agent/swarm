package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/security"
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
	MaxConcurrentRuns int    `json:"max_concurrent_runs,omitempty"`
	// Per-end-user limits (sessions created with an end_user); zero is the default.
	EndUserMessagesPerMinute int   `json:"end_user_messages_per_minute,omitempty"`
	EndUserSessionsPerHour   int   `json:"end_user_sessions_per_hour,omitempty"`
	EndUserMaxTokensPerDay   int64 `json:"end_user_max_tokens_per_day,omitempty"`
	// Operator (with agent_name) mints the human-takeover token for that agent.
	Operator bool `json:"operator,omitempty"`
	// AIAccess mints an AI key for Swarm Control (/mcp): "read", "write" or "full".
	// Scopes are derived from the level; scopes, worker and agent stay empty.
	AIAccess string `json:"ai_access,omitempty"`
}

// scopedTokenListItem is a stored token as GET /v3/auth/tokens lists it, with
// never_expires set on one stored without an expiry (minted before expiry was
// required, or a trigger worker, remote transport or runner credential).
type scopedTokenListItem struct {
	pebblestore.ScopedTokenRecord
	NeverExpires bool `json:"never_expires,omitempty"`
}

// requestedLifetime is the lifetime a mint request asks for. Every key an API
// caller mints must expire: a missing, zero or negative expires_in_seconds is
// refused, never defaulted, and so is one over the kind's maximum.
func requestedLifetime(seconds int64, kind string, max time.Duration) (time.Duration, error) {
	maxSeconds := int64(max / time.Second)
	days := maxSeconds / 86400
	if seconds <= 0 {
		return 0, fmt.Errorf("expires_in_seconds is required: every key must expire (%s lasts at most %d days, %d seconds)", kind, days, maxSeconds)
	}
	if seconds > maxSeconds {
		return 0, fmt.Errorf("%s lasts at most %d days: expires_in_seconds must be at most %d", kind, days, maxSeconds)
	}
	return time.Duration(seconds) * time.Second, nil
}

// aiKeyScopes are the API scopes plus Swarm Control levels for an AI key.
// Read keys see and call only read tools, including the machine's status, its
// activity feed and which secrets exist where (never their values); write keys
// may also create projects, deploy workers, give them jobs, follow up and
// answer their questions, merge their results, stop runs, pick each role's
// model among the connected providers and start the person's ChatGPT
// sign-in (they finish it in their own browser). Full keys are for the AI that
// runs this box: every Swarm Control tool, including approving tool calls, models, workers,
// limits, custom agents and client keys. Any key with a
// Swarm Control level reaches only /mcp (gateAIKey), so admin here widens
// what the allowlisted tools may do, never the rest of the API.
func aiKeyScopes(level string) ([]string, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "read":
		return []string{"swarm:read", "sessions:read", "automations:read", "agents:read", "signals:read", "secrets:read"}, nil
	case "write":
		return []string{"swarm:read", "swarm:write", "sessions:read", "sessions:write", "automations:read", "automations:write", "agents:read", "settings:write", "signals:read", "secrets:read"}, nil
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
		items := make([]scopedTokenListItem, 0, len(tokens))
		for _, token := range tokens {
			items = append(items, scopedTokenListItem{ScopedTokenRecord: token, NeverExpires: security.NeverExpires(token)})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"tokens": items,
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

		kind, maxLifetime := "a client key", security.ScopedTokenMaxLifetime
		if strings.TrimSpace(req.AIAccess) != "" {
			kind = "an AI key"
		} else if strings.TrimSpace(req.AgentName) != "" {
			// A gateway or operator token sits on an internet-facing host.
			kind, maxLifetime = "an agent-bound token", security.AgentBoundMaxLifetime
		}
		expiresIn, lifetimeErr := requestedLifetime(req.ExpiresInSeconds, kind, maxLifetime)
		if lifetimeErr != nil {
			writeError(w, http.StatusBadRequest, lifetimeErr)
			return
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
			rawToken, record, err = s.security.CreateAgentBoundTokenUnder(parent, req.Name, accountScopeID, principal.UserID, expiresIn, agentName, security.AgentBoundLimits{MessagesPerMinute: req.MessagesPerMinute, SessionsPerHour: req.SessionsPerHour, MaxConcurrentRuns: req.MaxConcurrentRuns,
				EndUserMessagesPerMinute: req.EndUserMessagesPerMinute, EndUserSessionsPerHour: req.EndUserSessionsPerHour, EndUserMaxTokensPerDay: req.EndUserMaxTokensPerDay, Operator: req.Operator})
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

// updateTokenLimitsRequest changes the fields it names on a gateway token.
type updateTokenLimitsRequest struct {
	MessagesPerMinute        *int   `json:"messages_per_minute"`
	SessionsPerHour          *int   `json:"sessions_per_hour"`
	MaxConcurrentRuns        *int   `json:"max_concurrent_runs"`
	EndUserMessagesPerMinute *int   `json:"end_user_messages_per_minute"`
	EndUserSessionsPerHour   *int   `json:"end_user_sessions_per_hour"`
	EndUserMaxTokensPerDay   *int64 `json:"end_user_max_tokens_per_day"`
}

// handleAuthTokenLimits is POST /v3/auth/tokens/{id}/limits (owner or admin
// key): edit a gateway token's limits without reissuing it.
func (s *Server) handleAuthTokenLimits(w http.ResponseWriter, r *http.Request, accountScopeID, tokenID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req updateTokenLimitsRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid json payload: send only limit fields"))
		return
	}
	if req == (updateTokenLimitsRequest{}) {
		writeError(w, http.StatusBadRequest, errors.New("no limit to change"))
		return
	}
	record, err := s.security.UpdateAgentBoundLimits(accountScopeID, tokenID, security.AgentBoundLimitsUpdate{
		MessagesPerMinute: req.MessagesPerMinute, SessionsPerHour: req.SessionsPerHour, MaxConcurrentRuns: req.MaxConcurrentRuns,
		EndUserMessagesPerMinute: req.EndUserMessagesPerMinute, EndUserSessionsPerHour: req.EndUserSessionsPerHour, EndUserMaxTokensPerDay: req.EndUserMaxTokensPerDay,
	})
	if errors.Is(err, security.ErrScopedTokenNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "record": record})
}

func (s *Server) handleAuthTokenSubpath(w http.ResponseWriter, r *http.Request, accountScopeID, subpath string) {
	if tokenID, ok := strings.CutSuffix(subpath, "/limits"); ok && tokenID != "" && !strings.Contains(tokenID, "/") {
		s.handleAuthTokenLimits(w, r, accountScopeID, tokenID)
		return
	}
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
