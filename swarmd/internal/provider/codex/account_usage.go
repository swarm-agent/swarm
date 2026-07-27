package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	providerdiagnostics "swarm/packages/swarmd/internal/provider/diagnostics"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	codexAccountUsageURL         = "https://chatgpt.com/backend-api/wham/usage"
	codexResetCreditsURL         = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits"
	codexConsumeResetCreditURL   = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume"
	maxCodexAccountResponseBytes = 1 << 20
)

var ErrCodexAccountOAuthRequired = errors.New("codex account usage requires a connected ChatGPT OAuth account")

type AccountUsage struct {
	PlanType     string               `json:"plan_type"`
	RateLimit    *AccountRateLimit    `json:"rate_limit,omitempty"`
	ResetCredits *ResetCreditsSummary `json:"rate_limit_reset_credits,omitempty"`
}

type AccountRateLimit struct {
	Allowed         bool         `json:"allowed"`
	LimitReached    bool         `json:"limit_reached"`
	PrimaryWindow   *UsageWindow `json:"primary_window,omitempty"`
	SecondaryWindow *UsageWindow `json:"secondary_window,omitempty"`
}

type UsageWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	ResetAfterSeconds  int64   `json:"reset_after_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

type ResetCreditsSummary struct {
	AvailableCount int64 `json:"available_count"`
}

type ResetCredits struct {
	Credits        []ResetCredit `json:"credits"`
	AvailableCount int64         `json:"available_count"`
}

type ResetCredit struct {
	ID          string  `json:"id"`
	ResetType   string  `json:"reset_type"`
	Status      string  `json:"status"`
	GrantedAt   string  `json:"granted_at"`
	ExpiresAt   *string `json:"expires_at"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
}

type ConsumeResetCreditRequest struct {
	RedeemRequestID string `json:"redeem_request_id"`
	CreditID        string `json:"credit_id"`
}

type ConsumeResetCreditResponse struct {
	Code         string `json:"code"`
	WindowsReset int64  `json:"windows_reset"`
}

type AccountAPIError struct {
	StatusCode int
}

func (e *AccountAPIError) Error() string {
	if e == nil || e.StatusCode == 0 {
		return "codex account request failed"
	}
	return fmt.Sprintf("codex account request failed with status %d", e.StatusCode)
}

func (c *Client) GetAccountUsage(ctx context.Context) (AccountUsage, error) {
	var out AccountUsage
	err := c.doAccountJSON(ctx, http.MethodGet, codexAccountUsageURL, nil, &out)
	return out, err
}

func (c *Client) GetResetCredits(ctx context.Context) (ResetCredits, error) {
	var out ResetCredits
	err := c.doAccountJSON(ctx, http.MethodGet, codexResetCreditsURL, nil, &out)
	return out, err
}

func (c *Client) ConsumeResetCredit(ctx context.Context, req ConsumeResetCreditRequest) (ConsumeResetCreditResponse, error) {
	req.RedeemRequestID = strings.TrimSpace(req.RedeemRequestID)
	req.CreditID = strings.TrimSpace(req.CreditID)
	if req.RedeemRequestID == "" {
		return ConsumeResetCreditResponse{}, errors.New("redeem_request_id is required")
	}
	if req.CreditID == "" {
		return ConsumeResetCreditResponse{}, errors.New("credit_id is required")
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return ConsumeResetCreditResponse{}, fmt.Errorf("encode reset credit request: %w", err)
	}
	var out ConsumeResetCreditResponse
	err = c.doAccountJSON(ctx, http.MethodPost, codexConsumeResetCreditURL, payload, &out)
	if err == nil {
		switch out.Code {
		case "reset", "nothing_to_reset", "no_credit", "already_redeemed":
		default:
			err = fmt.Errorf("decode codex account response: unknown consume outcome %q", out.Code)
		}
	}
	return out, err
}

func (c *Client) doAccountJSON(ctx context.Context, method, endpoint string, payload []byte, out any) error {
	record, err := c.ensureAuth(ctx)
	if err != nil {
		return err
	}
	if record.Type != pebblestore.CodexAuthTypeOAuth {
		return ErrCodexAccountOAuthRequired
	}
	if strings.TrimSpace(record.AccountID) == "" {
		return errors.New("codex OAuth account id is missing; reconnect ChatGPT OAuth")
	}

	status, body, err := c.sendAccountJSON(ctx, record, method, endpoint, payload)
	if err != nil {
		return err
	}
	if status == http.StatusUnauthorized {
		refreshed, refreshErr := c.refreshOAuth(ctx, record.RefreshToken)
		if refreshErr != nil {
			return fmt.Errorf("codex account request unauthorized and refresh failed: %w", refreshErr)
		}
		accountID := extractAccountIDFromToken(refreshed.AccessToken)
		if accountID == "" {
			accountID = record.AccountID
		}
		record, err = c.authStore.UpdateOAuthCredentialForAccount(record.AccountScopeID, record.Provider, record.ID, refreshed.AccessToken, refreshed.RefreshToken, refreshed.ExpiresAt, accountID)
		if err != nil {
			return fmt.Errorf("persist refreshed codex oauth: %w", err)
		}
		status, body, err = c.sendAccountJSON(ctx, record, method, endpoint, payload)
		if err != nil {
			return err
		}
	}
	if status < 200 || status >= 300 {
		return &AccountAPIError{StatusCode: status}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode codex account response: %w", err)
	}
	return nil
}

func (c *Client) sendAccountJSON(ctx context.Context, record pebblestore.CodexAuthRecord, method, endpoint string, payload []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+record.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set(userAgentHeader, defaultCodexTransportUserAgent)
	req.Header.Set(originatorHeader, defaultOriginatorHeaderValue)
	req.Header.Set(chatGPTAccountIDHeader, record.AccountID)
	if len(payload) != 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	providerdiagnostics.LogRequest("codex", "account."+strings.ToLower(method), req, payload)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		providerdiagnostics.LogErrorContext(ctx, "codex", "account."+strings.ToLower(method), err)
		return 0, nil, fmt.Errorf("codex account request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCodexAccountResponseBytes+1))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read codex account response: %w", err)
	}
	providerdiagnostics.LogResponse("codex", "account."+strings.ToLower(method), resp, body)
	if len(body) > maxCodexAccountResponseBytes {
		return resp.StatusCode, nil, errors.New("codex account response exceeds size limit")
	}
	return resp.StatusCode, body, nil
}
