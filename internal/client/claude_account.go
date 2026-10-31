package client

import (
	"context"
	"time"
)

type ClaudeCodeUsageCLI struct {
	Ready            bool   `json:"ready"`
	Version          string `json:"version,omitempty"`
	LoggedIn         bool   `json:"logged_in"`
	AuthMethod       string `json:"auth_method,omitempty"`
	SubscriptionType string `json:"subscription_type,omitempty"`
	AccountEmail     string `json:"account_email,omitempty"`
	AccountUUID      string `json:"account_uuid,omitempty"`
	OrganizationName string `json:"organization_name,omitempty"`
	OrganizationUUID string `json:"organization_uuid,omitempty"`
	Reason           string `json:"reason,omitempty"`
}

type ClaudeCodeUsageWindow struct {
	Name        string  `json:"name"`
	Utilization float64 `json:"utilization"`
	ResetsAt    int64   `json:"resets_at,omitempty"`
}

type ClaudeCodeRateLimits struct {
	Status         string                  `json:"status"`
	LimitType      string                  `json:"limit_type,omitempty"`
	ResetsAt       int64                   `json:"resets_at,omitempty"`
	OverageStatus  string                  `json:"overage_status,omitempty"`
	IsUsingOverage bool                    `json:"is_using_overage"`
	Windows        []ClaudeCodeUsageWindow `json:"windows"`
	ObservedAt     time.Time               `json:"observed_at"`
}

type ClaudeCodeUsage struct {
	CLI        ClaudeCodeUsageCLI    `json:"cli"`
	RateLimits *ClaudeCodeRateLimits `json:"rate_limits"`
}

// GetClaudeCodeUsage reads /claude: Claude Code readiness, plan and the latest
// subscription usage a Claude Code turn reported.
func (c *API) GetClaudeCodeUsage(ctx context.Context) (ClaudeCodeUsage, error) {
	var usage ClaudeCodeUsage
	if err := c.getJSON(ctx, "/v1/claudecode/usage", &usage, true); err != nil {
		return ClaudeCodeUsage{}, err
	}
	return usage, nil
}
