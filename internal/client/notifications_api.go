package client

import (
	"context"
	"net/url"
	"strconv"
	"strings"
)

type NotificationSummary struct {
	SwarmID     string `json:"swarm_id"`
	TotalCount  int    `json:"total_count"`
	UnreadCount int    `json:"unread_count"`
	ActiveCount int    `json:"active_count"`
	UpdatedAt   int64  `json:"updated_at"`
}

type NotificationRecord struct {
	ID              string `json:"id"`
	SwarmID         string `json:"swarm_id"`
	OriginSwarmID   string `json:"origin_swarm_id,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	RunID           string `json:"run_id,omitempty"`
	Category        string `json:"category"`
	Severity        string `json:"severity"`
	Title           string `json:"title"`
	Body            string `json:"body"`
	Status          string `json:"status"`
	SourceEventType string `json:"source_event_type,omitempty"`
	PermissionID    string `json:"permission_id,omitempty"`
	ToolName        string `json:"tool_name,omitempty"`
	Requirement     string `json:"requirement,omitempty"`
	SessionTitle    string `json:"session_title,omitempty"`
	SessionLabel    string `json:"session_label,omitempty"`
	WorkspacePath   string `json:"workspace_path,omitempty"`
	WorkspaceName   string `json:"workspace_name,omitempty"`
	OriginLabel     string `json:"origin_label,omitempty"`
	ActionURL       string `json:"action_url,omitempty"`
	ReadAt          int64  `json:"read_at,omitempty"`
	AckedAt         int64  `json:"acked_at,omitempty"`
	MutedAt         int64  `json:"muted_at,omitempty"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

func (c *API) ListNotifications(ctx context.Context, limit int, swarmID string) ([]NotificationRecord, error) {
	values := url.Values{}
	if limit > 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
	if swarmID = strings.TrimSpace(swarmID); swarmID != "" {
		values.Set("swarm_id", swarmID)
	}
	path := "/v1/notifications"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var resp struct {
		OK            bool                 `json:"ok"`
		Notifications []NotificationRecord `json:"notifications"`
	}
	if err := c.getJSON(ctx, path, &resp, true); err != nil {
		return nil, err
	}
	return resp.Notifications, nil
}

func (c *API) ClearNotifications(ctx context.Context, swarmID string) (int, error) {
	values := url.Values{}
	if swarmID = strings.TrimSpace(swarmID); swarmID != "" {
		values.Set("swarm_id", swarmID)
	}
	path := "/v1/alerts/clear"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			Deleted int `json:"deleted"`
		} `json:"result"`
	}
	if err := c.postJSON(ctx, path, nil, &resp, true); err != nil {
		return 0, err
	}
	return resp.Result.Deleted, nil
}

func (c *API) GetNotificationSummary(ctx context.Context, swarmID string) (NotificationSummary, error) {
	values := url.Values{}
	if swarmID = strings.TrimSpace(swarmID); swarmID != "" {
		values.Set("swarm_id", swarmID)
	}
	path := "/v1/notifications/summary"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var resp struct {
		OK      bool                `json:"ok"`
		Summary NotificationSummary `json:"summary"`
	}
	if err := c.getJSON(ctx, path, &resp, true); err != nil {
		return NotificationSummary{}, err
	}
	return resp.Summary, nil
}
