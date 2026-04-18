package client

import (
	"context"
	"net/url"
	"strings"
)

type NotificationSummary struct {
	SwarmID     string `json:"swarm_id"`
	TotalCount  int    `json:"total_count"`
	UnreadCount int    `json:"unread_count"`
	ActiveCount int    `json:"active_count"`
	UpdatedAt   int64  `json:"updated_at"`
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
