package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"swarm/packages/swarmd/internal/identity"
)

// ProjectInspectionRequest selects a catalog source or one exact task attempt.
// Paths and status labels are never substitutes for the task/session identity.
type ProjectInspectionRequest struct {
	ProjectID           string `json:"project_id"`
	WorkspacePath       string `json:"workspace_path,omitempty"`
	WorkspaceID         string `json:"workspace_id,omitempty"`
	WorkspaceGeneration int64  `json:"workspace_generation,omitempty"`
	TaskID              string `json:"task_id,omitempty"`
	AttemptID           string `json:"attempt_id,omitempty"`
	SessionID           string `json:"source_session_id,omitempty"`
	HeadCommit          string `json:"head_commit,omitempty"`
}

type ProjectInspectionTarget struct {
	Reference ProjectInspectionRequest `json:"reference"`
	Root      string                   `json:"workspace_path"`
	Base      string                   `json:"base_commit,omitempty"`
	Branch    string                   `json:"branch,omitempty"`
}

type projectInspectionResolver interface {
	ResolveProjectInspection(context.Context, identity.Principal, string, ProjectInspectionRequest) (ProjectInspectionTarget, error)
}

func (r *Runtime) resolveProjectInspection(ctx context.Context, scope WorkspaceScope, args map[string]any) (ProjectInspectionTarget, error) {
	resolver, ok := r.projectTaskLifecycle.(projectInspectionResolver)
	if !ok {
		return ProjectInspectionTarget{}, errors.New("project inspection capability unavailable; no checkout or command authority was granted")
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return ProjectInspectionTarget{}, err
	}
	var req ProjectInspectionRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return ProjectInspectionTarget{}, err
	}
	return resolver.ResolveProjectInspection(ctx, scope.Principal, scope.SessionID, req)
}

func (r *Runtime) inspectProjectFiles(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	target, err := r.resolveProjectInspection(ctx, scope, args)
	if err != nil {
		return "", err
	}
	response := map[string]any{"target": target, "inspection_only": true, "guidance": "Inspection is not testing, acceptance or integration. For validation use manage_environments with project_result equal to target.reference, then ensure/exec/release with the returned receipts. Never run against the source checkout instead."}
	if operation, ok := args["inspection"].(map[string]any); ok {
		name := strings.TrimSpace(asString(operation["tool"]))
		switch name {
		case "read", "list", "search", "find":
		default:
			return "", errors.New("inspection.tool must be read, list, search or find; mutations and commands are not inspection")
		}
		parameters, ok := operation["arguments"].(map[string]any)
		if !ok {
			return "", errors.New("inspection.arguments must be an object")
		}
		raw, err := json.Marshal(parameters)
		if err != nil {
			return "", err
		}
		// This scope lasts only for the selected read. It is never persisted as
		// ambient authority, nor passed to arbitrary/custom tools.
		selected := WorkspaceScope{PrimaryPath: target.Root, ReadOnlyRoots: []string{target.Root}, RejectScopeExpansion: true, Principal: scope.Principal, SessionID: scope.SessionID}
		out, err := r.executeOne(ctx, selected, Call{Name: name, Arguments: string(raw)}, nil)
		if err != nil {
			return "", err
		}
		response["inspection"] = json.RawMessage(out)
	}
	raw, err := json.Marshal(response)
	return string(raw), err
}

func projectResultDefinition() map[string]any {
	return map[string]any{"type": "object", "description": "Exact target.reference returned by manage_projects inspect_files for an isolated committed task result. Revalidated on every environment call; source checkouts are not validation targets.", "properties": map[string]any{
		"project_id":           map[string]any{"type": "string"},
		"workspace_path":       map[string]any{"type": "string"},
		"workspace_id":         map[string]any{"type": "string"},
		"workspace_generation": map[string]any{"type": "integer"},
		"task_id":              map[string]any{"type": "string"},
		"attempt_id":           map[string]any{"type": "string"},
		"source_session_id":    map[string]any{"type": "string"},
		"head_commit":          map[string]any{"type": "string"},
	}, "required": []string{"project_id", "task_id", "attempt_id", "source_session_id", "head_commit"}, "additionalProperties": false}
}
