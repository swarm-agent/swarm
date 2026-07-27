package client

import (
	"context"
	"errors"
	"strings"
)

const (
	WorkspaceAITaskModeAuto = "auto"
	WorkspaceAITaskModePlan = "plan"
)

type WorkspaceAITaskItem struct {
	ID               string `json:"id"`
	Text             string `json:"text"`
	AIState          string `json:"ai_state"`
	AIError          string `json:"ai_error,omitempty"`
	AIDisplayTitle   string `json:"ai_display_title,omitempty"`
	ManagedSessionID string `json:"managed_session_id,omitempty"`
}

type WorkspaceAITaskResponse struct {
	OK       bool                `json:"ok"`
	Item     WorkspaceAITaskItem `json:"item"`
	Status   string              `json:"status"`
	Replayed bool                `json:"replayed"`
}

func (c *API) CreateWorkspaceAITask(ctx context.Context, workspacePath, request, idempotencyKey, mode, originSessionID string) (WorkspaceAITaskResponse, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	request = strings.TrimSpace(request)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	mode = strings.ToLower(strings.TrimSpace(mode))
	originSessionID = strings.TrimSpace(originSessionID)
	if workspacePath == "" {
		return WorkspaceAITaskResponse{}, errors.New("workspace path is required")
	}
	if request == "" {
		return WorkspaceAITaskResponse{}, errors.New("enter a task request after /task")
	}
	if idempotencyKey == "" {
		return WorkspaceAITaskResponse{}, errors.New("AI task idempotency key is required")
	}
	if mode == "" {
		mode = WorkspaceAITaskModeAuto
	}
	if mode != WorkspaceAITaskModeAuto && mode != WorkspaceAITaskModePlan {
		return WorkspaceAITaskResponse{}, errors.New("AI task mode must be auto or plan")
	}
	payload := map[string]any{
		"action":         "ai_task",
		"workspace_path": workspacePath,
		"owner_kind":     "user",
		"text":           request,
		"mode":           mode,
	}
	if originSessionID != "" {
		payload["origin_session_id"] = originSessionID
	}
	var response WorkspaceAITaskResponse
	if err := c.postJSONWithHeaders(ctx, "/v1/workspace/todos", payload, &response, true, map[string]string{"Idempotency-Key": idempotencyKey}); err != nil {
		return WorkspaceAITaskResponse{}, err
	}
	if strings.TrimSpace(response.Item.ID) == "" {
		return WorkspaceAITaskResponse{}, errors.New("AI task request returned no task")
	}
	return response, nil
}
