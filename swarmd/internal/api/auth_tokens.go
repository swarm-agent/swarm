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
	// Kind, when given, must match the other fields: "ai" (ai_access), "bot"
	// (agent_name) or "app" (scopes, monitor or a worker).
	Kind   string   `json:"kind,omitempty"`
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
	// Monitor makes an app key that reads only the machine summary and the
	// signal feed; scopes must then be empty.
	Monitor bool `json:"monitor,omitempty"`
	// ExpiresInSeconds is an optional fixed end date. Without one the key
	// works while it is used and lapses after 90 days unused.
	ExpiresInSeconds int64  `json:"expires_in_seconds,omitempty"`
	WorkerID         string `json:"worker_id,omitempty"`
	WorkerName       string `json:"worker_name,omitempty"`
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

// scopedTokenListItem is a stored key as GET /v3/auth/tokens lists it, with
// what it is for and when it stops working on its own (security.KeyLapse).
type scopedTokenListItem struct {
	pebblestore.ScopedTokenRecord
	KeyKind      string   `json:"key_kind"`
	Options      []string `json:"options,omitempty"`
	State        string   `json:"state"`
	LapsesAt     int64    `json:"lapses_at,omitempty"`
	LapseReason  string   `json:"lapse_reason,omitempty"`
	FixedDate    bool     `json:"fixed_date,omitempty"`
	NeverExpires bool     `json:"never_expires,omitempty"`
}

func newScopedTokenListItem(record pebblestore.ScopedTokenRecord, now int64) scopedTokenListItem {
	at, reason := security.KeyLapse(record)
	state := "active"
	switch {
	case record.Revoked:
		state = "revoked"
	case at > 0 && now >= at:
		state = "expired"
	}
	return scopedTokenListItem{ScopedTokenRecord: record, KeyKind: security.KeyKind(record), Options: security.KeyOptions(record),
		State: state, LapsesAt: at, LapseReason: reason, FixedDate: security.HasFixedDate(record), NeverExpires: security.NeverExpires(record)}
}

// requestedLifetime is a mint request's optional fixed end date: 0 (none,
// the key lapses only after 90 days unused) or 1 second to 365 days.
func requestedLifetime(seconds int64) (time.Duration, error) {
	maxSeconds := int64(security.KeyFixedDateMax / time.Second)
	if seconds < 0 {
		return 0, errors.New("expires_in_seconds must be positive (a fixed end date) or left out (the key lapses after 90 days unused)")
	}
	if seconds > maxSeconds {
		return 0, fmt.Errorf("a fixed end date is at most 365 days away: expires_in_seconds must be at most %d", maxSeconds)
	}
	return time.Duration(seconds) * time.Second, nil
}

// checkRequestedKind refuses a kind that does not match the request's fields.
func checkRequestedKind(req createScopedTokenRequest) error {
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	derived := security.KeyKindApp
	switch {
	case strings.TrimSpace(req.AIAccess) != "":
		derived = security.KeyKindAI
	case strings.TrimSpace(req.AgentName) != "":
		derived = security.KeyKindBot
	}
	if req.Monitor && (derived != security.KeyKindApp || len(req.Scopes) > 0 || req.WorkerID != "") {
		return errors.New("monitor is an app key that reads status and signals only; it takes no scopes, worker, agent or AI access")
	}
	switch kind {
	case "", derived:
		return nil
	case security.KeyKindAI:
		return errors.New(`kind "ai" needs ai_access ("read", "write" or "full")`)
	case security.KeyKindBot:
		return errors.New(`kind "bot" needs agent_name (a sealed agent)`)
	case security.KeyKindApp:
		return errors.New(`kind "app" takes scopes or monitor, not ai_access or agent_name`)
	}
	return errors.New(`kind must be "ai", "app" or "bot"`)
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
		now := time.Now().UnixMilli()
		for _, token := range tokens {
			items = append(items, newScopedTokenListItem(token, now))
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

		if err := checkRequestedKind(req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if req.Monitor {
			req.Scopes = append([]string(nil), security.MonitorScopes...)
		}
		expiresIn, lifetimeErr := requestedLifetime(req.ExpiresInSeconds)
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
			"record": newScopedTokenListItem(record, time.Now().UnixMilli()),
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
