package tool

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	actionruntime "swarm/packages/swarmd/internal/action"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func manageActionsDefinition() Definition {
	inputSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":          map[string]any{"type": "string"},
			"label":       map[string]any{"type": "string"},
			"description": map[string]any{"type": "string"},
			"kind":        map[string]any{"type": "string", "description": "Input kind: text|secret"},
			"required":    map[string]any{"type": "boolean"},
			"placeholder": map[string]any{"type": "string"},
			"default":     map[string]any{"type": "string"},
			"arguments":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"required":             []string{"id", "label"},
		"additionalProperties": false,
	}
	return Definition{
		Type:        "function",
		Name:        "manage_actions",
		Description: "List, inspect, create, update, delete, or reorder private Actions for an account-owned workspace. This tool manages definitions only and cannot run Actions. Entrypoints must be workspace-relative and arguments are structured arrays, never persisted shell command strings.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":         map[string]any{"type": "string", "description": "Action: list|get|create|update|delete|reorder"},
				"workspace_path": map[string]any{"type": "string", "description": "Optional account-owned workspace path; defaults to the current workspace"},
				"id":             map[string]any{"type": "string", "description": "Action id for get/update/delete"},
				"name":           map[string]any{"type": "string"},
				"description":    map[string]any{"type": "string"},
				"icon":           map[string]any{"type": "string"},
				"entrypoint":     map[string]any{"type": "string", "description": "Workspace-relative executable or script path"},
				"arguments":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Fixed argv values passed without shell interpolation"},
				"inputs":         map[string]any{"type": "array", "items": inputSchema, "description": "Optional prompted input definitions whose argument templates are structured argv values"},
				"pinned":         map[string]any{"type": "boolean", "description": "Whether the Action appears in pinned quick-access lists"},
				"ordered_ids":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Every workspace Action id in the desired order"},
			},
			"required":             []string{"action"},
			"additionalProperties": false,
		},
	}
}

