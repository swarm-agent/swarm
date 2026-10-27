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
	for _, key := range []string{"brief", "model", "aspect_ratio", "resolution", "operation", "text", "voice", "style", "language", "format", "job_id", "folder", "status", "focus"} {
		properties[key] = map[string]any{"type": "string"}
	}
	properties["action"] = map[string]any{"type": "string", "enum": []string{"models", "generate", "list", "retry", "get", "save", "transcribe"}}
	properties["kind"] = map[string]any{"type": "string", "enum": []string{"image", "video", "sound"}}
	for _, key := range []string{"duration_seconds", "count", "wait_seconds", "limit"} {
		properties[key] = map[string]any{"type": "integer", "minimum": 0}
	}
	properties["inputs"] = map[string]any{"type": "array", "maxItems": 14, "items": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "asset_id": map[string]any{"type": "string"}, "job_id": map[string]any{"type": "string"}, "file": map[string]any{"type": "integer", "minimum": 1}}, "additionalProperties": false}}
	return Definition{Type: "function", Name: ManageMediaToolName, Description: "Make and handle media using the session's project. models lists available models and options; generate takes kind and brief plus optional model, options and inputs (workspace paths, asset_id of a file attached to this chat, or earlier media job_id/file), returning a job_id. When the session tasks setting is ask, generate creates a task waiting for approval and returns its pending status. To narrate, generate with kind sound, operation speech, an explicit TTS model, text and voice (optional style, language and format); get returns audio and timing JSON and save writes both. Video defaults to a single clip. list returns this session's jobs newest first, optionally filtered by status and limit (default 20, maximum 100). retry takes job_id and reuses its kind, brief, inputs and options; model, brief and generation options can be overridden. It creates a new job with retry_of, with the same approval and spending checks as generate. get returns status, retry_of, files and costs, optionally waiting up to 30 seconds. save writes files into folder in this session's workspace without overwriting. transcribe takes one audio or video input (an attached file's asset_id, a workspace path or a media job file) and optional focus, and returns its text with timed segments, using the transcription model set in Media settings. Generation costs are charged to this session and spending limits apply. No project means generation is unavailable.", Parameters: map[string]any{"type": "object", "properties": properties, "required": []string{"action"}, "additionalProperties": false}}
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
