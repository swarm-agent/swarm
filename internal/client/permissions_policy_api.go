package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type PermissionPolicy struct {
	Version   int              `json:"version"`
	Rules     []PermissionRule `json:"rules,omitempty"`
	UpdatedAt int64            `json:"updated_at,omitempty"`
}

type PermissionRule struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Decision  string `json:"decision"`
	Tool      string `json:"tool,omitempty"`
	Pattern   string `json:"pattern,omitempty"`
	CreatedAt int64  `json:"created_at,omitempty"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
}

type PermissionExplain struct {
	Decision    string          `json:"decision"`
	Source      string          `json:"source"`
	Reason      string          `json:"reason"`
	ToolName    string          `json:"tool_name,omitempty"`
	Command     string          `json:"command,omitempty"`
	Rule        *PermissionRule `json:"rule,omitempty"`
	RulePreview string          `json:"rule_preview,omitempty"`
}

func (c *API) GetPermissionPolicy(ctx context.Context) (PermissionPolicy, error) {
	var resp struct {
		OK     bool             `json:"ok"`
		Policy PermissionPolicy `json:"policy"`
	}
	if err := c.getJSON(ctx, "/v1/permissions", &resp, true); err != nil {
		return PermissionPolicy{}, err
	}
	return resp.Policy, nil
}

func (c *API) AddPermissionRule(ctx context.Context, rule PermissionRule) (PermissionRule, error) {
	var resp struct {
		OK   bool           `json:"ok"`
		Rule PermissionRule `json:"rule"`
	}
	if err := c.postJSON(ctx, "/v1/permissions", rule, &resp, true); err != nil {
		return PermissionRule{}, err
	}
	return resp.Rule, nil
}

func (c *API) RemovePermissionRule(ctx context.Context, ruleID string) (bool, error) {
	ruleID = strings.TrimSpace(ruleID)
	if ruleID == "" {
		return false, errors.New("rule id is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/v1/permissions/"+url.PathEscape(ruleID), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/json")
	if token := strings.TrimSpace(c.Token()); token != "" {
		req.Header.Set("X-Swarm-Token", token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("request DELETE /v1/permissions/%s: %w", ruleID, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}
	if resp.StatusCode != http.StatusOK {
		return false, decodeAPIError(resp.StatusCode, raw)
	}
	var payload struct {
		Removed bool `json:"removed"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false, err
	}
	return payload.Removed, nil
}

func (c *API) SetBypassPermissions(ctx context.Context, enabled bool) (bool, error) {
	var resp struct {
		OK                bool `json:"ok"`
		BypassPermissions bool `json:"bypass_permissions"`
	}
	if err := c.postJSON(ctx, "/v1/permissions/bypass", map[string]bool{"enabled": enabled}, &resp, true); err != nil {
		return false, err
	}
	return resp.BypassPermissions, nil
}

func (c *API) ResetPermissionPolicy(ctx context.Context) (PermissionPolicy, error) {
	var resp struct {
		OK     bool             `json:"ok"`
		Policy PermissionPolicy `json:"policy"`
	}
	if err := c.postJSON(ctx, "/v1/permissions/reset", map[string]any{}, &resp, true); err != nil {
		return PermissionPolicy{}, err
	}
	return resp.Policy, nil
}

func (c *API) ExplainPermission(ctx context.Context, mode, toolName, arguments string) (PermissionExplain, error) {
	values := url.Values{}
	values.Set("mode", strings.TrimSpace(mode))
	values.Set("tool", strings.TrimSpace(toolName))
	values.Set("arguments", strings.TrimSpace(arguments))
	var resp struct {
		OK      bool              `json:"ok"`
		Explain PermissionExplain `json:"explain"`
	}
	if err := c.getJSON(ctx, "/v1/permissions/explain?"+values.Encode(), &resp, true); err != nil {
		return PermissionExplain{}, err
	}
	return resp.Explain, nil
}
