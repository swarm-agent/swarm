package tool

import (
	"context"
	"swarm/packages/swarmd/internal/identity"
)

// WorkflowLifecycleService is implemented by the API's shared workflow
// authority: one function serves every manage_workflows action here, for the
// Orchestrator and Swarm Control alike. The caller is never the machine owner
// and has no acceptance operation; who names it (its session, or its AI key).
type WorkflowLifecycleService interface {
	ManageWorkflows(ctx context.Context, p identity.Principal, who string, args map[string]any) (any, error)
}

// WorkflowForEachSchema is shared by native and MCP workflow steps.
func WorkflowForEachSchema() map[string]any {
	return map[string]any{"type": "object", "required": []string{"file"}, "additionalProperties": false, "description": "for_each queues one worker job per object in a committed JSON array, with bounded parallelism and optional after_field dependencies on id_field (default id). Briefs support {{item.field}} (nested paths; missing fields become empty) and {{item}} (the whole object as compact, sorted-key JSON), within the 64 KiB worker-input limit. Item placeholders require for_each; unknown brief placeholder namespaces are refused.", "properties": map[string]any{"file": map[string]any{"type": "string"}, "items": map[string]any{"type": "string"}, "max_parallel": map[string]any{"type": "integer", "minimum": 1, "maximum": 5, "default": 1}, "after_field": map[string]any{"type": "string"}, "id_field": map[string]any{"type": "string", "default": "id"}}}
}

// WorkflowInputsSchema is shared by the native and MCP workflow tools.
func WorkflowInputsSchema() map[string]any {
	return map[string]any{"type": "object", "maxProperties": 20, "propertyNames": map[string]any{"pattern": "^[a-z][a-z0-9_]{0,31}$"}, "additionalProperties": map[string]any{"type": "object", "properties": map[string]any{"description": map[string]any{"type": "string"}, "default": map[string]any{"type": "string", "maxLength": 1000}}, "additionalProperties": false}}
}

func WorkflowWithSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string", "maxLength": 1000}}
}
