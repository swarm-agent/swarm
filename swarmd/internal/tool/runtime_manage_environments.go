package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"swarm-refactor/swarmtui/pkg/environments"
)

func manageEnvironmentsDefinition() Definition {
	return Definition{
		Type:        "function",
		Name:        "manage_environments",
		Description: "List, get, create, update, delete, set default testbench, export, and import reusable environment definitions. Environment definitions are static and contain no ephemeral runtime state; they specify container configuration, source provisioning strategy, and preferred connection.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"description": "Action: list|get|create|update|delete|set_default_test|export|import",
					"enum":        []string{"list", "get", "create", "update", "delete", "set_default_test", "export", "import"},
				},
				"workspace_path": map[string]any{
					"type":        "string",
					"description": "Optional workspace path; defaults to current/active workspace scope",
				},
				"id": map[string]any{
					"type":        "string",
					"description": "Environment ID for get/update/delete/set_default_test/export, or explicit ID for create",
				},
				"environment_id": map[string]any{
					"type":        "string",
					"description": "Alias for id",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "Display name of the environment",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "Optional description of the environment",
				},
				"mode": map[string]any{
					"type":        "string",
					"description": "Environment mode: attached|deployable (default deployable)",
					"enum":        []string{"attached", "deployable"},
				},
				"role": map[string]any{
					"type":        "string",
					"description": "Environment role: development|testing|build|custom (default testing)",
					"enum":        []string{"development", "testing", "build", "custom"},
				},
				"preferred_connection_id": map[string]any{
					"type":        "string",
					"description": "Optional preferred Connection ID for this environment (Tier 2 connection resolution)",
				},
				"image": map[string]any{
					"type":        "string",
					"description": "Container image (e.g. golang:1.24, node:20, ubuntu:22.04)",
				},
				"container": map[string]any{
					"type":        "object",
					"description": "Container specification. Call action='help' for schema.",
				},
				"provisioning": map[string]any{
					"type":        "object",
					"description": "Workspace source provisioning strategy. Call action='help' for schema.",
				},
				"deployment_policy": map[string]any{
					"type":        "object",
					"description": "Deployment policy. Call action='help' for schema.",
				},
				"health_check": map[string]any{
					"type":        "object",
					"description": "Health check definition. Call action='help' for schema.",
				},
				"resources": map[string]any{
					"type":        "object",
					"description": "Resource limits. Call action='help' for schema.",
				},
				"labels": map[string]any{
					"type":        "object",
					"description": "Metadata labels. Call action='help' for schema.",
				},
				"set_default_test": map[string]any{
					"type":        "boolean",
					"description": "If true, sets this as default test environment",
				},
				"json": map[string]any{
					"type":        "string",
					"description": "JSON representation for import action",
				},
				"environment": map[string]any{
					"type":        "object",
					"description": "Complete environment definition object. Call action='help' for schema.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum environments to list",
				},
			},
			"required":             []string{"action"},
			"additionalProperties": false,
		},
	}
}