func (r *Runtime) executeManageActions(scope WorkspaceScope, args map[string]any) (string, error) {
	if r == nil || r.actions == nil {
		return "", errors.New("manage_actions service is not configured")
	}
	actionName := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	if actionName == "" {
		actionName = "list"
	}
	requestedPath := strings.TrimSpace(asString(args["workspace_path"]))
	if requestedPath == "" {
		requestedPath = "."
	}
	workspacePath, err := resolveWorkspacePath(scope, requestedPath)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(scope.Principal.AccountScopeID) == "" {
		return "", errors.New("manage_actions requires an authenticated account scope")
	}
	if r.workspace == nil {
		return "", errors.New("manage_actions workspace service is not configured")
	}
	workspaceScope, err := r.workspace.ScopeForPathForPrincipal(scope.Principal, workspacePath)
	if err != nil {
		return "", err
	}
	if !workspaceScope.Matched || strings.TrimSpace(workspaceScope.WorkspaceID) == "" || strings.TrimSpace(workspaceScope.WorkspacePath) == "" {
		return "", errors.New("manage_actions requires an account-owned canonical workspace")
	}
	canonical := actionruntime.Scope{AccountScopeID: scope.Principal.AccountScopeID, WorkspaceID: workspaceScope.WorkspaceID, WorkspacePath: workspaceScope.WorkspacePath}
	response := map[string]any{"tool": "manage_actions", "action": actionName, "status": "ok", "workspace_id": canonical.WorkspaceID, "workspace_path": canonical.WorkspacePath, "path_id": toolPathID("manage_actions"), "details_truncated": false}

	switch actionName {
	case "list":
		actions, err := r.actions.List(canonical)
		if err != nil {
			return "", err
		}
		response["actions"], response["count"] = actions, len(actions)
	case "get":
		id := strings.TrimSpace(asString(args["id"]))
		action, found, err := r.actions.Get(canonical, id)
		if err != nil {
			return "", err
		}
		if !found {
			return "", fmt.Errorf("action %q not found", id)
		}
		response["definition"] = action
	case "create":
		name, entrypoint := strings.TrimSpace(asString(args["name"])), strings.TrimSpace(asString(args["entrypoint"]))
		if name == "" || entrypoint == "" {
			return "", errors.New("create requires name and entrypoint")
		}
		inputs, err := parseManageActionInputs(args["inputs"])
		if err != nil {
			return "", err
		}
		arguments, err := parseExactStringSlice(args["arguments"], "arguments")
		if err != nil {
			return "", err
		}
		action, err := r.actions.Create(actionruntime.CreateInput{Scope: canonical, Name: name, Description: asString(args["description"]), Icon: asString(args["icon"]), Entrypoint: entrypoint, Arguments: arguments, Inputs: inputs, Pinned: asBool(args["pinned"])})
		if err != nil {
			return "", err
		}
		response["definition"] = action
	case "update":
		id := strings.TrimSpace(asString(args["id"]))
		if id == "" {
			return "", errors.New("update requires id")
		}
		input := actionruntime.UpdateInput{Scope: canonical, ID: id}
		setStringPointer(args, "name", &input.Name)
		setStringPointer(args, "description", &input.Description)
		setStringPointer(args, "icon", &input.Icon)
		setStringPointer(args, "entrypoint", &input.Entrypoint)
		if _, ok := args["pinned"]; ok {
			value := asBool(args["pinned"])
			input.Pinned = &value
		}
		if _, ok := args["arguments"]; ok {
			values, err := parseExactStringSlice(args["arguments"], "arguments")
			if err != nil {
				return "", err
			}
			input.Arguments = &values
		}
		if _, ok := args["inputs"]; ok {
			values, err := parseManageActionInputs(args["inputs"])
			if err != nil {
				return "", err
			}
			input.Inputs = &values
		}
		action, err := r.actions.Update(input)
		if err != nil {
			return "", err
		}
		response["definition"] = action
	case "delete":
		id := strings.TrimSpace(asString(args["id"]))
		deleted, err := r.actions.Delete(canonical, id)
		if err != nil {
			return "", err
		}
		if !deleted {
			return "", fmt.Errorf("action %q not found", id)
		}
		response["id"] = id
	case "reorder":
		orderedIDs := asStringSlice(args["ordered_ids"])
		if len(orderedIDs) == 0 {
			return "", errors.New("reorder requires ordered_ids")
		}
		actions, err := r.actions.Reorder(canonical, orderedIDs)
		if err != nil {
			return "", err
		}
		response["actions"], response["count"] = actions, len(actions)
	default:
		return "", fmt.Errorf("unsupported manage_actions action %q; execution is not available through this tool", actionName)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func parseManageActionInputs(raw any) ([]pebblestore.WorkspaceActionInput, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, errors.New("inputs must be an array")
	}
	inputs := make([]pebblestore.WorkspaceActionInput, 0, len(items))
	for index, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("input %d must be an object", index)
		}
		arguments, err := parseExactStringSlice(item["arguments"], fmt.Sprintf("input %d arguments", index))
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, pebblestore.WorkspaceActionInput{ID: asString(item["id"]), Label: asString(item["label"]), Description: asString(item["description"]), Kind: asString(item["kind"]), Required: asBool(item["required"]), Placeholder: asString(item["placeholder"]), Default: asString(item["default"]), Arguments: arguments})
	}
	return inputs, nil
}

func parseExactStringSlice(value any, field string) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	if typed, ok := value.([]string); ok {
		return append([]string(nil), typed...), nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array of strings", field)
	}
	out := make([]string, len(items))
	for index, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s item %d must be a string", field, index)
		}
		out[index] = text
	}
	return out, nil
}

func setStringPointer(args map[string]any, key string, target **string) {
	if raw, ok := args[key]; ok {
		value := asString(raw)
		*target = &value
	}
}
