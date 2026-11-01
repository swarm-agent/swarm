package tool

import (
	"context"
	"fmt"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/jobautomation"
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

// WorkflowStepsSchema is shared by native and MCP workflow steps: at most
// jobautomation.MaxSteps, each with its for_each and if.
func WorkflowStepsSchema() map[string]any {
	return map[string]any{"type": "array", "maxItems": jobautomation.MaxSteps, "items": map[string]any{"type": "object", "properties": map[string]any{"for_each": WorkflowForEachSchema(), "if": WorkflowConditionsSchema()}}, "description": fmt.Sprintf("Ordered agent/reopen/run/deliver/rebase steps (at most %d); for_each queues one independent worker job per committed JSON item; {type: rebase, onto} rebases the head at that point.", jobautomation.MaxSteps)}
}

// WorkflowConditionsSchema is a step's if: conditions on earlier steps.
func WorkflowConditionsSchema() map[string]any {
	return map[string]any{"type": "array", "maxItems": jobautomation.MaxStepConditions, "description": "All must hold for the step to run, else it is skipped. Each names an earlier step and has status or verdict; output_contains (with status) also needs that literal, case-sensitive text in the step's reason or output tail, e.g. {step:\"gate\",status:\"failed\",output_contains:\"rebase needed\"}.", "items": map[string]any{"type": "object", "required": []string{"step"}, "additionalProperties": false, "properties": map[string]any{
		"step":            map[string]any{"type": "string"},
		"status":          map[string]any{"type": "string", "enum": []string{"succeeded", "failed", "error"}},
		"verdict":         map[string]any{"type": "string", "enum": []string{"ok", "block"}},
		"output_contains": map[string]any{"type": "string", "minLength": 1, "maxLength": jobautomation.MaxOutputContainsBytes},
	}}}
}

// WorkflowInputsSchema is shared by the native and MCP workflow tools.
func WorkflowInputsSchema() map[string]any {
	return map[string]any{"type": "object", "maxProperties": 20, "propertyNames": map[string]any{"pattern": "^[a-z][a-z0-9_]{0,31}$"}, "additionalProperties": map[string]any{"type": "object", "properties": map[string]any{"description": map[string]any{"type": "string"}, "default": map[string]any{"type": "string", "maxLength": 1000}}, "additionalProperties": false}}
}

func WorkflowWithSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string", "maxLength": 1000}}
}
