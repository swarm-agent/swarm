package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type CreateProjectInput struct {
	ClientRequestID string                `json:"client_request_id"`
	Name           string                `json:"name"`
	Description    string                `json:"description,omitempty"`
	IconPNGDataURL string                `json:"icon_png_data_url,omitempty"`
	ThemeID        string                `json:"theme_id,omitempty"`
	Workspaces     []ProjectWorkspaceRef `json:"workspaces,omitempty"`
}

type ProjectContextGeneration struct {
	Status      string `json:"status"`
	Attempt     int    `json:"attempt"`
	LeaseUntil  int64  `json:"lease_until,omitempty"`
	Error       string `json:"error,omitempty"`
	RouterAlert string `json:"router_alert,omitempty"`
}

type ProjectRecord struct {
	ID                string                    `json:"id"`
	AccountID         string                    `json:"account_id"`
	Name              string                    `json:"name"`
	Description       string                    `json:"description,omitempty"`
	IconPNGDataURL    string                    `json:"icon_png_data_url,omitempty"`
	ThemeID           string                    `json:"theme_id,omitempty"`
	Workspaces        []ProjectWorkspaceRef     `json:"workspaces,omitempty"`
	ProjectContext    string                    `json:"project_context,omitempty"`
	ContextGeneration *ProjectContextGeneration `json:"context_generation,omitempty"`
	PrimarySessionID  string                    `json:"primary_session_id,omitempty"`
	CreatedAt         int64                     `json:"created_at"`
	UpdatedAt         int64                     `json:"updated_at"`
}

type ProjectWorkspaceRef struct {
	WorkspaceID   string `json:"workspace_id"`
	Path          string `json:"path"`
	WorkspaceName string `json:"workspace_name,omitempty"`
}

func (c *API) CreateProject(ctx context.Context, input CreateProjectInput) (ProjectRecord, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return ProjectRecord{}, errors.New("project name is required")
	}
	input.ClientRequestID = strings.TrimSpace(input.ClientRequestID)
	if input.ClientRequestID == "" {
		input.ClientRequestID = newSessionV3ClientRequestID("project-create")
	}
	var resp struct {
		Project ProjectRecord `json:"project"`
		ID      string        `json:"id"`
		Name    string        `json:"name"`
	}
	if err := c.postJSON(ctx, "/v3/projects", input, &resp, true); err != nil {
		return ProjectRecord{}, err
	}
	if resp.Project.ID != "" {
		return resp.Project, nil
	}
	if resp.ID != "" {
		return ProjectRecord{ID: resp.ID, Name: resp.Name}, nil
	}
	return ProjectRecord{}, errors.New("empty project response")
}

func (c *API) ListProjects(ctx context.Context) ([]ProjectRecord, error) {
	var resp struct {
		Projects []ProjectRecord `json:"projects"`
	}
	if err := c.getJSON(ctx, "/v3/projects", &resp, true); err != nil {
		return nil, err
	}
	return resp.Projects, nil
}

func (c *API) GetProject(ctx context.Context, id string) (ProjectRecord, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return ProjectRecord{}, errors.New("project id is required")
	}
	var resp struct {
		Project ProjectRecord `json:"project"`
		ID      string        `json:"id"`
	}
	if err := c.getJSON(ctx, "/v3/projects/"+id, &resp, true); err != nil {
		return ProjectRecord{}, err
	}
	if resp.Project.ID != "" {
		return resp.Project, nil
	}
	if resp.ID != "" {
		return ProjectRecord{ID: resp.ID}, nil
	}
	return ProjectRecord{}, errors.New("empty project response")
}

func (c *API) UpdateProject(ctx context.Context, id string, patch map[string]any) (ProjectRecord, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return ProjectRecord{}, errors.New("project id is required")
	}
	status, body, err := c.request(ctx, http.MethodPatch, "/v3/projects/"+id, patch, true)
	if err != nil {
		return ProjectRecord{}, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return ProjectRecord{}, decodeAPIError(status, body)
	}
	var rec ProjectRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		return ProjectRecord{}, fmt.Errorf("decode /v3/projects/%s response: %w", id, err)
	}
	return rec, nil
}

type ProjectTaskRecord struct {
	ID              string `json:"id"`
	ProjectID       string `json:"project_id"`
	AccountID       string `json:"account_id,omitempty"`
	Title           string `json:"title"`
	Description     string `json:"description,omitempty"`
	Status          string `json:"status"` // queued, in_progress, needs_review, completed, failed
	SessionID       string `json:"session_id,omitempty"`
	ActiveAttemptID string `json:"active_attempt_id,omitempty"`
	Agent           string `json:"agent,omitempty"`
	WorkerID        string `json:"worker_id,omitempty"`
	WorkerName      string `json:"worker_name,omitempty"`
	WorktreeBranch  string `json:"worktree_branch,omitempty"`
	WorktreeName    string `json:"worktree_name,omitempty"`
	BaseBranch      string `json:"base_branch,omitempty"`
	GitStatus       string `json:"git_status,omitempty"`
	CreatedAt       int64  `json:"created_at,omitempty"`
	UpdatedAt       int64  `json:"updated_at,omitempty"`
}

func (c *API) ListProjectTasks(ctx context.Context, projectID string) ([]ProjectTaskRecord, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, errors.New("project id is required")
	}
	var resp struct {
		Tasks []ProjectTaskRecord `json:"tasks"`
		Count int                 `json:"count"`
	}
	if err := c.getJSON(ctx, "/v3/projects/"+projectID+"/tasks", &resp, true); err != nil {
		return nil, err
	}
	return resp.Tasks, nil
}

func (c *API) GetProjectTask(ctx context.Context, projectID, taskID string) (ProjectTaskRecord, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return ProjectTaskRecord{}, errors.New("project id is required")
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return ProjectTaskRecord{}, errors.New("task id is required")
	}
	var resp struct {
		Task ProjectTaskRecord `json:"task"`
	}
	if err := c.getJSON(ctx, "/v3/projects/"+projectID+"/tasks/"+taskID, &resp, true); err != nil {
		return ProjectTaskRecord{}, err
	}
	return resp.Task, nil
}

func (c *API) ListProjectSessions(ctx context.Context, projectID string) ([]SessionSummary, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, errors.New("project id is required")
	}
	path := "/v3/sessions?project_id=" + url.QueryEscape(projectID) + "&limit=10"
	var resp struct {
		OK       bool `json:"ok"`
		Sessions []struct {
			Session    SessionSummary      `json:"session"`
			Projection SessionV3Projection `json:"projection"`
		} `json:"sessions"`
	}
	if err := c.getJSON(ctx, path, &resp, true); err != nil {
		return nil, err
	}
	out := make([]SessionSummary, 0, len(resp.Sessions))
	for _, item := range resp.Sessions {
		out = append(out, markSessionV3(item.Session, item.Projection))
	}
	return out, nil
}

func (c *API) CreateProjectSession(ctx context.Context, projectID, title string) (SessionV3Hydrated, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return SessionV3Hydrated{}, errors.New("project id is required")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Orchestrator"
	}
	return c.CreateSessionV3WithOptions(ctx, SessionCreateOptions{
		ProjectID: projectID,
		Title:     title,
		AgentName: "system-orchestrator",
		Mode:      "auto",
	})
}

