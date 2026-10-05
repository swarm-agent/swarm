package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

type CreateProjectInput struct {
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
