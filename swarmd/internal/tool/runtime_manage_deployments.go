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
	"swarm/packages/swarmd/internal/environments/provider"
)

func manageDeploymentsDefinition() Definition {
	return Definition{
		Type:        "function",
		Name:        "manage_deployments",
		Description: "Environment deployment manager (ensure, deploy, release, stop, destroy, check, access, exec). Call action='help' for schema.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type": "string",
					"enum": []string{"list", "get", "ensure", "deploy", "release", "stop", "destroy", "check", "access", "exec", "reap"},
				},
				"workspace_path":    map[string]any{"type": "string"},
				"id":                map[string]any{"type": "string", "description": "Deployment ID"},
				"deployment_id":     map[string]any{"type": "string", "description": "Alias for id"},
				"environment_id":    map[string]any{"type": "string", "description": "Environment ID"},
				"connection_id":     map[string]any{"type": "string", "description": "Connection ID override"},
				"name":              map[string]any{"type": "string", "description": "Deployment name"},
				"deployment_name":   map[string]any{"type": "string", "description": "Alias for name"},
				"consumer_type":     map[string]any{"type": "string", "enum": []string{"session", "test_run", "worker", "custom"}},
				"consumer_id":       map[string]any{"type": "string"},
				"consumer_metadata": map[string]any{"type": "object"},
				"lease_id":          map[string]any{"type": "string"},
				"reason":            map[string]any{"type": "string"},
				"ttl_millis":        map[string]any{"type": "integer"},
				"env_overrides":     map[string]any{"type": "object"},
				"command":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Command and args for exec"},
				"working_dir":       map[string]any{"type": "string"},
				"env":               map[string]any{"type": "object"},
				"timeout_ms":        map[string]any{"type": "integer"},
				"limit":             map[string]any{"type": "integer"},
			},
			"required":             []string{"action"},
			"additionalProperties": false,
		},
	}
}

