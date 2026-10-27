package tool

import (
	"context"
	"swarm/packages/swarmd/internal/identity"
)

// WorkflowLifecycleService is implemented by the API's shared workflow authority.
// Native callers may propose but have no acceptance operation.
type WorkflowLifecycleService interface {
	ManageWorkflows(context.Context, identity.Principal, string, map[string]any) (any, error)
}

func manageWorkflowsDefinition() Definition {
	return Definition{Type: "function", Name: "manage_workflows", Description: "Manage workflows that run when tasks or worker jobs end. Propose new workflows or changes for the person to accept or discard; never self-accept. Enable and disable existing workflows immediately. Call help for examples. for_each queues one independent worker job per object in a committed JSON array with bounded parallelism. " + WorkflowTriggerDescription, Parameters: map[string]any{
		"type": "object", "required": []string{"action"}, "properties": map[string]any{
			"inputs": WorkflowInputsSchema(), "with": WorkflowWithSchema(),
			"on":         WorkflowTriggerSchema(),
			"rotate_key": map[string]any{"type": "boolean", "description": "Webhook key rotation is not yet supported for native proposals; use the person's HTTP save or MCP path."},
			"action":     map[string]any{"type": "string", "enum": []string{"list", "get", "deliveries", "history", "propose", "disable", "enable", "help"}},
			"id":         map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}, "job_id": map[string]any{"type": "string"},
			"project": map[string]any{"type": "string"}, "workspace": map[string]any{"type": "string"},
			"scope": map[string]any{"type": "object", "properties": map[string]any{"role": map[string]any{"type": "string"}, "worker": map[string]any{"type": "string"}, "job": map[string]any{"type": "string"}, "schedule": map[string]any{"type": "string"}}},
			"when":  map[string]any{"type": "string", "enum": []string{"succeeded", "failed", "always"}}, "enabled": map[string]any{"type": "boolean"}, "retries": map[string]any{"type": "integer"}, "revision": map[string]any{"type": "integer"},
			"result": map[string]any{"type": "object"}, "steps": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"for_each": WorkflowForEachSchema()}}, "description": "Ordered agent/reopen/run/deliver steps; for_each queues one independent worker job per committed JSON item."},
		}}}
}

// WorkflowForEachSchema is shared by native and MCP workflow steps.
func WorkflowForEachSchema() map[string]any {
	return map[string]any{"type": "object", "required": []string{"file"}, "additionalProperties": false, "description": "for_each queues one worker job per object in a committed JSON array, with bounded parallelism and optional after_field dependencies on id_field (default id).", "properties": map[string]any{"file": map[string]any{"type": "string"}, "items": map[string]any{"type": "string"}, "max_parallel": map[string]any{"type": "integer", "minimum": 1, "maximum": 5, "default": 1}, "after_field": map[string]any{"type": "string"}, "id_field": map[string]any{"type": "string", "default": "id"}}}
}

// WorkflowInputsSchema is shared by the native and MCP workflow tools.
func WorkflowInputsSchema() map[string]any {
	return map[string]any{"type": "object", "maxProperties": 20, "propertyNames": map[string]any{"pattern": "^[a-z][a-z0-9_]{0,31}$"}, "additionalProperties": map[string]any{"type": "object", "properties": map[string]any{"description": map[string]any{"type": "string"}, "default": map[string]any{"type": "string", "maxLength": 1000}}, "additionalProperties": false}}
}

func WorkflowWithSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string", "maxLength": 1000}}
}
