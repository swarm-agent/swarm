package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
)

const ManageMediaToolName = "manage_media"

// MediaLifecycleService reuses the daemon's media task authority without MCP.
type MediaLifecycleService interface {
	ManageMedia(context.Context, WorkspaceScope, map[string]any) (any, error)
}

func manageMediaDefinition() Definition {
	properties := map[string]any{}
	for _, key := range []string{"brief", "model", "aspect_ratio", "resolution", "operation", "job_id", "folder"} {
		properties[key] = map[string]any{"type": "string"}
	}
	properties["action"] = map[string]any{"type": "string", "enum": []string{"models", "generate", "get", "save"}}
	properties["kind"] = map[string]any{"type": "string", "enum": []string{"image", "video", "sound"}}
	for _, key := range []string{"duration_seconds", "count", "wait_seconds"} {
		properties[key] = map[string]any{"type": "integer", "minimum": 0}
	}
	properties["inputs"] = map[string]any{"type": "array", "maxItems": 14, "items": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "job_id": map[string]any{"type": "string"}, "file": map[string]any{"type": "integer", "minimum": 1}}, "additionalProperties": false}}
	return Definition{Type: "function", Name: ManageMediaToolName, Description: "Make and handle media using the session's project. models lists available models and options; generate takes kind and brief plus optional model, options and inputs (workspace paths or earlier media job_id/file), returning a job_id. When the session tasks setting is ask, generate creates a task waiting for approval and returns its pending status. Video defaults to a single clip. get returns status, files and costs, optionally waiting up to 30 seconds. save writes files into folder in this session's workspace without overwriting. Generation costs are charged to this session and spending limits apply. No project means generation is unavailable.", Parameters: map[string]any{"type": "object", "properties": properties, "required": []string{"action"}, "additionalProperties": false}}
}

func (r *Runtime) executeManageMedia(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	service, ok := r.projectTaskLifecycle.(MediaLifecycleService)
	if !ok {
		return "", errors.New("media lifecycle service unavailable")
	}
	result, err := service.ManageMedia(ctx, scope, args)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(result)
	return string(data), err
}

// OpenMediaWorkspacePath applies workspace protection and ownership checks.
// The caller must retain and close the root and perform all I/O relative to it.
func OpenMediaWorkspacePath(scope WorkspaceScope, path string, write bool) (*os.Root, string, error) {
	rooted, err := openRootedWorkspacePath(scope, path)
	if err != nil {
		return nil, "", err
	}
	if write && !rooted.mutable {
		rooted.root.Close()
		return nil, "", errors.New("media save is outside the owned mutation scope")
	}
	return rooted.root, rooted.relative, nil
}