func (r *Runtime) executeManageDeployments(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	if r == nil || r.deploymentManager == nil {
		return "", errors.New("manage_deployments deployment manager is not configured")
	}

	actionName := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	if actionName == "" {
		actionName = "list"
	}

	accountScopeID, workspaceID, workspacePath, err := r.resolveWorkspaceScopeForEnvironments(scope, args, "manage_deployments")
	if err != nil {
		return "", err
	}

	response := map[string]any{
		"tool":           "manage_deployments",
		"action":         actionName,
		"status":         "ok",
		"workspace_id":   workspaceID,
		"workspace_path": workspacePath,
		"path_id":        toolPathID("manage_deployments"),
	}

	depID := firstNonEmptyString(strings.TrimSpace(asString(args["id"])), strings.TrimSpace(asString(args["deployment_id"])))

	switch actionName {
	case "list":
		// Lazily reap expired leases before listing so view reflects active status
		if reaper, ok := r.deploymentManager.(interface {
			ReapExpired(ctx context.Context, accountScopeID, workspaceID string) ([]string, error)
		}); ok {
			_, _ = reaper.ReapExpired(ctx, accountScopeID, workspaceID)
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

	case "get":
		if depID == "" {
			// If lease_id provided, look up lease to get deployment_id
			if leaseID := strings.TrimSpace(asString(args["lease_id"])); leaseID != "" {
				// We can check if deploymentManager implements GetLease
				if lGetter, ok := r.deploymentManager.(interface {
					GetLease(accountScopeID, workspaceID, leaseID string) (environments.DeploymentLease, bool, error)
				}); ok {
					if l, foundL, _ := lGetter.GetLease(accountScopeID, workspaceID, leaseID); foundL {
						depID = l.DeploymentID
						response["lease"] = l
					}
				}
			}
		}
		if depID == "" {
			return "", errors.New("id or deployment_id is required for get action")
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
			response["lease"] = lease
		}

		if dep.IsUsable() {
			access, _ := r.deploymentManager.ResolveAccess(ctx, accountScopeID, workspaceID, depID)
			if access != nil {
				response["access"] = access
			}
		}

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

		consumerType := environments.ConsumerType(strings.TrimSpace(asString(args["consumer_type"])))
		if consumerType == "" {
			consumerType = environments.ConsumerTypeSession
		}

		consumerID := strings.TrimSpace(asString(args["consumer_id"]))
		if consumerID == "" {
			if scope.SessionID != "" {
				consumerID = scope.SessionID
			} else {
				consumerID = "consumer_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
			}
		}

		depName := firstNonEmptyString(strings.TrimSpace(asString(args["name"])), strings.TrimSpace(asString(args["deployment_name"])))
		ttlMillis := int64(asInt(args["ttl_millis"], 0))

		req := lifecycle.EnsureDeploymentRequest{
			AccountScopeID:   accountScopeID,
			WorkspaceID:      workspaceID,
			EnvironmentID:    envID,
			ConnectionID:     strings.TrimSpace(asString(args["connection_id"])),
			ConsumerType:     consumerType,
			ConsumerID:       consumerID,
			ConsumerMetadata: asStringMap(args["consumer_metadata"]),
			DeploymentName:   depName,
			WorkspacePath:    workspacePath,
			EnvOverrides:     asStringMap(args["env_overrides"]),
			TTLMillis:        ttlMillis,
		}

		result, err := r.deploymentManager.EnsureDeployment(ctx, req)
		if err != nil {
			return "", fmt.Errorf("ensure deployment: %w", err)
		}

		response["deployment"] = result.Deployment
		response["lease"] = result.Lease
		response["reused"] = result.Reused
		response["access"] = result.Access

	case "deploy":
		envID := strings.TrimSpace(asString(args["environment_id"]))
		if envID == "" {
			return "", errors.New("environment_id is required for deploy action")
		}

		depName := firstNonEmptyString(strings.TrimSpace(asString(args["name"])), strings.TrimSpace(asString(args["deployment_name"])))
		consumerType := environments.ConsumerType(strings.TrimSpace(asString(args["consumer_type"])))
		consumerID := strings.TrimSpace(asString(args["consumer_id"]))
		ttlMillis := int64(asInt(args["ttl_millis"], 0))

		req := lifecycle.DeployDeploymentRequest{
			AccountScopeID:   accountScopeID,
			WorkspaceID:      workspaceID,
			EnvironmentID:    envID,
			ConnectionID:     strings.TrimSpace(asString(args["connection_id"])),
			DeploymentName:   depName,
			WorkspacePath:    workspacePath,
			EnvOverrides:     asStringMap(args["env_overrides"]),
			ConsumerType:     consumerType,
			ConsumerID:       consumerID,
			ConsumerMetadata: asStringMap(args["consumer_metadata"]),
			TTLMillis:        ttlMillis,
		}

		result, err := r.deploymentManager.DeployDeployment(ctx, req)
		if err != nil {
			return "", fmt.Errorf("deploy deployment: %w", err)
		}

		response["deployment"] = result.Deployment
		if result.Lease != nil {
			response["lease"] = result.Lease
		}
		response["access"] = result.Access

	case "release":
		leaseID := strings.TrimSpace(asString(args["lease_id"]))
		if leaseID == "" && depID != "" {
			// Find active lease for the deployment
			activeLease, hasActive, err := r.deploymentManager.GetActiveLease(accountScopeID, workspaceID, depID)
			if err == nil && hasActive && activeLease.Active {
				leaseID = activeLease.ID
			}
		}
		if leaseID == "" {
			return "", errors.New("lease_id or active deployment_id is required for release action")
		}

		reason := strings.TrimSpace(asString(args["reason"]))
		result, err := r.deploymentManager.ReleaseDeployment(ctx, lifecycle.ReleaseDeploymentRequest{
			AccountScopeID: accountScopeID,
			WorkspaceID:    workspaceID,
			LeaseID:        leaseID,
			Reason:         reason,
		})
		if err != nil {
			return "", fmt.Errorf("release deployment: %w", err)
		}

		response["lease"] = result.Lease
		response["deployment"] = result.Deployment
		response["release_behavior"] = result.ReleaseBehavior
		response["action_taken"] = result.ActionTaken

	case "stop":
		if depID == "" {
			return "", errors.New("id or deployment_id is required for stop action")
		}
		if err := r.deploymentManager.StopDeployment(ctx, accountScopeID, workspaceID, depID); err != nil {
			return "", fmt.Errorf("stop deployment %q: %w", depID, err)
		}
		response["deployment_id"] = depID

	case "destroy":
		if depID == "" {
			return "", errors.New("id or deployment_id is required for destroy action")
		}
		reason := strings.TrimSpace(asString(args["reason"]))
		if err := r.deploymentManager.DestroyDeployment(ctx, lifecycle.DestroyDeploymentRequest{
			AccountScopeID: accountScopeID,
			WorkspaceID:    workspaceID,
			DeploymentID:   depID,
			Reason:         reason,
		}); err != nil {
			return "", fmt.Errorf("destroy deployment %q: %w", depID, err)
		}
		response["deployment_id"] = depID

	case "check":
		if depID == "" {
			return "", errors.New("id or deployment_id is required for check action")
		}
		dep, err := r.deploymentManager.InspectDeployment(ctx, accountScopeID, workspaceID, depID)
		if err != nil {
			return "", fmt.Errorf("check deployment %q: %w", depID, err)
		}
		response["deployment"] = dep
		response["status_detail"] = dep.Status
		response["health"] = dep.Health

	case "access":
		if depID == "" {
			return "", errors.New("id or deployment_id is required for access action")
		}
		access, err := r.deploymentManager.ResolveAccess(ctx, accountScopeID, workspaceID, depID)
		if err != nil {
			return "", fmt.Errorf("resolve access for deployment %q: %w", depID, err)
		}
		response["deployment_id"] = depID
		response["access"] = access

	case "exec":
		if depID == "" {
			return "", errors.New("id or deployment_id is required for exec action")
		}
		cmd, err := parseCommandList(args["command"])
		if err != nil {
			return "", err
		}
		if len(cmd) == 0 {
			return "", errors.New("command is required for exec action")
		}

		timeoutMs := asInt(args["timeout_ms"], 30000)
		if timeoutMs <= 0 {
			timeoutMs = 30000
		}

		execReq := provider.ExecRequest{
			Command:    cmd,
			WorkingDir: strings.TrimSpace(asString(args["working_dir"])),
			Env:        asStringMap(args["env"]),
			Timeout:    time.Duration(timeoutMs) * time.Millisecond,
		}

		res, err := r.deploymentManager.Exec(ctx, accountScopeID, workspaceID, depID, execReq)
		if err != nil {
			return "", fmt.Errorf("exec in deployment %q: %w", depID, err)
		}

		response["deployment_id"] = depID
		response["exit_code"] = res.ExitCode
		response["stdout"] = res.Stdout
		response["stderr"] = res.Stderr
		response["success"] = res.Success()

	case "reap":
		if reaper, ok := r.deploymentManager.(interface {
			ReapExpired(ctx context.Context, accountScopeID, workspaceID string) ([]string, error)
		}); ok {
			reaped, err := reaper.ReapExpired(ctx, accountScopeID, workspaceID)
			if err != nil {
				return "", fmt.Errorf("reap expired deployments: %w", err)
			}
			response["reaped"] = reaped
			response["reaped_count"] = len(reaped)
		} else {
			response["reaped"] = []string{}
			response["reaped_count"] = 0
		}

	default:
		return "", fmt.Errorf("unsupported manage_deployments action %q", actionName)
	}

	encoded, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func parseCommandList(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	switch typed := raw.(type) {
	case []string:
		return typed, nil
	case []any:
		res := make([]string, 0, len(typed))
		for _, item := range typed {
			if s, ok := item.(string); ok {
				res = append(res, s)
			} else {
				return nil, fmt.Errorf("command array item is not a string: %v", item)
			}
		}
		return res, nil
	case string:
		// Single string command
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return nil, nil
		}
		return strings.Fields(trimmed), nil
	default:
		return nil, fmt.Errorf("unsupported command format: %T", raw)
	}
}

func asStringMap(raw any) map[string]string {
	if raw == nil {
		return nil
	}
	switch typed := raw.(type) {
	case map[string]string:
		return typed
	case map[string]any:
		res := make(map[string]string, len(typed))
		for k, v := range typed {
			if s, ok := v.(string); ok {
				res[k] = s
			} else {
				res[k] = fmt.Sprint(v)
			}
		}
		return res
	default:
		return nil
	}
}
