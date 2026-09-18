package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"swarm/packages/swarmd/internal/identity"
)

type SessionUsageLimitsStatus struct {
	AccountScopeID    string  `json:"account_scope_id"`
	DailyCostLimitUSD float64 `json:"daily_cost_limit_usd"`
	DailyTokensLimit  int64   `json:"daily_tokens_limit,omitempty"`
	Enabled           bool    `json:"enabled"`
	TodayCostUSD      float64 `json:"today_cost_usd"`
	TodayTokens       int64   `json:"today_tokens"`
	LimitExceeded     bool    `json:"limit_exceeded"`
	UpdatedAt         int64   `json:"updated_at"`
}

type SessionUsageLimitsResponse struct {
	OK     bool                     `json:"ok"`
	Limits SessionUsageLimitsStatus `json:"limits"`
}

type UpdateUsageLimitsRequest struct {
	DailyCostLimitUSD *float64 `json:"daily_cost_limit_usd,omitempty"`
	DailyTokensLimit  *int64   `json:"daily_tokens_limit,omitempty"`
	Enabled           *bool    `json:"enabled,omitempty"`
}

func (s *Server) handleSessionsV3UsageLimits(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("sessions v3 service is not configured"))
		return
	}
	principal, principalOK := PrincipalFromRequest(r)
	if !principalOK || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.handleGetSessionsV3UsageLimits(w, r, principal)
	case http.MethodPost, http.MethodPut:
		s.handlePostSessionsV3UsageLimits(w, r, principal)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleGetSessionsV3UsageLimits(w http.ResponseWriter, _ *http.Request, principal identity.Principal) {
	limitRec, _, err := s.sessions.GetUsageLimit(principal.AccountScopeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("get usage limit: %w", err))
		return
	}

	todayCost, todayTokens, err := s.sessions.GetTodayUsageTotal(principal.AccountScopeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("get today usage: %w", err))
		return
	}

	exceeded := limitRec.Enabled && limitRec.DailyCostLimitUSD > 0 && todayCost >= limitRec.DailyCostLimitUSD

	writeJSON(w, http.StatusOK, SessionUsageLimitsResponse{
		OK: true,
		Limits: SessionUsageLimitsStatus{
			AccountScopeID:    principal.AccountScopeID,
			DailyCostLimitUSD: limitRec.DailyCostLimitUSD,
			DailyTokensLimit:  limitRec.DailyTokensLimit,
			Enabled:           limitRec.Enabled,
			TodayCostUSD:      todayCost,
			TodayTokens:       todayTokens,
			LimitExceeded:     exceeded,
			UpdatedAt:         limitRec.UpdatedAt,
		},
	})
}

func (s *Server) handlePostSessionsV3UsageLimits(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
	var req UpdateUsageLimitsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode request body: %w", err))
		return
	}

	if req.DailyCostLimitUSD != nil && *req.DailyCostLimitUSD < 0 {
		writeError(w, http.StatusBadRequest, errors.New("daily_cost_limit_usd must be >= 0"))
		return
	}
	if req.DailyTokensLimit != nil && *req.DailyTokensLimit < 0 {
		writeError(w, http.StatusBadRequest, errors.New("daily_tokens_limit must be >= 0"))
		return
	}

	current, _, err := s.sessions.GetUsageLimit(principal.AccountScopeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("get current usage limit: %w", err))
		return
	}

	limitUSD := current.DailyCostLimitUSD
	if req.DailyCostLimitUSD != nil {
		limitUSD = *req.DailyCostLimitUSD
	}
	tokensLimit := current.DailyTokensLimit
	if req.DailyTokensLimit != nil {
		tokensLimit = *req.DailyTokensLimit
	}
	enabled := current.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	updated, err := s.sessions.SetUsageLimit(principal.AccountScopeID, limitUSD, tokensLimit, enabled)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("set usage limit: %w", err))
		return
	}

	todayCost, todayTokens, _ := s.sessions.GetTodayUsageTotal(principal.AccountScopeID)
	exceeded := updated.Enabled && updated.DailyCostLimitUSD > 0 && todayCost >= updated.DailyCostLimitUSD

	// If limit is exceeded after setting, immediately kill all running sessions for this account
	if exceeded && s.v3SessionExecutor != nil {
		reason := fmt.Sprintf("daily usage limit exceeded ($%.4f spent today, limit is $%.2f)", todayCost, updated.DailyCostLimitUSD)
		s.v3SessionExecutor.CancelRunsForAccount(principal.AccountScopeID, reason)
	}

	writeJSON(w, http.StatusOK, SessionUsageLimitsResponse{
		OK: true,
		Limits: SessionUsageLimitsStatus{
			AccountScopeID:    principal.AccountScopeID,
			DailyCostLimitUSD: updated.DailyCostLimitUSD,
			DailyTokensLimit:  updated.DailyTokensLimit,
			Enabled:           updated.Enabled,
			TodayCostUSD:      todayCost,
			TodayTokens:       todayTokens,
			LimitExceeded:     exceeded,
			UpdatedAt:         updated.UpdatedAt,
		},
	})
}
