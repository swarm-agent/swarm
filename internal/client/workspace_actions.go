package client

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

type WorkspaceActionInput struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Description string   `json:"description,omitempty"`
	Kind        string   `json:"kind"`
	Required    bool     `json:"required,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Default     string   `json:"default,omitempty"`
	Arguments   []string `json:"arguments,omitempty"`
}

type WorkspaceAction struct {
	ID            string                 `json:"id"`
	WorkspaceID   string                 `json:"workspace_id"`
	WorkspacePath string                 `json:"workspace_path"`
	Name          string                 `json:"name"`
	Description   string                 `json:"description,omitempty"`
	Icon          string                 `json:"icon,omitempty"`
	Entrypoint    string                 `json:"entrypoint"`
	Arguments     []string               `json:"arguments,omitempty"`
	Inputs        []WorkspaceActionInput `json:"inputs,omitempty"`
	Pinned        bool                   `json:"pinned"`
	SortIndex     int                    `json:"sort_index"`
}

type WorkspaceActionRun struct {
	ID         string `json:"id"`
	ActionID   string `json:"action_id"`
	ActionName string `json:"action_name"`
	Status     string `json:"status"`
}

func (c *API) ListWorkspaceActions(ctx context.Context, workspacePath string) ([]WorkspaceAction, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return nil, errors.New("workspace path is required")
	}
	var resp struct {
		Actions []WorkspaceAction `json:"actions"`
	}
	path := "/v1/workspace/actions?workspace_path=" + url.QueryEscape(workspacePath)
	if err := c.getJSON(ctx, path, &resp, true); err != nil {
		return nil, err
	}
	return append([]WorkspaceAction(nil), resp.Actions...), nil
}

func (c *API) StartWorkspaceAction(ctx context.Context, workspacePath, actionID string, inputs map[string]string) (WorkspaceActionRun, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	actionID = strings.TrimSpace(actionID)
	if workspacePath == "" {
		return WorkspaceActionRun{}, errors.New("workspace path is required")
	}
	if actionID == "" {
		return WorkspaceActionRun{}, errors.New("action id is required")
	}
	var resp struct {
		Run WorkspaceActionRun `json:"run"`
	}
	payload := map[string]any{"workspace_path": workspacePath, "action_id": actionID, "inputs": inputs}
	if err := c.postJSON(ctx, "/v1/workspace/actions/run", payload, &resp, true); err != nil {
		return WorkspaceActionRun{}, err
	}
	if strings.TrimSpace(resp.Run.ID) == "" {
		return WorkspaceActionRun{}, errors.New("action launch returned no run")
	}
	return resp.Run, nil
}
