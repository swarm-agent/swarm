package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
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
				"project_result": projectResultDefinition(),
				"action": map[string]any{
					"type": "string",
					"enum": []string{
						"list", "get", "create", "update", "delete", "set_default_test", "export", "import",
						"list_deployments", "get_deployment", "ensure", "deploy", "exec", "start", "stop", "destroy", "release",
						"summary", "history", "get_operation", "cancel", "cancel_operation", "help",
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
				"container":               environmentValueSchema(reflect.TypeOf(environments.ContainerDefinition{})),
				"provisioning":            environmentValueSchema(reflect.TypeOf(environments.WorkspaceProvisioning{})),
				"deployment_policy":       environmentValueSchema(reflect.TypeOf(environments.DeploymentPolicy{})),
				"health_check":            environmentValueSchema(reflect.TypeOf(environments.HealthCheck{})),
				"resources":               environmentValueSchema(reflect.TypeOf(environments.ResourceRequirements{})),
				"labels":                  environmentValueSchema(reflect.TypeOf(map[string]string{})),
				"ports":                   environmentValueSchema(reflect.TypeOf([]environments.PortMapping{})),
				"max_instances":           map[string]any{"type": "integer"},
				"release_behavior":        map[string]any{"type": "string", "enum": []string{"none", "restart", "recreate"}},
				"reuse":                   map[string]any{"type": "boolean"},
				"set_default_test":        map[string]any{"type": "boolean"},
				"json":                    map[string]any{"type": "string", "description": "JSON payload for import"},
				"environment":             environmentValueSchema(reflect.TypeOf(environments.Environment{})),
				// Runtime execution and supervised operation fields
				"command":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Command and arguments for exec action"},
				"working_dir":     map[string]any{"type": "string", "description": "Working directory inside container for exec"},
				"env":             map[string]any{"type": "object", "description": "Environment variables for exec"},
				"env_overrides":   map[string]any{"type": "object", "description": "Environment variable overrides for deploy/ensure"},
				"timeout_ms":      map[string]any{"type": "integer", "description": "Timeout in milliseconds for command or operation"},
				"deadline":        map[string]any{"type": "integer", "description": "Finite epoch millisecond deadline for operation"},
				"idempotency_key": map[string]any{"type": "string", "description": "Client idempotency key for operation deduplication"},
				"ttl_millis":      map[string]any{"type": "integer", "description": "Lease TTL in milliseconds for deploy/ensure (0 = default 1 hour)"},
				"consumer_type":   map[string]any{"type": "string", "enum": []string{"session", "test_run", "worker", "custom"}},
				"consumer_id":     map[string]any{"type": "string"},
				"lease_id":        map[string]any{"type": "string", "description": "Lease ID for release action"},
				"reason":          map[string]any{"type": "string", "description": "Reason for release, destroy, or cancel_operation"},
				"max_output":      map[string]any{"type": "integer", "description": "Maximum stdout/stderr bytes for exec"},
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

func (r *Runtime) executeManageEnvironments(ctx context.Context, scope WorkspaceScope, callID string, args map[string]any) (string, error) {
	if r == nil || r.environmentsStore == nil {
		return "", errors.New("manage_environments environment store is not configured")
	}

	actionName := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	if actionName == "" {
		actionName = "list"
	}

	if actionName == "create" || actionName == "update" || actionName == "import" {
		if err := validateEnvironmentDefinitionArgs(actionName, args); err != nil {
			return "", err
		}
	}

	var accountScopeID, workspaceID, workspacePath string
	var err error
	var projectTarget *ProjectInspectionTarget
	if reference, ok := args["project_result"].(map[string]any); ok {
		switch actionName {
		case "help", "list", "get", "ensure", "deploy", "exec", "release", "get_operation", "get_deployment":
		default:
			return "", errors.New("project_result supports inspection and leased validation only")
		}
		if asString(reference["task_id"]) == "" || asString(reference["head_commit"]) == "" {
			return "", errors.New("project_result requires the exact committed task reference returned by inspect_files")
		}
		resolutionReference := reference
		if actionName == "release" {
			// Cleanup must remain possible after validation generates files or the
			// task advances. Release still uses canonical account and workspace
			// parent-owned lease admission; it cannot execute against a tree.
			resolutionReference = map[string]any{"project_id": reference["project_id"], "workspace_id": reference["workspace_id"], "workspace_path": reference["workspace_path"], "workspace_generation": reference["workspace_generation"]}
		}
		target, resolveErr := r.resolveProjectInspection(ctx, scope, resolutionReference)
		if resolveErr != nil {
			return "", resolveErr
		}
		if asString(args["workspace_path"]) != "" || asString(args["workspace_id"]) != "" {
			return "", errors.New("project_result cannot be combined with workspace overrides")
		}
		projectTarget = &target
		accountScopeID, workspaceID, workspacePath = scope.Principal.AccountScopeID, target.Reference.WorkspaceID, target.Root
	} else {
		accountScopeID, workspaceID, workspacePath, err = r.resolveWorkspaceScopeForEnvironments(scope, args, "manage_environments")
		if err != nil {
			return "", err
		}
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
		response["definition_schema"] = manageEnvironmentsDefinition().Parameters
		response["definition_help"] = "Create accepts top-level definition fields or one environment object; import accepts exactly one environment object or json string containing an exported definition. Nested fields are JSON objects, not JSON-encoded strings. Unknown fields, nulls and wrong types are rejected. Exported account_scope_id/workspace_id are rebound to authorized caller scope, never trusted. Update accepts top-level fields only: container and deployment_policy merge supplied fields; provisioning, health_check, resources and labels replace the supplied section. image/ports and max_instances/release_behavior/reuse are aliases and cannot accompany their canonical section. Omitted create provisioning defaults to local_mount at /app; explicit provisioning requires a valid strategy. Container keys: image, command, args, env_vars, exposed_ports, privileged, user, working_dir, setup_commands. No arbitrary runtime flags or systemd options are supported. Strategy availability is provider-dependent; schema describes stored definitions, not a guarantee of provider support."
		response["available_actions"] = []string{
			"list", "get", "create", "update", "delete", "set_default_test", "export", "import",
			"list_deployments", "get_deployment", "ensure", "deploy", "exec", "start", "stop", "destroy", "release",
			"summary", "history", "get_operation", "cancel", "cancel_operation", "help",
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
		importedEnv, err := decodeEnvironmentImport(args)
		if err != nil {
			return "", err
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

	case "cancel", "cancel_operation":
		if r.deploymentManager == nil {
			return "", errors.New("manage_environments deployment manager is not configured")
		}
		opID := firstNonEmptyString(strings.TrimSpace(asString(args["operation_id"])), strings.TrimSpace(asString(args["id"])))
		if opID == "" {
			return "", errors.New("operation_id is required for cancel action")
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
		var callerRunID string
		var callerWorkerID string
		if runCtx, ok := ctx.Value(artifactRunContextKey{}).(ArtifactRunContext); ok {
			callerRunID = strings.TrimSpace(runCtx.RunID)
			callerWorkerID = strings.TrimSpace(runCtx.ChildSessionID)
			if callerWorkerID == "" {
				callerWorkerID = strings.TrimSpace(runCtx.ProgramJobID)
			}
		}

		idempotencyKey := strings.TrimSpace(asString(args["idempotency_key"]))
		if idempotencyKey == "" && strings.TrimSpace(callID) != "" {
			idempotencyKey = "tool:" + strings.TrimSpace(callID)
		}

		subReq := lifecycle.SubmitOperationRequest{
			AccountScopeID: accountScopeID,
			WorkspaceID:    workspaceID,
			Action:         actionName,
			IdempotencyKey: idempotencyKey,
			Deadline:       int64(asInt(args["deadline"], 0)),
			Attribution: environments.OperationAttribution{
				Actor:     callerActor,
				SessionID: callerSessionID,
				RunID:     callerRunID,
				WorkerID:  callerWorkerID,
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
			if projectTarget != nil {
				env, found, err := r.environmentsStore.Get(accountScopeID, workspaceID, envID)
				if err != nil || !found || env.Provisioning.Strategy.Kind != environments.SourceStrategyKindLocalMount || env.Provisioning.Strategy.LocalMount == nil || (env.Provisioning.Strategy.LocalMount.HostPath != "" && env.Provisioning.Strategy.LocalMount.HostPath != projectTarget.Root) {
					return "", errors.New("project result validation requires a local_mount environment with a dynamic host path or this exact result root; configured source provisioning cannot prove the selected commit")
				}
				resolver, ok := r.deploymentManager.(interface {
					ResolveConnection(context.Context, string, string, string, *environments.Environment) (*environments.Connection, error)
				})
				if !ok {
					return "", errors.New("validation connection resolver unavailable")
				}
				conn, err := resolver.ResolveConnection(ctx, accountScopeID, workspaceID, asString(args["connection_id"]), &env)
				if err != nil || conn == nil || string(conn.Kind) != "local_docker" {
					return "", errors.New("project result validation requires a local Docker connection; remote source identity is not proven")
				}
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
			if projectTarget != nil {
				env, found, err := r.environmentsStore.Get(accountScopeID, workspaceID, envID)
				if err != nil || !found || env.Provisioning.Strategy.Kind != environments.SourceStrategyKindLocalMount || env.Provisioning.Strategy.LocalMount == nil || (env.Provisioning.Strategy.LocalMount.HostPath != "" && env.Provisioning.Strategy.LocalMount.HostPath != projectTarget.Root) {
					return "", errors.New("project result validation requires a local_mount environment with a dynamic host path or this exact result root; configured source provisioning cannot prove the selected commit")
				}
				resolver, ok := r.deploymentManager.(interface {
					ResolveConnection(context.Context, string, string, string, *environments.Environment) (*environments.Connection, error)
				})
				if !ok {
					return "", errors.New("validation connection resolver unavailable")
				}
				conn, err := resolver.ResolveConnection(ctx, accountScopeID, workspaceID, asString(args["connection_id"]), &env)
				if err != nil || conn == nil || string(conn.Kind) != "local_docker" {
					return "", errors.New("project result validation requires a local Docker connection; remote source identity is not proven")
				}
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
			if projectTarget != nil {
				dep, found, err := r.deploymentManager.GetDeployment(accountScopeID, workspaceID, depID)
				if err != nil || !found || dep.WorkspacePath != projectTarget.Root {
					return "", errors.New("validation deployment does not mount the selected isolated result; ensure a deployment with the exact project_result first")
				}
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
	var env environments.Environment
	if value, supplied := args["environment"]; supplied {
		if err := rejectEnvironmentFieldMix(args); err != nil {
			return env, err
		}
		if err := decodeEnvironmentValue("environment", value, &env); err != nil {
			return environments.Environment{}, err
		}
	} else {
		env.Mode = environments.EnvironmentModeDeployable
		env.Role = environments.EnvironmentRoleTesting
		env.DeploymentPolicy.MaxInstances = 1
		env.DeploymentPolicy.ReleaseBehavior = environments.ReleaseBehaviorNone
		if _, supplied := args["provisioning"]; !supplied {
			env.Provisioning.Strategy = environments.SourceStrategy{
				Kind:       environments.SourceStrategyKindLocalMount,
				LocalMount: &environments.LocalMountConfig{ContainerPath: "/app"},
			}
		}
		var err error
		env, err = applyEnvironmentFields(env, args)
		if err != nil {
			return environments.Environment{}, err
		}
		env.ID = firstNonEmptyString(asString(args["environment_id"]), asString(args["id"]))
	}
	env.AccountScopeID = accountScopeID
	env.WorkspaceID = workspaceID
	if strings.TrimSpace(env.ID) == "" {
		env.ID = "env_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	}
	return env, nil
}

func applyEnvironmentUpdates(current environments.Environment, args map[string]any) (environments.Environment, error) {
	if _, supplied := args["environment"]; supplied {
		return environments.Environment{}, errors.New("environment: update requires top-level definition fields, not an environment object")
	}
	// Decoding and validation must never mutate maps, slices or pointers owned by
	// the caller/store, even when a later field fails.
	return applyEnvironmentFields(*current.Clone(), args)
}

func applyEnvironmentFields(env environments.Environment, args map[string]any) (environments.Environment, error) {
	for section, aliases := range map[string][]string{
		"container":         {"image", "ports"},
		"deployment_policy": {"max_instances", "release_behavior", "reuse"},
	} {
		if _, supplied := args[section]; supplied {
			for _, alias := range aliases {
				if _, supplied := args[alias]; supplied {
					return environments.Environment{}, fmt.Errorf("%s: cannot combine with %s; use canonical nested fields", alias, section)
				}
			}
		}
	}
	fields := []struct {
		name string
		dest any
	}{
		{"name", &env.Name}, {"description", &env.Description},
		{"mode", &env.Mode}, {"role", &env.Role},
		{"preferred_connection_id", &env.PreferredConnectionID},
		{"container", &env.Container}, {"image", &env.Container.Image},
		{"ports", &env.Container.ExposedPorts},
		{"deployment_policy", &env.DeploymentPolicy},
		{"max_instances", &env.DeploymentPolicy.MaxInstances},
		{"release_behavior", &env.DeploymentPolicy.ReleaseBehavior},
		{"reuse", &env.DeploymentPolicy.Reuse},
	}
	for _, field := range fields {
		if value, supplied := args[field.name]; supplied {
			if err := decodeEnvironmentValue(field.name, value, field.dest); err != nil {
				return environments.Environment{}, err
			}
		}
	}
	// These sections replace rather than merge, avoiding stale strategy variants
	// when switching source kinds. Explicit empty provisioning is not omission.
	if value, supplied := args["provisioning"]; supplied {
		var provisioning environments.WorkspaceProvisioning
		if err := decodeEnvironmentValue("provisioning", value, &provisioning); err != nil {
			return environments.Environment{}, err
		}
		if err := provisioning.Validate(); err != nil {
			return environments.Environment{}, fmt.Errorf("provisioning.strategy: %w", err)
		}
		env.Provisioning = provisioning
	}
	if value, supplied := args["health_check"]; supplied {
		var health environments.HealthCheck
		if err := decodeEnvironmentValue("health_check", value, &health); err != nil {
			return environments.Environment{}, err
		}
		env.HealthCheck = &health
	}
	if value, supplied := args["resources"]; supplied {
		var resources environments.ResourceRequirements
		if err := decodeEnvironmentValue("resources", value, &resources); err != nil {
			return environments.Environment{}, err
		}
		env.Resources = &resources
	}
	if value, supplied := args["labels"]; supplied {
		var labels map[string]string
		if err := decodeEnvironmentValue("labels", value, &labels); err != nil {
			return environments.Environment{}, err
		}
		env.Labels = labels
	}
	return env, nil
}

func rejectEnvironmentFieldMix(args map[string]any) error {
	for _, field := range []string{"name", "description", "mode", "role", "preferred_connection_id", "container", "image", "ports", "provisioning", "deployment_policy", "max_instances", "release_behavior", "reuse", "health_check", "resources", "labels", "id", "environment_id"} {
		if _, supplied := args[field]; supplied {
			return fmt.Errorf("%s: cannot combine definition fields with environment or json; supply one definition", field)
		}
	}
	return nil
}

func decodeEnvironmentImport(args map[string]any) (environments.Environment, error) {
	var env environments.Environment
	if err := rejectEnvironmentFieldMix(args); err != nil {
		return env, err
	}
	value, objectSupplied := args["environment"]
	raw, jsonSupplied := args["json"]
	if objectSupplied == jsonSupplied {
		return env, errors.New("import requires exactly one of json string or environment object")
	}
	path := "environment"
	if jsonSupplied {
		text, ok := raw.(string)
		if !ok {
			return env, errors.New("json: expected a JSON string containing an environment object")
		}
		// Unmarshal rejects malformed JSON and trailing values before any writes.
		var document json.RawMessage
		if err := json.Unmarshal([]byte(text), &document); err != nil {
			return env, fmt.Errorf("json: invalid environment JSON: %w", err)
		}
		value = document
		path = "json.environment"
	}
	if err := decodeEnvironmentValue(path, value, &env); err != nil {
		return environments.Environment{}, err
	}
	return env, nil
}

// The schema and strict decoder share the canonical JSON tags, so fields cannot
// silently disappear when the domain definition grows. This is deliberately
// scoped to environment definition DTOs (no custom JSON marshalers).
func environmentValueSchema(typ reflect.Type) map[string]any {
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		properties := map[string]any{}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				properties[name] = environmentValueSchema(field.Type)
			}
		}
		return map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": environmentValueSchema(typ.Elem())}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": environmentValueSchema(typ.Elem())}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int64:
		return map[string]any{"type": "integer"}
	default:
		return map[string]any{"type": "string"}
	}
}

func decodeEnvironmentValue(path string, value any, dest any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("%s: invalid JSON value: %w", path, err)
	}
	return decodeEnvironmentJSON(path, raw, reflect.ValueOf(dest).Elem())
}

func decodeEnvironmentJSON(path string, raw json.RawMessage, dest reflect.Value) error {
	if string(raw) == "null" {
		return fmt.Errorf("%s: null is not supported; omit the field or supply its documented value", path)
	}
	if dest.Kind() == reflect.Pointer {
		if dest.IsNil() {
			dest.Set(reflect.New(dest.Type().Elem()))
		}
		return decodeEnvironmentJSON(path, raw, dest.Elem())
	}
	switch dest.Kind() {
	case reflect.Struct, reflect.Map:
		object, err := environmentJSONObject(path, raw)
		if err != nil {
			return err
		}
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if dest.Kind() == reflect.Map {
			if dest.IsNil() {
				dest.Set(reflect.MakeMap(dest.Type()))
			}
			for _, key := range keys {
				item := reflect.New(dest.Type().Elem()).Elem()
				if err := decodeEnvironmentJSON(path+"."+key, object[key], item); err != nil {
					return err
				}
				dest.SetMapIndex(reflect.ValueOf(key), item)
			}
			return nil
		}
		fields := map[string]int{}
		for i := 0; i < dest.NumField(); i++ {
			name := strings.Split(dest.Type().Field(i).Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				fields[name] = i
			}
		}
		for _, key := range keys {
			index, ok := fields[key]
			if !ok {
				return fmt.Errorf("%s.%s: unknown field; use the canonical fields from action=help", path, key)
			}
			if err := decodeEnvironmentJSON(path+"."+key, object[key], dest.Field(index)); err != nil {
				return err
			}
		}
		return nil
	case reflect.Slice:
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return fmt.Errorf("%s: expected array: %w", path, err)
		}
		result := reflect.MakeSlice(dest.Type(), len(items), len(items))
		for i, item := range items {
			if err := decodeEnvironmentJSON(fmt.Sprintf("%s[%d]", path, i), item, result.Index(i)); err != nil {
				return err
			}
		}
		dest.Set(result)
		return nil
	default:
		if err := json.Unmarshal(raw, dest.Addr().Interface()); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		return nil
	}
}

// Definition actions reject unrelated runtime arguments rather than reporting a
// successful save after dropping them. Scope arguments still use canonical
// workspace authorization; full definition scope metadata is never trusted.
func validateEnvironmentDefinitionArgs(action string, args map[string]any) error {
	allowed := map[string]bool{"action": true, "workspace_path": true, "workspace_id": true}
	if action == "import" {
		allowed["json"], allowed["environment"] = true, true
	} else {
		for _, key := range []string{"environment_id", "id", "name", "description", "mode", "role", "preferred_connection_id", "image", "container", "ports", "provisioning", "deployment_policy", "max_instances", "release_behavior", "reuse", "health_check", "resources", "labels", "set_default_test"} {
			allowed[key] = true
		}
		if action == "create" {
			allowed["environment"] = true
		}
	}
	for key := range args {
		if !allowed[key] {
			return fmt.Errorf("%s: unsupported field for %s; use action=help for definition fields", key, action)
		}
	}
	for _, key := range []string{"workspace_path", "workspace_id", "environment_id", "id"} {
		if value, supplied := args[key]; supplied {
			var text string
			if err := decodeEnvironmentValue(key, value, &text); err != nil {
				return err
			}
		}
	}
	if value, supplied := args["set_default_test"]; supplied {
		var flag bool
		if err := decodeEnvironmentValue("set_default_test", value, &flag); err != nil {
			return err
		}
	}
	return nil
}

// Preserve duplicate-key evidence in imported JSON instead of accepting the
// last value and silently discarding an earlier setting.
func environmentJSONObject(path string, raw json.RawMessage) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("%s: expected object", path)
	}
	object := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("%s: invalid object: %w", path, err)
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("%s: expected field name", path)
		}
		if _, exists := object[key]; exists {
			return nil, fmt.Errorf("%s.%s: duplicate field", path, key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("%s.%s: invalid JSON: %w", path, key, err)
		}
		object[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, fmt.Errorf("%s: invalid object: %w", path, err)
	}
	return object, nil
}
