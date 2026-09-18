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

func manageConnectionsDefinition() Definition {
	return Definition{
		Type:        "function",
		Name:        "manage_connections",
		Description: "List, get, create, update, delete, check, and inspect environment host connections (Docker/SSH).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":         map[string]any{"type": "string", "enum": []string{"list", "get", "create", "update", "delete", "check", "capabilities", "set_default"}, "description": "Action: list|get|create|update|delete|check|capabilities|set_default"},
				"workspace_path": map[string]any{"type": "string", "description": "Optional workspace path"},
				"id":             map[string]any{"type": "string", "description": "Connection ID"},
				"name":           map[string]any{"type": "string", "description": "Connection name"},
				"kind":           map[string]any{"type": "string", "enum": []string{"local_docker", "ssh"}, "description": "local_docker|ssh"},
				"host":           map[string]any{"type": "string", "description": "SSH host"},
				"user":           map[string]any{"type": "string", "description": "SSH user"},
				"port":           map[string]any{"type": "integer", "description": "SSH port (default 22)"},
				"is_default":     map[string]any{"type": "boolean", "description": "Set as workspace default"},
				"docker_host":    map[string]any{"type": "string", "description": "Local Docker host URL"},
				"capabilities":   map[string]any{"type": "object", "description": "Capability overrides. Call action='help' for schema."},
			},
			"required":             []string{"action"},
			"additionalProperties": true,
		},
	}
}

