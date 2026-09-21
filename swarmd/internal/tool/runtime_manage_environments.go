package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/lifecycle"
)

func manageEnvironmentsDefinition() Definition {
	return Definition{
		Type:        "function",
		Name:        "manage_environments",
		Description: "Unified environment manager for definitions, deployments, execution, supervised operations, summary, and daily history. Call action='help' for schema and usage guide.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type": "string",
					"enum": []string{
						"list", "get", "create", "update", "delete", "set_default_test", "export", "import",
						"list_deployments", "get_deployment", "ensure", "deploy", "exec", "start", "stop", "destroy", "release",
						"summary", "history", "get_operation", "cancel_operation", "help",
					},
					"description": "Operation action to perform",
				},
				"workspace_path": map[string]any{"type": "string", "description": "Workspace or isolated worktree root path"},
				"workspace_id":   map[string]any{"type": "string", "description": "Canonical workspace ID"},
				// Explicit entity identifiers (no ambiguous cross-object aliasing)
				"environment_id": map[string]any{"type": "string", "description": "Environment definition ID"},
				"deployment_id":  map[string]any{"type": "string", "description": "Deployment instance ID for runtime actions (exec, stop, start, destroy, release, get_deployment)"},
				"operation_id":   map[string]any{"type": "string", "description": "Operation ID for get_operation or cancel_operation"},
				"id":             map[string]any{"type": "string", "description": "Environment ID alias for backward compatibility with definition get/update/delete/export"},
				// Definition fields
				"name":                    map[string]any{"type": "string", "description": "Environment display name"},
				"description":             map[string]any{"type": "string"},
				"mode":                    map[string]any{"type": "string", "enum": []string{"attached", "deployable"}},
				"role":                    map[string]any{"type": "string", "enum": []string{"development", "testing", "build", "custom"}},
				"preferred_connection_id": map[string]any{"type": "string"},
				"connection_id":           map[string]any{"type": "string", "description": "Connection override for deploy/ensure"},
				"deployment_name":         map[string]any{"type": "string", "description": "Display name for deployment instance"},
				"image":                   map[string]any{"type": "string", "description": "Container image name:tag"},
				"container":               map[string]any{"type": "object"},
				"provisioning":            map[string]any{"type": "object"},
				"deployment_policy":       map[string]any{"type": "object"},
				"health_check":            map[string]any{"type": "object"},
				"resources":               map[string]any{"type": "object"},
				"labels":                  map[string]any{"type": "object"},
				"set_default_test":        map[string]any{"type": "boolean"},
				"json":                    map[string]any{"type": "string", "description": "JSON payload for import"},
				"environment":             map[string]any{"type": "object", "description": "Environment object for import"},
				// Runtime execution and supervised operation fields
				"command":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Command and arguments for exec action"},
				"working_dir":    map[string]any{"type": "string", "description": "Working directory inside container for exec"},
				"env":            map[string]any{"type": "object", "description": "Environment variables for exec"},
				"env_overrides":  map[string]any{"type": "object", "description": "Environment variable overrides for deploy/ensure"},
				"timeout_ms":     map[string]any{"type": "integer", "description": "Timeout in milliseconds for command or operation"},
				"deadline":       map[string]any{"type": "integer", "description": "Finite epoch millisecond deadline for operation"},
				"idempotency_key": map[string]any{"type": "string", "description": "Client idempotency key for operation deduplication"},
				"ttl_millis":     map[string]any{"type": "integer", "description": "Lease TTL in milliseconds for deploy/ensure (0 = default 1 hour)"},
				"consumer_type":  map[string]any{"type": "string", "enum": []string{"session", "test_run", "worker", "custom"}},
				"consumer_id":    map[string]any{"type": "string"},
				"lease_id":       map[string]any{"type": "string", "description": "Lease ID for release action"},
				"reason":         map[string]any{"type": "string", "description": "Reason for release, destroy, or cancel_operation"},
				"max_output":     map[string]any{"type": "integer", "description": "Maximum stdout/stderr bytes for exec"},
				// History query filters
				"status":        map[string]any{"type": "string", "description": "Filter history by status (queued, running, succeeded, failed, cancelled, timed_out, cleanup_failed, unknown)"},
				"action_filter": map[string]any{"type": "string", "description": "Filter history by action (ensure, deploy, exec, stop, release, destroy, cancel)"},
				"actor":         map[string]any{"type": "string", "description": "Filter history by actor user ID"},
				"session_id":    map[string]any{"type": "string", "description": "Filter history by session ID"},
				"worker_id":     map[string]any{"type": "string", "description": "Filter history by worker ID"},
				"timezone":      map[string]any{"type": "string", "description": "Explicit IANA timezone for daily history counts (defaults to UTC)"},
				"start_date":    map[string]any{"type": "string", "description": "Start date YYYY-MM-DD inclusive for history"},
				"end_date":      map[string]any{"type": "string", "description": "End date YYYY-MM-DD inclusive for history"},
				"cursor":        map[string]any{"type": "string", "description": "Opaque cursor for history pagination"},
				"limit":         map[string]any{"type": "integer", "description": "Max entries to return (default 50 for history, 100 for lists)"},
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
	case "help":
		response["instructions"] = "manage_environments unified reference:\n" +
			"1. Definition actions: list, get, create, update, delete, set_default_test, export, import.\n" +
			"2. Runtime instance inspection: list_deployments (reads deployments with active leases; no lazy reap), get_deployment (deployment_id required).\n" +
			"3. Supervised runtime mutations: ensure (environment_id, deliberate receipt execution), deploy, exec (deployment_id, command), start, stop, destroy, release (deployment_id or lease_id).\n" +
			"   Mutations return an immediate bounded operation receipt with operation_id and status within 2 seconds. Do not busy-poll; inspect receipts and use realtime updates.\n" +
			"4. Supervision & observability: summary (authoritative deployment and operation counts), history (cursor-paginated daily counts, timezone, date range), get_operation (operation_id), cancel_operation (operation_id)."
		response["available_actions"] = []string{
			"list", "get", "create", "update", "delete", "set_default_test", "export", "import",
			"list_deployments", "get_deployment", "ensure", "deploy", "exec", "start", "stop", "destroy", "release",
			"summary", "history", "get_operation", "cancel_operation", "help",
		}

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
		envID := firstNonEmptyString(strings.TrimSpace(asString(args["environment_id"])), strings.TrimSpace(asString(args["id"])))
		if envID == "" {
			return "", errors.New("environment_id is required for get action")
		}
		env, found, err := r.environmentsStore.Get(accountScopeID, workspaceID, envID)
		if err != nil {
			return "", fmt.Errorf("get environment %q: %w", envID, err)
		}
		if !found {
			return "", fmt.Errorf("environment %q not found", envID)
		}
		response["environment"] = env
		if r.workspaceSettings != nil {
			if ws, foundWS, _ := r.workspaceSettings.GetWorkspaceSettings(accountScopeID, workspaceID); foundWS {
				response["is_default_test"] = ws.DefaultTestEnvironmentID == envID
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
		envID := firstNonEmptyString(strings.TrimSpace(asString(args["environment_id"])), strings.TrimSpace(asString(args["id"])))
		if envID == "" {
			return "", errors.New("environment_id is required for update action")
		}
		current, found, err := r.environmentsStore.Get(accountScopeID, workspaceID, envID)
		if err != nil {
			return "", fmt.Errorf("get environment %q: %w", envID, err)
		}
		if !found {
			return "", fmt.Errorf("environment %q not found", envID)
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
		envID := firstNonEmptyString(strings.TrimSpace(asString(args["environment_id"])), strings.TrimSpace(asString(args["id"])))
		if envID == "" {
			return "", errors.New("environment_id is required for delete action")
		}
		deleted, err := r.environmentsStore.Delete(accountScopeID, workspaceID, envID)
		if err != nil {
			return "", fmt.Errorf("delete environment %q: %w", envID, err)
		}
		if !deleted {
			return "", fmt.Errorf("environment %q not found", envID)
		}
		response["id"] = envID
		response["environment_id"] = envID
		if r.workspaceSettings != nil {
			if ws, found, _ := r.workspaceSettings.GetWorkspaceSettings(accountScopeID, workspaceID); found && ws.DefaultTestEnvironmentID == envID {
				empty := ""
				_, _ = r.workspaceSettings.UpdateWorkspaceSettings(accountScopeID, workspaceID, &empty, nil)
			}
		}

	case "set_default_test":
		envID := firstNonEmptyString(strings.TrimSpace(asString(args["environment_id"])), strings.TrimSpace(asString(args["id"])))
		if envID != "" && envID != "none" {
			_, found, err := r.environmentsStore.Get(accountScopeID, workspaceID, envID)
			if err != nil {
				return "", fmt.Errorf("check environment %q: %w", envID, err)
			}
			if !found {
				return "", fmt.Errorf("environment %q not found", envID)
			}
		}
		if envID == "none" {
			envID = ""
		}
		if r.workspaceSettings == nil {
			return "", errors.New("workspace settings store is not configured")
		}
		_, err := r.workspaceSettings.UpdateWorkspaceSettings(accountScopeID, workspaceID, &envID, nil)
		if err != nil {
			return "", fmt.Errorf("set default test environment: %w", err)
		}
		response["default_test_environment_id"] = envID

	case "export":
		envID := firstNonEmptyString(strings.TrimSpace(asString(args["environment_id"])), strings.TrimSpace(asString(args["id"])))
		if envID == "" {
			return "", errors.New("environment_id is required for export action")
		}
		env, found, err := r.environmentsStore.Get(accountScopeID, workspaceID, envID)
		if err != nil {
			return "", fmt.Errorf("get environment %q: %w", envID, err)
		}
		if !found {
			return "", fmt.Errorf("environment %q not found", envID)
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

	// Runtime deployment inspection
	case "list_deployments":
		if r.deploymentManager == nil {
			return "", errors.New("manage_environments deployment manager is not configured")
		}
		limit := asInt(args["limit"], 100)
		envID := strings.TrimSpace(asString(args["environment_id"]))
		var deps []environments.Deployment
		if envID != "" {
			deps, err = r.deploymentManager.ListDeploymentsByEnvironment(accountScopeID, workspaceID, envID, limit)
		} else {
			deps, err = r.deploymentManager.ListDeployments(accountScopeID, workspaceID, limit)
		}
		if err != nil {
			return "", fmt.Errorf("list deployments: %w", err)
		}

		type depListItem struct {
			environments.Deployment
			ActiveLease *environments.DeploymentLease `json:"active_lease,omitempty"`
		}
		items := make([]depListItem, 0, len(deps))
		for _, dep := range deps {
			item := depListItem{Deployment: dep}
			if lease, hasLease, _ := r.deploymentManager.GetActiveLease(accountScopeID, workspaceID, dep.ID); hasLease && lease.Active {
				item.ActiveLease = &lease
			}
			items = append(items, item)
		}
		response["deployments"] = items
		response["count"] = len(items)

	case "get_deployment":
		if r.deploymentManager == nil {
			return "", errors.New("manage_environments deployment manager is not configured")
		}
		depID := strings.TrimSpace(asString(args["deployment_id"]))
		if depID == "" {
			return "", errors.New("deployment_id is required for get_deployment action")
		}
		dep, found, err := r.deploymentManager.GetDeployment(accountScopeID, workspaceID, depID)
		if err != nil {
			return "", fmt.Errorf("get deployment %q: %w", depID, err)
		}
		if !found {
			return "", fmt.Errorf("deployment %q not found", depID)
		}
		response["deployment"] = dep
		if lease, hasLease, _ := r.deploymentManager.GetActiveLease(accountScopeID, workspaceID, depID); hasLease {
			response["active_lease"] = lease
			response["lease"] = lease
		}
		if dep.IsUsable() {
			if access, _ := r.deploymentManager.ResolveAccess(ctx, accountScopeID, workspaceID, depID); access != nil {
				response["access"] = access
			}
		}

	case "summary":
		if r.deploymentManager == nil {
			return "", errors.New("manage_environments deployment manager is not configured")
		}
		summary, err := r.deploymentManager.Summary(ctx, accountScopeID, workspaceID)
		if err != nil {
			return "", fmt.Errorf("get environment summary: %w", err)
		}
		response["summary"] = summary

	case "history":
		if r.deploymentManager == nil {
			return "", errors.New("manage_environments deployment manager is not configured")
		}
		q := environments.OperationHistoryQuery{
			AccountScopeID: accountScopeID,
			WorkspaceID:    workspaceID,
			EnvironmentID:  strings.TrimSpace(asString(args["environment_id"])),
			DeploymentID:   strings.TrimSpace(asString(args["deployment_id"])),
			Actor:          strings.TrimSpace(asString(args["actor"])),
			SessionID:      strings.TrimSpace(asString(args["session_id"])),
			WorkerID:       strings.TrimSpace(asString(args["worker_id"])),
			Status:         environments.OperationStatus(strings.TrimSpace(asString(args["status"]))),
			Action:         firstNonEmptyString(strings.TrimSpace(asString(args["action_filter"])), strings.TrimSpace(asString(args["filter_action"]))),
			Timezone:       strings.TrimSpace(asString(args["timezone"])),
			StartDate:      strings.TrimSpace(asString(args["start_date"])),
			EndDate:        strings.TrimSpace(asString(args["end_date"])),
			Cursor:         strings.TrimSpace(asString(args["cursor"])),
			Limit:          asInt(args["limit"], 50),
		}
		page, err := r.deploymentManager.History(ctx, q)
		if err != nil {
			return "", fmt.Errorf("query operation history: %w", err)
		}
		response["history"] = page
		response["operations"] = page.Operations
		response["daily_totals"] = page.DailyTotals
		response["summary"] = page.Summary
		response["next_cursor"] = page.NextCursor
		response["has_more"] = page.HasMore

	case "get_operation":
		if r.deploymentManager == nil {
			return "", errors.New("manage_environments deployment manager is not configured")
		}
		opID := strings.TrimSpace(asString(args["operation_id"]))
		if opID == "" {
			return "", errors.New("operation_id is required for get_operation action")
		}
		op, found, err := r.deploymentManager.Get(ctx, accountScopeID, workspaceID, opID)
		if err != nil {
			return "", fmt.Errorf("get operation %q: %w", opID, err)
		}
		if !found {
			return "", fmt.Errorf("operation %q not found", opID)
		}
		response["operation"] = op
		response["operation_id"] = op.OperationID
		response["status"] = op.Status

	case "cancel_operation":
		if r.deploymentManager == nil {
			return "", errors.New("manage_environments deployment manager is not configured")
		}
		opID := strings.TrimSpace(asString(args["operation_id"]))
		if opID == "" {
			return "", errors.New("operation_id is required for cancel_operation action")
		}
		reason := strings.TrimSpace(asString(args["reason"]))
		if reason == "" {
			reason = "cancelled via manage_environments"
		}
		op, err := r.deploymentManager.Cancel(ctx, lifecycle.CancelOperationRequest{
			AccountScopeID: accountScopeID,
			WorkspaceID:    workspaceID,
			OperationID:    opID,
			Reason:         reason,
		})
		if err != nil {
			return "", fmt.Errorf("cancel operation %q: %w", opID, err)
		}
		response["operation"] = op
		response["operation_id"] = op.OperationID
		response["status"] = op.Status

	// Supervised runtime mutations (all wired to supervised admission via Submit)
	case "ensure", "deploy", "exec", "start", "stop", "destroy", "release":
		if r.deploymentManager == nil {
			return "", errors.New("manage_environments deployment manager is not configured")
		}

		// Trusted attribution: caller cannot forge consumer_id or request session identity
		callerActor := strings.TrimSpace(scope.Principal.UserID)
		if callerActor == "" {
			callerActor = "session-agent"
		}
		callerSessionID := strings.TrimSpace(scope.SessionID)

		subReq := lifecycle.SubmitOperationRequest{
			AccountScopeID: accountScopeID,
			WorkspaceID:    workspaceID,
			Action:         actionName,
			IdempotencyKey: strings.TrimSpace(asString(args["idempotency_key"])),
			Deadline:       int64(asInt(args["deadline"], 0)),
			Attribution: environments.OperationAttribution{
				Actor:     callerActor,
				SessionID: callerSessionID,
			},
		}
		if tMs := asInt(args["timeout_ms"], 0); tMs > 0 {
			subReq.Timeout = time.Duration(tMs) * time.Millisecond
		}

		switch actionName {
		case "ensure":
			envID := strings.TrimSpace(asString(args["environment_id"]))
			if envID == "" && r.workspaceSettings != nil {
				if ws, found, _ := r.workspaceSettings.GetWorkspaceSettings(accountScopeID, workspaceID); found {
					envID = strings.TrimSpace(ws.DefaultTestEnvironmentID)
				}
			}
			if envID == "" {
				return "", errors.New("environment_id is required for ensure (no workspace default test environment configured)")
			}
			subReq.EnvironmentID = envID
			subReq.ConnectionID = strings.TrimSpace(asString(args["connection_id"]))
			subReq.DeploymentName = strings.TrimSpace(asString(args["deployment_name"]))
			subReq.WorkspacePath = workspacePath
			subReq.EnvOverrides = asStringMap(args["env_overrides"])
			subReq.TTLMillis = int64(asInt(args["ttl_millis"], 0))
			subReq.ConsumerType = environments.ConsumerTypeSession
			subReq.ConsumerID = callerSessionID

		case "deploy":
			envID := strings.TrimSpace(asString(args["environment_id"]))
			if envID == "" {
				return "", errors.New("environment_id is required for deploy action")
			}
			subReq.EnvironmentID = envID
			subReq.ConnectionID = strings.TrimSpace(asString(args["connection_id"]))
			subReq.DeploymentName = strings.TrimSpace(asString(args["deployment_name"]))
			subReq.WorkspacePath = workspacePath
			subReq.EnvOverrides = asStringMap(args["env_overrides"])
			subReq.TTLMillis = int64(asInt(args["ttl_millis"], 0))
			subReq.ConsumerType = environments.ConsumerTypeSession
			subReq.ConsumerID = callerSessionID

		case "exec":
			depID := strings.TrimSpace(asString(args["deployment_id"]))
			if depID == "" {
				return "", errors.New("deployment_id is required for exec action")
			}
			cmd, err := parseCommandList(args["command"])
			if err != nil {
				return "", err
			}
			if len(cmd) == 0 {
				return "", errors.New("command is required for exec action")
			}
			subReq.DeploymentID = depID
			subReq.Command = cmd
			subReq.WorkingDir = strings.TrimSpace(asString(args["working_dir"]))
			subReq.Env = asStringMap(args["env"])
			subReq.MaxOutput = asInt(args["max_output"], 0)
			subReq.LeaseID = strings.TrimSpace(asString(args["lease_id"]))

		case "start", "stop", "destroy":
			depID := strings.TrimSpace(asString(args["deployment_id"]))
			if depID == "" {
				return "", fmt.Errorf("deployment_id is required for %s action", actionName)
			}
			subReq.DeploymentID = depID
			subReq.Reason = strings.TrimSpace(asString(args["reason"]))

		case "release":
			depID := strings.TrimSpace(asString(args["deployment_id"]))
			leaseID := strings.TrimSpace(asString(args["lease_id"]))
			if depID == "" && leaseID == "" {
				return "", errors.New("deployment_id or lease_id is required for release action")
			}
			subReq.DeploymentID = depID
			subReq.LeaseID = leaseID
			subReq.Reason = strings.TrimSpace(asString(args["reason"]))
		}

		op, err := r.deploymentManager.Submit(ctx, subReq)
		if err != nil {
			return "", fmt.Errorf("%s failed: %w", actionName, err)
		}
		response["operation"] = op
		response["operation_id"] = op.OperationID
		response["status"] = op.Status
		if op.DeploymentID != "" {
			response["deployment_id"] = op.DeploymentID
		}

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

	id := strings.TrimSpace(asString(args["environment_id"]))
	if id == "" {
		id = strings.TrimSpace(asString(args["id"]))
	}
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

	var prov environments.WorkspaceProvisioning
	if pRaw, ok := args["provisioning"].(map[string]any); ok && pRaw != nil {
		rawBytes, _ := json.Marshal(pRaw)
		_ = json.Unmarshal(rawBytes, &prov)
	}
	if prov.Strategy.Kind == "" {
		prov.Strategy.Kind = environments.SourceStrategyKindLocalMount
		prov.Strategy.LocalMount = &environments.LocalMountConfig{
			ContainerPath: "/app",
		}
	}

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
