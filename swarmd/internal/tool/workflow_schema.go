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
	return Definition{Type: "function", Name: "manage_workflows", Description: "Manage workflows that run when tasks or worker jobs end. Propose new workflows or changes for the person to accept or discard; never self-accept. Enable and disable existing workflows immediately. Call help for examples.", Parameters: map[string]any{
		"type": "object", "required": []string{"action"}, "properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"list", "get", "deliveries", "history", "propose", "disable", "enable", "help"}},
			"id":     map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}, "job_id": map[string]any{"type": "string"},
			"project": map[string]any{"type": "string"}, "workspace": map[string]any{"type": "string"},
			"scope": map[string]any{"type": "object", "properties": map[string]any{"role": map[string]any{"type": "string"}, "worker": map[string]any{"type": "string"}, "job": map[string]any{"type": "string"}, "schedule": map[string]any{"type": "string"}}},
			"when":  map[string]any{"type": "string", "enum": []string{"succeeded", "failed", "always"}}, "enabled": map[string]any{"type": "boolean"}, "retries": map[string]any{"type": "integer"}, "revision": map[string]any{"type": "integer"},
			"result": map[string]any{"type": "object"}, "steps": map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "description": "Ordered agent/reopen/run/deliver steps; help describes fields and examples."},
		}}}
}
