package tool

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// manage_media in the catalog: the actions the Orchestrator's manage_media
// had (models, generate, list, retry, get, save, transcribe) plus read, which
// only Swarm Control had (swarm_read_media). One handler
// (Runtime.executeManageMedia) serves the Orchestrator, every agent with
// manage_media and Swarm Control; one service (MediaLifecycleService, the API
// server's ManageMedia) runs every action: a conversation's media are its
// session's project's, and a Swarm Control or linked caller (no
// conversation) runs the swarm_* media handlers with project and workspace.
// set_media_models is manage_setup's.

const manageMediaDescription = "Make and handle media using the session's project. models lists available models and options; generate takes kind and brief plus optional model, options and inputs (workspace paths, asset_id of a file attached to this chat, or earlier media job_id/file), returning a job_id. When the session tasks setting is ask, generate creates a task waiting for approval and returns its pending status. To narrate, generate with kind sound, operation speech, an explicit TTS model, text and voice (optional style, language and format); get returns audio and timing JSON and save writes both. Video defaults to a single clip. list returns this session's jobs newest first, optionally filtered by status and limit (default 20, maximum 100). retry takes job_id and reuses its kind, brief, inputs and options; model, brief and generation options can be overridden. It creates a new job with retry_of, with the same approval and spending checks as generate. get returns status, retry_of, files and costs, optionally waiting up to 30 seconds. save writes files into folder in this session's workspace without overwriting. transcribe takes one audio or video input (an attached file's asset_id, a workspace path or a media job file) and optional focus, and returns its text with timed segments, using the transcription model set in Media settings. read returns part of one file of a media job (project_id, task_id, file) as base64. Generation costs are charged to this session and spending limits apply. No project means generation is unavailable."

func manageMediaFields() []CatalogField {
	str := map[string]any{"type": "string"}
	described := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	whole := map[string]any{"type": "integer", "minimum": 0}
	fields := []CatalogField{
		{"machine", described("A linked machine's name (manage_fleet list_machines): run this media action there.")},
		{"kind", map[string]any{"type": "string", "enum": []string{"image", "video", "sound"}}},
	}
	for _, key := range []string{"brief", "model", "aspect_ratio", "resolution", "operation", "text", "voice", "style", "language", "format", "job_id", "folder", "status", "focus"} {
		fields = append(fields, CatalogField{key, str})
	}
	for _, key := range []string{"duration_seconds", "count", "wait_seconds", "limit"} {
		fields = append(fields, CatalogField{key, whole})
	}
	fields = append(fields,
		CatalogField{"inputs", map[string]any{"type": "array", "maxItems": 14, "items": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "asset_id": map[string]any{"type": "string"}, "job_id": map[string]any{"type": "string"}, "file": map[string]any{"type": "integer", "minimum": 1}}, "additionalProperties": false}}},
		CatalogField{"project", described("Swarm Control: the project (id or name) the media job belongs to; a conversation uses its own project.")},
		CatalogField{"workspace", described("Swarm Control: which of the project's folders, when it has several.")},
		CatalogField{"title", described("Swarm Control: a title for the media job.")},
		CatalogField{"include_unready", map[string]any{"type": "boolean", "description": "models: also models whose provider has no key yet."}},
		CatalogField{"project_id", described("read: the media job's project.")},
		CatalogField{"task_id", described("read: the media job's task.")},
		CatalogField{"file", map[string]any{"type": "integer", "minimum": 1, "description": "read: which file of the job, from 1. Default 1."}},
		CatalogField{"offset", map[string]any{"type": "integer", "minimum": 0, "description": "read: byte to start at. Default 0."}},
		CatalogField{"length", map[string]any{"type": "integer", "minimum": 1, "maximum": 2 * 1024 * 1024, "description": "read: bytes to read, at most 2 MiB (the default)."}},
	)
	return fields
}

