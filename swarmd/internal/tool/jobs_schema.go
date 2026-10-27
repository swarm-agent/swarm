package tool

import (
	"context"
	"swarm/packages/swarmd/internal/identity"
)

// JobsService shares the control job handlers with native agent callers.
type JobsService interface {
	ManageJobs(context.Context, identity.Principal, string, string, map[string]any) (any, error)
}

func manageJobsDefinition() Definition {
	return Definition{Type: "function", Name: "manage_jobs", Description: "Read worker runs, jobs and workflow deliveries. Actions require an explicit owner permission grant for this agent; self-targeting is forbidden. Read results over the inline limit return JSON text pages: repeat the same read with next_page_offset and concatenate json_page. No host files are created.", Parameters: map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"action"}, "properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"list_runs", "get_job", "deliveries", "rerun_failed_steps", "give_job", "message_job"}},
			"job_id": map[string]any{"type": "string"}, "id": map[string]any{"type": "string", "description": "Workflow ID for deliveries."},
			"worker": map[string]any{"type": "string"}, "brief": map[string]any{"type": "string"}, "message": map[string]any{"type": "string"},
			"project": map[string]any{"type": "string"}, "folder": map[string]any{"type": "string"},
			"since": map[string]any{"type": "string"}, "until": map[string]any{"type": "string"}, "status": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50}, "cursor": map[string]any{"type": "string"},
			"client_request_id": map[string]any{"type": "string"}, "idempotency_key": map[string]any{"type": "string"},
			"page_digest": map[string]any{"type": "string", "description": "Digest from the first page; required for continuation."},
			"page_offset": map[string]any{"type": "integer", "minimum": 0, "description": "UTF-8 byte offset returned by a previous oversized read; repeat identical filters."},
		}}}
}
