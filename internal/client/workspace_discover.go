package client

import (
	"context"
	"net/url"
	"strconv"
	"strings"
)

type WorkspaceDiscoverEntry struct {
	Path         string `json:"path"`
	Name         string `json:"name"`
	IsGitRepo    bool   `json:"is_git_repo"`
	HasClaude    bool   `json:"has_claude"`
	HasSwarm     bool   `json:"has_swarm"`
	LastModified int64  `json:"last_modified,omitempty"`
}

func (c *API) DiscoverWorkspaces(ctx context.Context, limit int, roots []string) ([]WorkspaceDiscoverEntry, error) {
	if limit <= 0 {
		limit = 200
	}
	path := "/v1/workspace/discover?limit=" + strconv.Itoa(limit)
	if len(roots) > 0 {
		clean := make([]string, 0, len(roots))
		for _, root := range roots {
			root = strings.TrimSpace(root)
			if root != "" {
				clean = append(clean, root)
			}
		}
		if len(clean) > 0 {
			path += "&roots=" + url.QueryEscape(strings.Join(clean, ","))
		}
	}
	var resp struct {
		OK          bool                     `json:"ok"`
		Directories []WorkspaceDiscoverEntry `json:"directories"`
	}
	if err := c.getJSON(ctx, path, &resp, true); err != nil {
		return nil, err
	}
	return resp.Directories, nil
}