func manageMediaCatalog() CatalogTool {
	core := []CatalogProfile{CatalogProfileEssentials, CatalogProfileFull, CatalogProfileOrchestrator}
	orchestrator := []CatalogProfile{CatalogProfileOrchestrator}
	options := []string{"model", "aspect_ratio", "resolution", "operation", "text", "voice", "style", "language", "format", "duration_seconds", "count", "inputs"}
	with := func(base []string, more ...string) []string {
		return append(append([]string(nil), base...), more...)
	}
	// Levels are remote.ToolScope of the swarm_* tool with the same effect:
	// swarm_media_models (read), swarm_request_media (write, generate and
	// retry), swarm_get_job (read), swarm_save_media (write) and
	// swarm_read_media (read). list and transcribe read this conversation's
	// own jobs and attached files: only a conversation has them.
	actions := []CatalogAction{
		{Action: "models", Inputs: []string{"machine", "kind", "include_unready"}, Level: CatalogLevelRead, Reach: CatalogReachAny, Profiles: core, APIScopes: []string{"sessions:read"},
			Description: "The image, video and sound models this machine can use, the default per kind and each model's options (swarm_media_models)."},
		{Action: "generate", Inputs: with([]string{"machine", "kind", "brief", "project", "workspace", "title", "wait_seconds"}, options...), Level: CatalogLevelWrite, Reach: CatalogReachAny, Profiles: core, APIScopes: []string{"sessions:write"},
			Description: "Make an image, video or sound from kind and brief (swarm_request_media); returns a job_id. Every result costs money with the provider."},
		{Action: "list", Inputs: []string{"status", "limit"}, Level: CatalogLevelRead, Reach: CatalogReachHome, Profiles: orchestrator, APIScopes: []string{"sessions:read"},
			Description: "This conversation's media jobs, newest first."},
		{Action: "retry", Inputs: with([]string{"machine", "job_id", "brief", "project", "workspace", "wait_seconds"}, options...), Level: CatalogLevelWrite, Reach: CatalogReachAny, Profiles: core, APIScopes: []string{"sessions:write"},
			Description: "A new media job from an earlier one (job_id), optionally overriding model, brief or generation options (swarm_request_media retry_of)."},
		{Action: "get", Inputs: []string{"machine", "job_id", "wait_seconds"}, Level: CatalogLevelRead, Reach: CatalogReachAny, Profiles: core, APIScopes: []string{"sessions:read"}, Untrusted: true,
			Description: "A media job's status, retry_of, files and costs, optionally waiting (swarm_get_job)."},
		{Action: "save", Inputs: []string{"machine", "job_id", "folder"}, Level: CatalogLevelWrite, Reach: CatalogReachAny, Profiles: core, APIScopes: []string{"sessions:write"},
			Description: "Write a media job's files into a folder of its workspace, never overwriting (swarm_save_media)."},
		{Action: "transcribe", Inputs: []string{"inputs", "focus"}, Level: CatalogLevelWrite, Reach: CatalogReachHome, Profiles: orchestrator, APIScopes: []string{"sessions:write"},
			Description: "Transcribe one audio or video input: its text with timed segments."},
		{Action: "read", Inputs: []string{"machine", "project_id", "task_id", "file", "offset", "length"}, Level: CatalogLevelRead, Reach: CatalogReachAny, Profiles: core, APIScopes: []string{"sessions:read"},
			Description: "Part of one file a media job made, base64, at most 2 MiB a call: read from offset 0 until done (swarm_read_media)."},
	}
	nextActions := map[string]string{
		"models":   "generate with a model and options it takes makes media.",
		"generate": "get with the job_id reads its files when it is done.",
		"retry":    "get with the job_id reads its files when it is done.",
		"list":     "get with a job_id reads one job.",
	}
	for i := range actions {
		actions[i].Tool = ManageMediaToolName
		actions[i].NextAction = nextActions[actions[i].Action]
	}
	return CatalogTool{Name: ManageMediaToolName, Description: manageMediaDescription, Fields: manageMediaFields(), Actions: actions}
}

// executeManageMediaCatalog runs one manage_media action and returns its
// answer in the catalog envelope. A conversation's action runs the media
// service for its session (MediaLifecycleService); read, an action with
// machine, and every Swarm Control call run CatalogActionService, the
// swarm_* media handlers.
func (r *Runtime) executeManageMediaCatalog(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	catalog := catalogDeclared(ManageMediaToolName)
	name := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	declared, ok := catalog.Action(name)
	if !ok {
		return "", fmt.Errorf("unknown manage_media action %q; use one of: %s", name, strings.Join(catalogActionNames(catalog), ", "))
	}
	call := make(map[string]any, len(args))
	for key, value := range args {
		call[key] = value
	}
	call["action"] = declared.Action
	machine := strings.TrimSpace(asString(call["machine"]))
	if machine == "" {
		delete(call, "machine")
	}
	if scope.ControlCaller || machine != "" || declared.Action == "read" {
		if !declared.ControlCallable() && scope.ControlCaller {
			return "", fmt.Errorf("manage_media %s reads this conversation's own media; Swarm Control has no conversation", declared.Action)
		}
		answer, err := r.catalogServiceAnswer(ctx, scope, catalog, declared, call)
		if err != nil {
			return "", err
		}
		return r.catalogEnvelope(catalog, declared.Action, call, answer, machine != "")
	}
	service, ok := r.projectTaskLifecycle.(MediaLifecycleService)
	if !ok {
		return "", errors.New("media lifecycle service unavailable")
	}
	result, err := service.ManageMedia(ctx, scope, call)
	if err != nil {
		return "", err
	}
	answer, err := catalogServiceJSON(result)
	if err != nil {
		return "", err
	}
	return r.catalogEnvelope(catalog, declared.Action, call, answer, false)
}