func (r *Runtime) executeManageEnvironments(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	if r == nil || r.environmentsStore == nil {
		return "", errors.New("manage_environments environment store is not configured")
	}

	actionName := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	if actionName == "" {
		actionName = "list"
	}

	accountScopeID, workspaceID, workspacePath, err := r.resolveWorkspaceScopeForEnvironments(scope, args, "manage_environments")
	if err != nil {
		return "", err
	}

	response := map[string]any{
		"tool":           "manage_environments",
		"action":         actionName,
		"status":         "ok",
		"workspace_id":   workspaceID,
		"workspace_path": workspacePath,
		"path_id":        toolPathID("manage_environments"),
	}

	switch actionName {
	case "list":
		limit := asInt(args["limit"], 100)
		envs, err := r.environmentsStore.List(accountScopeID, workspaceID, limit)
		if err != nil {
			return "", fmt.Errorf("list environments: %w", err)
		}
		response["environments"] = envs
		response["count"] = len(envs)
		if r.workspaceSettings != nil {
			if ws, found, _ := r.workspaceSettings.GetWorkspaceSettings(accountScopeID, workspaceID); found {
				response["default_test_environment_id"] = ws.DefaultTestEnvironmentID
			}
		}

	case "get":
		id := firstNonEmptyString(strings.TrimSpace(asString(args["id"])), strings.TrimSpace(asString(args["environment_id"])))
		if id == "" {
			return "", errors.New("id is required for get action")
		}
		env, found, err := r.environmentsStore.Get(accountScopeID, workspaceID, id)
		if err != nil {
			return "", fmt.Errorf("get environment %q: %w", id, err)
		}
		if !found {
			return "", fmt.Errorf("environment %q not found", id)
		}
		response["environment"] = env
		if r.workspaceSettings != nil {
			if ws, foundWS, _ := r.workspaceSettings.GetWorkspaceSettings(accountScopeID, workspaceID); foundWS {
				response["is_default_test"] = ws.DefaultTestEnvironmentID == id
			}
		}

	case "create":
		env, err := parseEnvironmentInput(args, accountScopeID, workspaceID)
		if err != nil {
			return "", err
		}

		if err := env.Validate(); err != nil {
			return "", fmt.Errorf("invalid environment definition: %w", err)
		}

		saved, err := r.environmentsStore.Save(env)
		if err != nil {
			return "", fmt.Errorf("save environment: %w", err)
		}
		response["environment"] = saved

		if asBool(args["set_default_test"]) && r.workspaceSettings != nil {
			_, _ = r.workspaceSettings.UpdateWorkspaceSettings(accountScopeID, workspaceID, &saved.ID, nil)
			response["is_default_test"] = true
		}

	case "update":
		id := firstNonEmptyString(strings.TrimSpace(asString(args["id"])), strings.TrimSpace(asString(args["environment_id"])))
		if id == "" {
			return "", errors.New("id is required for update action")
		}
		current, found, err := r.environmentsStore.Get(accountScopeID, workspaceID, id)
		if err != nil {
			return "", fmt.Errorf("get environment %q: %w", id, err)
		}
		if !found {
			return "", fmt.Errorf("environment %q not found", id)
		}

		updated, err := applyEnvironmentUpdates(current, args)
		if err != nil {
			return "", err
		}

		if err := updated.Validate(); err != nil {
			return "", fmt.Errorf("invalid environment update: %w", err)
		}

		saved, err := r.environmentsStore.Save(updated)
		if err != nil {
			return "", fmt.Errorf("update environment: %w", err)
		}
		response["environment"] = saved

		if asBool(args["set_default_test"]) && r.workspaceSettings != nil {
			_, _ = r.workspaceSettings.UpdateWorkspaceSettings(accountScopeID, workspaceID, &saved.ID, nil)
			response["is_default_test"] = true
		}

	case "delete":
		id := firstNonEmptyString(strings.TrimSpace(asString(args["id"])), strings.TrimSpace(asString(args["environment_id"])))
		if id == "" {
			return "", errors.New("id is required for delete action")
		}
		deleted, err := r.environmentsStore.Delete(accountScopeID, workspaceID, id)
		if err != nil {
			return "", fmt.Errorf("delete environment %q: %w", id, err)
		}
		if !deleted {
			return "", fmt.Errorf("environment %q not found", id)
		}
		response["id"] = id

		// Clear from workspace settings if it was the default test environment
		if r.workspaceSettings != nil {
			if ws, found, _ := r.workspaceSettings.GetWorkspaceSettings(accountScopeID, workspaceID); found && ws.DefaultTestEnvironmentID == id {
				empty := ""
				_, _ = r.workspaceSettings.UpdateWorkspaceSettings(accountScopeID, workspaceID, &empty, nil)
			}
		}

	case "set_default_test":
		id := firstNonEmptyString(strings.TrimSpace(asString(args["id"])), strings.TrimSpace(asString(args["environment_id"])))
		if id != "" && id != "none" {
			_, found, err := r.environmentsStore.Get(accountScopeID, workspaceID, id)
			if err != nil {
				return "", fmt.Errorf("check environment %q: %w", id, err)
			}
			if !found {
				return "", fmt.Errorf("environment %q not found", id)
			}
		}
		if id == "none" {
			id = ""
		}
		if r.workspaceSettings == nil {
			return "", errors.New("workspace settings store is not configured")
		}
		_, err := r.workspaceSettings.UpdateWorkspaceSettings(accountScopeID, workspaceID, &id, nil)
		if err != nil {
			return "", fmt.Errorf("set default test environment: %w", err)
		}
		response["default_test_environment_id"] = id

	case "export":
		id := firstNonEmptyString(strings.TrimSpace(asString(args["id"])), strings.TrimSpace(asString(args["environment_id"])))
		if id == "" {
			return "", errors.New("id is required for export action")
		}
		env, found, err := r.environmentsStore.Get(accountScopeID, workspaceID, id)
		if err != nil {
			return "", fmt.Errorf("get environment %q: %w", id, err)
		}
		if !found {
			return "", fmt.Errorf("environment %q not found", id)
		}
		exportedJSON, err := json.MarshalIndent(env, "", "  ")
		if err != nil {
			return "", fmt.Errorf("export environment to json: %w", err)
		}
		response["environment"] = env
		response["json"] = string(exportedJSON)

	case "import":
		var importedEnv environments.Environment
		if rawJSON := strings.TrimSpace(asString(args["json"])); rawJSON != "" {
			if err := json.Unmarshal([]byte(rawJSON), &importedEnv); err != nil {
				return "", fmt.Errorf("invalid json in import action: %w", err)
			}
		} else if envObj, ok := args["environment"].(map[string]any); ok && envObj != nil {
			rawBytes, err := json.Marshal(envObj)
			if err != nil {
				return "", fmt.Errorf("invalid environment object in import action: %w", err)
			}
			if err := json.Unmarshal(rawBytes, &importedEnv); err != nil {
				return "", fmt.Errorf("unmarshal environment object: %w", err)
			}
		} else {
			return "", errors.New("import requires either 'json' string or 'environment' object")
		}

		// Enforce workspace isolation on imported environment
		importedEnv.AccountScopeID = accountScopeID
		importedEnv.WorkspaceID = workspaceID
		if strings.TrimSpace(importedEnv.ID) == "" {
			importedEnv.ID = "env_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
		}
		if strings.TrimSpace(importedEnv.Name) == "" {
			return "", errors.New("imported environment must have a name")
		}

		if err := importedEnv.Validate(); err != nil {
			return "", fmt.Errorf("imported environment validation failed: %w", err)
		}

		saved, err := r.environmentsStore.Save(importedEnv)
		if err != nil {
			return "", fmt.Errorf("save imported environment: %w", err)
		}
		response["environment"] = saved

	default:
		return "", fmt.Errorf("unsupported manage_environments action %q", actionName)
	}

	encoded, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func parseEnvironmentInput(args map[string]any, accountScopeID, workspaceID string) (environments.Environment, error) {
	// If full environment object was passed
	if envObj, ok := args["environment"].(map[string]any); ok && envObj != nil {
		rawBytes, err := json.Marshal(envObj)
		if err != nil {
			return environments.Environment{}, err
		}
		var env environments.Environment
		if err := json.Unmarshal(rawBytes, &env); err != nil {
			return environments.Environment{}, err
		}
		env.AccountScopeID = accountScopeID
		env.WorkspaceID = workspaceID
		if strings.TrimSpace(env.ID) == "" {
			env.ID = "env_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
		}
		return env, nil
	}

	name := strings.TrimSpace(asString(args["name"]))
	if name == "" {
		return environments.Environment{}, errors.New("name is required for environment creation")
	}

	id := strings.TrimSpace(asString(args["id"]))
	if id == "" {
		id = "env_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	}

	mode := environments.EnvironmentMode(strings.TrimSpace(asString(args["mode"])))
	if mode == "" {
		mode = environments.EnvironmentModeDeployable
	}

	role := environments.EnvironmentRole(strings.TrimSpace(asString(args["role"])))
	if role == "" {
		role = environments.EnvironmentRoleTesting
	}

	// Container definition
	var container environments.ContainerDefinition
	if cRaw, ok := args["container"].(map[string]any); ok && cRaw != nil {
		rawBytes, _ := json.Marshal(cRaw)
		_ = json.Unmarshal(rawBytes, &container)
	}
	if img := strings.TrimSpace(asString(args["image"])); img != "" {
		container.Image = img
	}
	if portsRaw, ok := args["ports"].([]any); ok && len(portsRaw) > 0 {
		rawBytes, _ := json.Marshal(portsRaw)
		var ports []environments.PortMapping
		if err := json.Unmarshal(rawBytes, &ports); err == nil {
			container.ExposedPorts = ports
		}
	}
	if container.Image == "" {
		return environments.Environment{}, errors.New("container image is required (specify 'image' or 'container.image')")
	}

	// Provisioning definition (source strategy)
	var prov environments.WorkspaceProvisioning
	if pRaw, ok := args["provisioning"].(map[string]any); ok && pRaw != nil {
		rawBytes, _ := json.Marshal(pRaw)
		_ = json.Unmarshal(rawBytes, &prov)
	}
	if prov.Strategy.Kind == "" {
		// Default to local mount if unspecified
		prov.Strategy.Kind = environments.SourceStrategyKindLocalMount
		prov.Strategy.LocalMount = &environments.LocalMountConfig{
			ContainerPath: "/app",
		}
	}

	// Deployment policy
	var policy environments.DeploymentPolicy
	if polRaw, ok := args["deployment_policy"].(map[string]any); ok && polRaw != nil {
		rawBytes, _ := json.Marshal(polRaw)
		_ = json.Unmarshal(rawBytes, &policy)
	}
	if mi := asInt(args["max_instances"], 0); mi > 0 {
		policy.MaxInstances = mi
	}
	if rb := strings.TrimSpace(asString(args["release_behavior"])); rb != "" {
		policy.ReleaseBehavior = environments.ReleaseBehavior(rb)
	}
	if _, ok := args["reuse"]; ok {
		policy.Reuse = asBool(args["reuse"])
	}
	if policy.MaxInstances <= 0 {
		policy.MaxInstances = 1
	}
	if policy.ReleaseBehavior == "" {
		policy.ReleaseBehavior = environments.ReleaseBehaviorNone
	}

	env := environments.Environment{
		ID:                    id,
		AccountScopeID:        accountScopeID,
		WorkspaceID:           workspaceID,
		Name:                  name,
		Description:           strings.TrimSpace(asString(args["description"])),
		Mode:                  mode,
		Role:                  role,
		PreferredConnectionID: strings.TrimSpace(asString(args["preferred_connection_id"])),
		Container:             container,
		Provisioning:          prov,
		DeploymentPolicy:      policy,
		Labels:                asStringMap(args["labels"]),
	}

	if hcRaw, ok := args["health_check"].(map[string]any); ok && hcRaw != nil {
		var hc environments.HealthCheck
		rawBytes, _ := json.Marshal(hcRaw)
		_ = json.Unmarshal(rawBytes, &hc)
		env.HealthCheck = &hc
	}

	if resRaw, ok := args["resources"].(map[string]any); ok && resRaw != nil {
		var res environments.ResourceRequirements
		rawBytes, _ := json.Marshal(resRaw)
		_ = json.Unmarshal(rawBytes, &res)
		env.Resources = &res
	}

	return env, nil
}

func applyEnvironmentUpdates(current environments.Environment, args map[string]any) (environments.Environment, error) {
	if name := strings.TrimSpace(asString(args["name"])); name != "" {
		current.Name = name
	}
	if desc, ok := args["description"].(string); ok {
		current.Description = strings.TrimSpace(desc)
	}
	if mode := strings.TrimSpace(asString(args["mode"])); mode != "" {
		current.Mode = environments.EnvironmentMode(mode)
	}
	if role := strings.TrimSpace(asString(args["role"])); role != "" {
		current.Role = environments.EnvironmentRole(role)
	}
	if prefID, ok := args["preferred_connection_id"].(string); ok {
		current.PreferredConnectionID = strings.TrimSpace(prefID)
	}
	if img := strings.TrimSpace(asString(args["image"])); img != "" {
		current.Container.Image = img
	}
	if cRaw, ok := args["container"].(map[string]any); ok && cRaw != nil {
		rawBytes, _ := json.Marshal(cRaw)
		_ = json.Unmarshal(rawBytes, &current.Container)
	}
	if pRaw, ok := args["provisioning"].(map[string]any); ok && pRaw != nil {
		rawBytes, _ := json.Marshal(pRaw)
		_ = json.Unmarshal(rawBytes, &current.Provisioning)
	}
	if polRaw, ok := args["deployment_policy"].(map[string]any); ok && polRaw != nil {
		rawBytes, _ := json.Marshal(polRaw)
		_ = json.Unmarshal(rawBytes, &current.DeploymentPolicy)
	}
	if hcRaw, ok := args["health_check"].(map[string]any); ok && hcRaw != nil {
		var hc environments.HealthCheck
		rawBytes, _ := json.Marshal(hcRaw)
		_ = json.Unmarshal(rawBytes, &hc)
		current.HealthCheck = &hc
	}
	if resRaw, ok := args["resources"].(map[string]any); ok && resRaw != nil {
		var res environments.ResourceRequirements
		rawBytes, _ := json.Marshal(resRaw)
		_ = json.Unmarshal(rawBytes, &res)
		current.Resources = &res
	}
	if labelsRaw, ok := args["labels"].(map[string]any); ok && labelsRaw != nil {
		current.Labels = asStringMap(labelsRaw)
	}
	return current, nil
}