func (r *Runtime) executeManageConnections(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	if r == nil || r.connections == nil {
		return "", errors.New("manage_connections connection store is not configured")
	}

	actionName := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	if actionName == "" {
		actionName = "list"
	}

	accountScopeID, workspaceID, workspacePath, err := r.resolveWorkspaceScopeForEnvironments(scope, args, "manage_connections")
	if err != nil {
		return "", err
	}

	response := map[string]any{
		"tool":           "manage_connections",
		"action":         actionName,
		"status":         "ok",
		"workspace_id":   workspaceID,
		"workspace_path": workspacePath,
		"path_id":        toolPathID("manage_connections"),
	}

	switch actionName {
	case "list":
		limit := asInt(args["limit"], 100)
		conns, err := r.connections.List(accountScopeID, workspaceID, limit)
		if err != nil {
			return "", fmt.Errorf("list connections: %w", err)
		}
		response["connections"] = conns
		response["count"] = len(conns)
		if r.workspaceSettings != nil {
			if ws, found, _ := r.workspaceSettings.GetWorkspaceSettings(accountScopeID, workspaceID); found {
				response["default_connection_id"] = ws.DefaultConnectionID
			}
		}

	case "get":
		id := strings.TrimSpace(asString(args["id"]))
		if id == "" {
			return "", errors.New("id is required for get action")
		}
		conn, found, err := r.connections.Get(accountScopeID, workspaceID, id)
		if err != nil {
			return "", fmt.Errorf("get connection %q: %w", id, err)
		}
		if !found {
			return "", fmt.Errorf("connection %q not found", id)
		}
		response["connection"] = conn

	case "create":
		name := strings.TrimSpace(asString(args["name"]))
		if name == "" {
			return "", errors.New("name is required for create action")
		}
		kindStr := strings.ToLower(strings.TrimSpace(asString(args["kind"])))
		if kindStr == "" {
			return "", errors.New("kind is required for create action (local_docker|ssh)")
		}

		connKind := environments.ConnectionKind(kindStr)
		id := strings.TrimSpace(asString(args["id"]))
		if id == "" {
			id = "conn_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
		}

		conn := environments.Connection{
			ID:             id,
			AccountScopeID: accountScopeID,
			WorkspaceID:    workspaceID,
			Name:           name,
			Description:    strings.TrimSpace(asString(args["description"])),
			Kind:           connKind,
		}

		switch connKind {
		case environments.ConnectionKindLocalDocker:
			conn.LocalDocker = &environments.LocalDockerConfig{
				SocketPath: strings.TrimSpace(asString(args["socket_path"])),
				Host:       strings.TrimSpace(asString(args["docker_host"])),
			}
			conn.Capabilities = environments.ConnectionCapabilities{
				SupportsDocker:      true,
				SupportsDirectMount: true,
				SupportsPortForward: true,
			}

		case environments.ConnectionKindSSH:
			host := strings.TrimSpace(asString(args["host"]))
			user := strings.TrimSpace(asString(args["user"]))
			if host == "" || user == "" {
				return "", errors.New("host and user are required for ssh connection")
			}
			port := asInt(args["port"], 22)
			if port <= 0 {
				port = 22
			}
			conn.SSH = &environments.SSHConfig{
				Host:           host,
				User:           user,
				Port:           port,
				IdentityFile:   strings.TrimSpace(asString(args["ssh_key_path"])),
				KnownHostsFile: strings.TrimSpace(asString(args["known_hosts_file"])),
			}
			conn.Capabilities = environments.ConnectionCapabilities{
				SupportsSSH:         true,
				SupportsDocker:      true, // default assume Docker available on remote host; verified on check/capabilities
				SupportsPortForward: true,
			}

		default:
			return "", fmt.Errorf("unsupported connection kind: %q", kindStr)
		}

		// Apply capability overrides if supplied
		if capsRaw, ok := args["capabilities"].(map[string]any); ok && capsRaw != nil {
			if v, ok := capsRaw["supports_docker"].(bool); ok {
				conn.Capabilities.SupportsDocker = v
			}
			if v, ok := capsRaw["supports_ssh"].(bool); ok {
				conn.Capabilities.SupportsSSH = v
			}
			if v, ok := capsRaw["supports_direct_mount"].(bool); ok {
				conn.Capabilities.SupportsDirectMount = v
			}
			if v, ok := capsRaw["supports_port_forward"].(bool); ok {
				conn.Capabilities.SupportsPortForward = v
			}
		}

		if err := conn.Validate(); err != nil {
			return "", fmt.Errorf("invalid connection: %w", err)
		}

		saved, err := r.connections.Save(conn)
		if err != nil {
			return "", fmt.Errorf("save connection: %w", err)
		}
		if (asBool(args["is_default"]) || asBool(args["set_default"])) && r.workspaceSettings != nil {
			_, err := r.workspaceSettings.UpdateWorkspaceSettings(accountScopeID, workspaceID, nil, &saved.ID)
			if err != nil {
				return "", fmt.Errorf("set default connection: %w", err)
			}
			response["is_default"] = true
		}
		response["connection"] = saved

	case "update":
		id := strings.TrimSpace(asString(args["id"]))
		if id == "" {
			return "", errors.New("id is required for update action")
		}
		current, found, err := r.connections.Get(accountScopeID, workspaceID, id)
		if err != nil {
			return "", fmt.Errorf("get connection %q: %w", id, err)
		}
		if !found {
			return "", fmt.Errorf("connection %q not found", id)
		}

		if name := strings.TrimSpace(asString(args["name"])); name != "" {
			current.Name = name
		}
		if desc, ok := args["description"].(string); ok {
			current.Description = strings.TrimSpace(desc)
		}
		if current.Kind == environments.ConnectionKindLocalDocker && current.LocalDocker != nil {
			if sp, ok := args["socket_path"].(string); ok {
				current.LocalDocker.SocketPath = strings.TrimSpace(sp)
			}
			if dh, ok := args["docker_host"].(string); ok {
				current.LocalDocker.Host = strings.TrimSpace(dh)
			}
		}
		if current.Kind == environments.ConnectionKindSSH && current.SSH != nil {
			if host := strings.TrimSpace(asString(args["host"])); host != "" {
				current.SSH.Host = host
			}
			if user := strings.TrimSpace(asString(args["user"])); user != "" {
				current.SSH.User = user
			}
			if port := asInt(args["port"], 0); port > 0 {
				current.SSH.Port = port
			}
			if keyPath, ok := args["ssh_key_path"].(string); ok {
				current.SSH.IdentityFile = strings.TrimSpace(keyPath)
			}
			if khPath, ok := args["known_hosts_file"].(string); ok {
				current.SSH.KnownHostsFile = strings.TrimSpace(khPath)
			}
		}

		if capsRaw, ok := args["capabilities"].(map[string]any); ok && capsRaw != nil {
			if v, ok := capsRaw["supports_docker"].(bool); ok {
				current.Capabilities.SupportsDocker = v
			}
			if v, ok := capsRaw["supports_ssh"].(bool); ok {
				current.Capabilities.SupportsSSH = v
			}
			if v, ok := capsRaw["supports_direct_mount"].(bool); ok {
				current.Capabilities.SupportsDirectMount = v
			}
			if v, ok := capsRaw["supports_port_forward"].(bool); ok {
				current.Capabilities.SupportsPortForward = v
			}
		}

		if err := current.Validate(); err != nil {
			return "", fmt.Errorf("invalid connection update: %w", err)
		}

		saved, err := r.connections.Save(current)
		if err != nil {
			return "", fmt.Errorf("update connection: %w", err)
		}
		if (asBool(args["is_default"]) || asBool(args["set_default"])) && r.workspaceSettings != nil {
			_, err := r.workspaceSettings.UpdateWorkspaceSettings(accountScopeID, workspaceID, nil, &saved.ID)
			if err != nil {
				return "", fmt.Errorf("set default connection: %w", err)
			}
			response["is_default"] = true
		}
		response["connection"] = saved

	case "delete":
		id := strings.TrimSpace(asString(args["id"]))
		if id == "" {
			return "", errors.New("id is required for delete action")
		}
		deleted, err := r.connections.Delete(accountScopeID, workspaceID, id)
		if err != nil {
			return "", fmt.Errorf("delete connection %q: %w", id, err)
		}
		if !deleted {
			return "", fmt.Errorf("connection %q not found", id)
		}
		response["id"] = id

	case "check":
		id := strings.TrimSpace(asString(args["id"]))
		if id == "" {
			return "", errors.New("id is required for check action")
		}
		conn, found, err := r.connections.Get(accountScopeID, workspaceID, id)
		if err != nil {
			return "", fmt.Errorf("get connection %q: %w", id, err)
		}
		if !found {
			return "", fmt.Errorf("connection %q not found", id)
		}
		if r.providerRegistry == nil {
			return "", errors.New("provider registry is not configured")
		}
		prov, ok := r.providerRegistry.Get(conn.Kind)
		if !ok {
			return "", fmt.Errorf("no provider registered for connection kind %q", conn.Kind)
		}
		checkErr := prov.ValidateConnection(ctx, &conn)
		if checkErr != nil {
			response["status"] = "failed"
			response["reachable"] = false
			response["error"] = checkErr.Error()
		} else {
			response["reachable"] = true
		}
		response["id"] = id

	case "capabilities":
		id := strings.TrimSpace(asString(args["id"]))
		if id == "" {
			return "", errors.New("id is required for capabilities action")
		}
		conn, found, err := r.connections.Get(accountScopeID, workspaceID, id)
		if err != nil {
			return "", fmt.Errorf("get connection %q: %w", id, err)
		}
		if !found {
			return "", fmt.Errorf("connection %q not found", id)
		}
		if r.providerRegistry == nil {
			return "", errors.New("provider registry is not configured")
		}
		prov, ok := r.providerRegistry.Get(conn.Kind)
		if !ok {
			return "", fmt.Errorf("no provider registered for connection kind %q", conn.Kind)
		}
		caps, err := prov.Capabilities(ctx, &conn)
		if err != nil {
			return "", fmt.Errorf("query capabilities for connection %q: %w", id, err)
		}
		// Persist updated capabilities if changed
		conn.Capabilities = caps
		if _, saveErr := r.connections.Save(conn); saveErr != nil {
			// Retain error but continue
		}
		response["id"] = id
		response["capabilities"] = caps

	case "set_default":
		id := firstNonEmptyString(strings.TrimSpace(asString(args["id"])), strings.TrimSpace(asString(args["connection_id"])))
		if id != "" && id != "none" {
			_, found, err := r.connections.Get(accountScopeID, workspaceID, id)
			if err != nil {
				return "", fmt.Errorf("check connection %q: %w", id, err)
			}
			if !found {
				return "", fmt.Errorf("connection %q not found", id)
			}
		}
		if id == "none" {
			id = ""
		}
		if r.workspaceSettings == nil {
			return "", errors.New("workspace settings store is not configured")
		}
		_, err := r.workspaceSettings.UpdateWorkspaceSettings(accountScopeID, workspaceID, nil, &id)
		if err != nil {
			return "", fmt.Errorf("set default connection: %w", err)
		}
		response["default_connection_id"] = id

	default:
		return "", fmt.Errorf("unsupported manage_connections action %q", actionName)
	}

	encoded, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
