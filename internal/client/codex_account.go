package client

import (
	"context"
	"errors"
	"strings"
)

type CodexUsageWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	ResetAfterSeconds  int64   `json:"reset_after_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

type CodexAccountRateLimit struct {
	Allowed         bool              `json:"allowed"`
	LimitReached    bool              `json:"limit_reached"`
	PrimaryWindow   *CodexUsageWindow `json:"primary_window,omitempty"`
	SecondaryWindow *CodexUsageWindow `json:"secondary_window,omitempty"`
}

type CodexResetCreditsSummary struct {
	AvailableCount int64 `json:"available_count"`
}

type CodexAccountUsage struct {
	PlanType     string                    `json:"plan_type"`
	RateLimit    *CodexAccountRateLimit    `json:"rate_limit,omitempty"`
	ResetCredits *CodexResetCreditsSummary `json:"rate_limit_reset_credits,omitempty"`
}

type CodexResetCredit struct {
	ID          string  `json:"id"`
	ResetType   string  `json:"reset_type"`
	Status      string  `json:"status"`
	GrantedAt   string  `json:"granted_at"`
	ExpiresAt   *string `json:"expires_at"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
}

type CodexResetCredits struct {
	Credits        []CodexResetCredit `json:"credits"`
	AvailableCount int64              `json:"available_count"`
}

type CodexConsumeResetCreditResponse struct {
	Code         string `json:"code"`
	WindowsReset int64  `json:"windows_reset"`
}

func (c *API) GetCodexAccountUsage(ctx context.Context) (CodexAccountUsage, error) {
	var usage CodexAccountUsage
	if err := c.getJSON(ctx, "/v1/codex/account/usage", &usage, true); err != nil {
		return CodexAccountUsage{}, err
	}
	return usage, nil
}

func (c *API) ListCodexResetCredits(ctx context.Context) (CodexResetCredits, error) {
	var credits CodexResetCredits
	if err := c.getJSON(ctx, "/v1/codex/account/reset-credits", &credits, true); err != nil {
		return CodexResetCredits{}, err
	}
	return credits, nil
}

// ConsumeCodexResetCredit redeems one daemon-reported credit. Callers must
// retain the same idempotency key while retrying the same redemption.
func (c *API) ConsumeCodexResetCredit(ctx context.Context, creditID, idempotencyKey string) (CodexConsumeResetCreditResponse, error) {
	creditID = strings.TrimSpace(creditID)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if creditID == "" {
		return CodexConsumeResetCreditResponse{}, errors.New("credit id is required")
	}
	if idempotencyKey == "" {
		return CodexConsumeResetCreditResponse{}, errors.New("idempotency key is required")
	}
	var response CodexConsumeResetCreditResponse
	if err := c.postJSON(ctx, "/v1/codex/account/reset-credits/consume", map[string]string{
		"credit_id":       creditID,
		"idempotency_key": idempotencyKey,
	}, &response, true); err != nil {
		return CodexConsumeResetCreditResponse{}, err
	}
	switch response.Code {
	case "reset", "already_redeemed", "nothing_to_reset", "no_credit":
		return response, nil
	default:
		return CodexConsumeResetCreditResponse{}, errors.New("codex reset credit response has an unknown outcome")
	}
}
