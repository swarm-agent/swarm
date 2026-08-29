package client

import (
	"context"
	"errors"
	"strings"
)

const routedTaskSessionsPath = "/v3/sessions:background-router"

type RoutedTaskSessionResponse struct {
	OK           bool           `json:"ok"`
	SessionID    string         `json:"session_id"`
	Title        string         `json:"title"`
	StartingMode string         `json:"starting_mode"`
	Replayed     bool           `json:"replayed"`
	Session      SessionSummary `json:"session"`
}

type RoutedTaskWorkspaceAuthority struct {
	WorkspacePath      string
	WorkspaceBindingID string
	SwarmID            string
}

// CreateRoutedTaskSession asks the dedicated background Router to create and
// start a task session. The caller supplies canonical source-workspace
// authority while the Router owns task naming and the worktree-name seed; the
// backend owns mandatory managed-worktree routing. Plan remains the only
// caller-owned routing intent.
func (c *API) CreateRoutedTaskSession(ctx context.Context, request, idempotencyKey string, planMode bool, originSessionID string, authority RoutedTaskWorkspaceAuthority) (RoutedTaskSessionResponse, error) {
	request = strings.TrimSpace(request)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	originSessionID = strings.TrimSpace(originSessionID)
	authority.WorkspacePath = strings.TrimSpace(authority.WorkspacePath)
	authority.WorkspaceBindingID = strings.TrimSpace(authority.WorkspaceBindingID)
	authority.SwarmID = strings.TrimSpace(authority.SwarmID)
	if request == "" {
		return RoutedTaskSessionResponse{}, errors.New("enter a task request after /task")
	}
	if idempotencyKey == "" {
		return RoutedTaskSessionResponse{}, errors.New("routed task idempotency key is required")
	}
	if authority.WorkspacePath == "" || authority.WorkspaceBindingID == "" || authority.SwarmID == "" {
		return RoutedTaskSessionResponse{}, errors.New("routed task requires source workspace path, binding, and swarm authority")
	}
	payload := map[string]any{
		"input":                      request,
		"client_request_id":          idempotencyKey,
		"idempotency_key":            idempotencyKey,
		"agent_name":                 "swarm",
		"plan_mode_requested":        planMode,
		"workspace_path":             authority.WorkspacePath,
		"host_workspace_path":        authority.WorkspacePath,
		"runtime_workspace_path":     authority.WorkspacePath,
		"workspace_binding_id":       authority.WorkspaceBindingID,
		"swarm_id":                   authority.SwarmID,
		"target_kind":                "host",
		"target_relationship":        "self",
		"metadata": map[string]any{
			"task_command": true,
		},
	}
	if originSessionID != "" {
		payload["metadata"].(map[string]any)["task_origin_session_id"] = originSessionID
	}
	var response RoutedTaskSessionResponse
	if err := c.postJSONWithHeaders(ctx, routedTaskSessionsPath, payload, &response, true, map[string]string{"Idempotency-Key": idempotencyKey}); err != nil {
		return RoutedTaskSessionResponse{}, err
	}
	if strings.TrimSpace(response.SessionID) == "" || strings.TrimSpace(response.Session.ID) != strings.TrimSpace(response.SessionID) {
		return RoutedTaskSessionResponse{}, errors.New("routed task returned no canonical session")
	}
	if strings.TrimSpace(response.Title) == "" && strings.TrimSpace(response.Session.Title) == "" {
		return RoutedTaskSessionResponse{}, errors.New("background Router returned no task title")
	}
	if !response.Session.WorktreeEnabled || strings.TrimSpace(response.Session.WorktreeRootPath) == "" {
		return RoutedTaskSessionResponse{}, errors.New("background Router did not create the required owned-worktree session")
	}
	wantMode := "auto"
	if planMode {
		wantMode = "plan"
	}
	if strings.ToLower(strings.TrimSpace(response.StartingMode)) != wantMode || strings.ToLower(strings.TrimSpace(response.Session.Mode)) != wantMode {
		return RoutedTaskSessionResponse{}, errors.New("routed task returned an unexpected starting mode")
	}
	return response, nil
}
