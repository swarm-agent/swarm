package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/lifecycle"
	"swarm/packages/swarmd/internal/environments/provider"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type manageConnectionStore interface {
	Get(accountScopeID, workspaceID, connectionID string) (environments.Connection, bool, error)
	List(accountScopeID, workspaceID string, limit int) ([]environments.Connection, error)
	Save(conn environments.Connection) (environments.Connection, error)
	Delete(accountScopeID, workspaceID, connectionID string) (bool, error)
}

type manageEnvironmentStore interface {
	Get(accountScopeID, workspaceID, environmentID string) (environments.Environment, bool, error)
	List(accountScopeID, workspaceID string, limit int) ([]environments.Environment, error)
	Save(env environments.Environment) (environments.Environment, error)
	Delete(accountScopeID, workspaceID, environmentID string) (bool, error)
}

type manageDeploymentLifecycleService interface {
	EnsureDeployment(ctx context.Context, req lifecycle.EnsureDeploymentRequest) (*lifecycle.EnsureDeploymentResult, error)
	DeployDeployment(ctx context.Context, req lifecycle.DeployDeploymentRequest) (*lifecycle.DeployDeploymentResult, error)
	ReleaseDeployment(ctx context.Context, req lifecycle.ReleaseDeploymentRequest) (*lifecycle.ReleaseDeploymentResult, error)
	DestroyDeployment(ctx context.Context, req lifecycle.DestroyDeploymentRequest) error
	StopDeployment(ctx context.Context, accountScopeID, workspaceID, deploymentID string) error
	StartDeployment(ctx context.Context, accountScopeID, workspaceID, deploymentID string) error
	InspectDeployment(ctx context.Context, accountScopeID, workspaceID, deploymentID string) (*environments.Deployment, error)
	ResolveAccess(ctx context.Context, accountScopeID, workspaceID, deploymentID string) (*provider.DeploymentAccess, error)
	Exec(ctx context.Context, accountScopeID, workspaceID, deploymentID string, req provider.ExecRequest) (*provider.ExecResult, error)
	GetDeployment(accountScopeID, workspaceID, deploymentID string) (environments.Deployment, bool, error)
	ListDeployments(accountScopeID, workspaceID string, limit int) ([]environments.Deployment, error)
	ListDeploymentsByEnvironment(accountScopeID, workspaceID, environmentID string, limit int) ([]environments.Deployment, error)
	GetActiveLease(accountScopeID, workspaceID, deploymentID string) (environments.DeploymentLease, bool, error)
}

type manageWorkspaceSettingsStore interface {
	GetWorkspaceSettings(accountScopeID, workspaceID string) (environments.WorkspaceSettings, bool, error)
	UpdateWorkspaceSettings(accountScopeID, workspaceID string, defaultTestEnvironmentID, defaultConnectionID *string) (pebblestore.WorkspaceEntry, error)
}

type manageProviderRegistry interface {
	Get(kind environments.ConnectionKind) (provider.DeploymentProvider, bool)
}

func (s *Server) SetEnvironmentServices(
	connections manageConnectionStore,
	environments manageEnvironmentStore,
	deployments manageDeploymentLifecycleService,
	workspaceEnvSettings manageWorkspaceSettingsStore,
	envProviders manageProviderRegistry,
) {
	s.connections = connections
	s.environments = environments
	s.deployments = deployments
	s.workspaceEnvSettings = workspaceEnvSettings
	s.envProviders = envProviders
}

func (s *Server) resolveEnvironmentScope(r *http.Request, rawWorkspaceID, rawPath string) (accountScopeID, workspaceID, workspacePath string, err error) {
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() || strings.TrimSpace(principal.AccountScopeID) == "" {
		return "", "", "", identity.ErrPrincipalRequired
	}
	accountScopeID = strings.TrimSpace(principal.AccountScopeID)

	rawWorkspaceID = strings.TrimSpace(rawWorkspaceID)
	rawPath = strings.TrimSpace(rawPath)

	if rawWorkspaceID != "" {
		workspaceID = rawWorkspaceID
		if rawPath != "" {
			workspacePath = rawPath
		}
		return accountScopeID, workspaceID, workspacePath, nil
	}

	if s.workspace != nil && rawPath != "" {
		scope, scopeErr := s.workspace.ScopeForPathForPrincipal(principal, rawPath)
		if scopeErr == nil && scope.Matched && strings.TrimSpace(scope.WorkspaceID) != "" {
			return accountScopeID, scope.WorkspaceID, scope.WorkspacePath, nil
		}
	}

	if s.workspace != nil {
		list, listErr := s.workspace.ListKnownForPrincipal(principal, 1)
		if listErr == nil && len(list) > 0 {
			return accountScopeID, list[0].WorkspaceID, list[0].Path, nil
		}
	}

	if rawWorkspaceID == "" && rawPath == "" {
		return accountScopeID, "default", "", nil
	}

	return accountScopeID, rawWorkspaceID, rawPath, nil
}

// =============================================================================
// Connections API (/v1/connections)
// =============================================================================

type connectionMutationRequest struct {
	Action         string                               `json:"action"`
	WorkspaceID    string                               `json:"workspace_id"`
	WorkspacePath  string                               `json:"workspace_path"`
	ID             string                               `json:"id"`
	Name           string                               `json:"name"`
	Description    string                               `json:"description,omitempty"`
	Kind           environments.ConnectionKind          `json:"kind"`
	Host           string                               `json:"host,omitempty"`
	Port           int                                  `json:"port,omitempty"`
	User           string                               `json:"user,omitempty"`
	SSHKeyPath     string                               `json:"ssh_key_path,omitempty"`
	KnownHostsFile string                               `json:"known_hosts_file,omitempty"`
	SocketPath     string                               `json:"socket_path,omitempty"`
	DockerHost     string                               `json:"docker_host,omitempty"`
	Capabilities   *environments.ConnectionCapabilities `json:"capabilities,omitempty"`
}

func (s *Server) handleConnections(w http.ResponseWriter, r *http.Request) {
	if s.connections == nil {
		writeError(w, http.StatusInternalServerError, errors.New("connection service not configured"))
		return
	}

	switch r.Method {
	case http.MethodGet:
		accountScopeID, workspaceID, _, err := s.resolveEnvironmentScope(r, r.URL.Query().Get("workspace_id"), r.URL.Query().Get("workspace_path"))
		if err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}

		connID := strings.TrimSpace(r.URL.Query().Get("id"))
		if connID != "" {
			conn, found, getErr := s.connections.Get(accountScopeID, workspaceID, connID)
			if getErr != nil {
				writeError(w, http.StatusInternalServerError, getErr)
				return
			}
			if !found {
				writeError(w, http.StatusNotFound, fmt.Errorf("connection %q not found", connID))
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "connection": conn})
			return
		}

		limit := 100
		if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
			if parsed, pErr := strconv.Atoi(rawLimit); pErr == nil && parsed > 0 {
				limit = parsed
			}
		}

		list, listErr := s.connections.List(accountScopeID, workspaceID, limit)
		if listErr != nil {
			writeError(w, http.StatusInternalServerError, listErr)
			return
		}
		if list == nil {
			list = []environments.Connection{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "connections": list, "count": len(list)})

	case http.MethodPost:
		var req connectionMutationRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		accountScopeID, workspaceID, _, err := s.resolveEnvironmentScope(r, req.WorkspaceID, req.WorkspacePath)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}

		action := strings.ToLower(strings.TrimSpace(req.Action))
		switch action {
		case "create", "update":
			connID := strings.TrimSpace(req.ID)
			if connID == "" {
				connID = "conn-" + uuid.NewString()[:8]
			}

			conn := environments.Connection{
				ID:             connID,
				AccountScopeID: accountScopeID,
				WorkspaceID:    workspaceID,
				Name:           strings.TrimSpace(req.Name),
				Description:    strings.TrimSpace(req.Description),
				Kind:           req.Kind,
				CreatedAt:      time.Now().UnixMilli(),
				UpdatedAt:      time.Now().UnixMilli(),
			}

			if req.Capabilities != nil {
				conn.Capabilities = *req.Capabilities
			}

			switch req.Kind {
			case environments.ConnectionKindLocalDocker:
				conn.LocalDocker = &environments.LocalDockerConfig{
					SocketPath: strings.TrimSpace(req.SocketPath),
					Host:       strings.TrimSpace(req.DockerHost),
				}
				if conn.Capabilities.SupportsDocker == false && conn.Capabilities.RemoteOS == "" {
					conn.Capabilities.SupportsDocker = true
					conn.Capabilities.SupportsDirectMount = true
				}
			case environments.ConnectionKindSSH:
				port := req.Port
				if port <= 0 {
					port = 22
				}
				conn.SSH = &environments.SSHConfig{
					Host:           strings.TrimSpace(req.Host),
					Port:           port,
					User:           strings.TrimSpace(req.User),
					IdentityFile:   strings.TrimSpace(req.SSHKeyPath),
					KnownHostsFile: strings.TrimSpace(req.KnownHostsFile),
				}
				if conn.Capabilities.SupportsSSH == false && conn.Capabilities.RemoteOS == "" {
					conn.Capabilities.SupportsSSH = true
					conn.Capabilities.SupportsDocker = true
				}
			default:
				writeError(w, http.StatusBadRequest, fmt.Errorf("unsupported connection kind %q", req.Kind))
				return
			}

			if action == "update" {
				existing, found, getErr := s.connections.Get(accountScopeID, workspaceID, connID)
				if getErr != nil {
					writeError(w, http.StatusInternalServerError, getErr)
					return
				}
				if found {
					conn.CreatedAt = existing.CreatedAt
					if conn.Name == "" {
						conn.Name = existing.Name
					}
					if conn.Description == "" {
						conn.Description = existing.Description
					}
				}
			}

			if valErr := conn.Validate(); valErr != nil {
				writeError(w, http.StatusBadRequest, valErr)
				return
			}

			saved, saveErr := s.connections.Save(conn)
			if saveErr != nil {
				writeError(w, http.StatusInternalServerError, saveErr)
				return
			}

			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "connection": saved})

		case "delete":
			connID := strings.TrimSpace(req.ID)
			if connID == "" {
				writeError(w, http.StatusBadRequest, errors.New("connection id is required for delete"))
				return
			}

			deleted, delErr := s.connections.Delete(accountScopeID, workspaceID, connID)
			if delErr != nil {
				writeError(w, http.StatusInternalServerError, delErr)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": deleted, "id": connID})

		case "check":
			var conn environments.Connection
			connID := strings.TrimSpace(req.ID)
			if connID != "" {
				existing, found, getErr := s.connections.Get(accountScopeID, workspaceID, connID)
				if getErr != nil {
					writeError(w, http.StatusInternalServerError, getErr)
					return
				}
				if !found {
					writeError(w, http.StatusNotFound, fmt.Errorf("connection %q not found", connID))
					return
				}
				conn = existing
			} else {
				conn = environments.Connection{
					ID:             "check-temp",
					AccountScopeID: accountScopeID,
					WorkspaceID:    workspaceID,
					Kind:           req.Kind,
				}
				if req.Kind == environments.ConnectionKindLocalDocker {
					conn.LocalDocker = &environments.LocalDockerConfig{
						SocketPath: strings.TrimSpace(req.SocketPath),
						Host:       strings.TrimSpace(req.DockerHost),
					}
				} else if req.Kind == environments.ConnectionKindSSH {
					port := req.Port
					if port <= 0 {
						port = 22
					}
					conn.SSH = &environments.SSHConfig{
						Host:           strings.TrimSpace(req.Host),
						Port:           port,
						User:           strings.TrimSpace(req.User),
						IdentityFile:   strings.TrimSpace(req.SSHKeyPath),
						KnownHostsFile: strings.TrimSpace(req.KnownHostsFile),
					}
				}
			}

			if s.envProviders == nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "healthy": false, "diagnostics": "provider registry not configured"})
				return
			}

			p, found := s.envProviders.Get(conn.Kind)
			if !found {
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "healthy": false, "diagnostics": fmt.Sprintf("no provider registered for kind %s", conn.Kind)})
				return
			}

			checkErr := p.ValidateConnection(r.Context(), &conn)
			if checkErr != nil {
				writeJSON(w, http.StatusOK, map[string]any{
					"ok":          true,
					"healthy":     false,
					"error":       checkErr.Error(),
					"diagnostics": fmt.Sprintf("Connectivity check failed: %v", checkErr),
				})
				return
			}

			caps, _ := p.Capabilities(r.Context(), &conn)
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":           true,
				"healthy":      true,
				"diagnostics":  "Connection verified and responsive.",
				"capabilities": caps,
			})

		case "capabilities":
			connID := strings.TrimSpace(req.ID)
			existing, found, getErr := s.connections.Get(accountScopeID, workspaceID, connID)
			if getErr != nil {
				writeError(w, http.StatusInternalServerError, getErr)
				return
			}
			if !found {
				writeError(w, http.StatusNotFound, fmt.Errorf("connection %q not found", connID))
				return
			}

			if s.envProviders == nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "capabilities": existing.Capabilities})
				return
			}

			p, pFound := s.envProviders.Get(existing.Kind)
			if !pFound {
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "capabilities": existing.Capabilities})
				return
			}

			caps, capsErr := p.Capabilities(r.Context(), &existing)
			if capsErr != nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "capabilities": existing.Capabilities, "error": capsErr.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "capabilities": caps})

		default:
			writeError(w, http.StatusBadRequest, fmt.Errorf("unknown connection action %q", action))
		}

	default:
		methodNotAllowed(w)
	}
}

// =============================================================================
// Environments API (/v1/environments)
// =============================================================================

type environmentMutationRequest struct {
	Action                   string                    `json:"action"`
	WorkspaceID              string                    `json:"workspace_id"`
	WorkspacePath            string                    `json:"workspace_path"`
	ID                       string                    `json:"id"`
	Environment              *environments.Environment `json:"environment,omitempty"`
	DefaultTestEnvironmentID string                    `json:"default_test_environment_id,omitempty"`
	DefaultConnectionID      string                    `json:"default_connection_id,omitempty"`
}

func (s *Server) handleEnvironments(w http.ResponseWriter, r *http.Request) {
	if s.environments == nil {
		writeError(w, http.StatusInternalServerError, errors.New("environment service not configured"))
		return
	}

	switch r.Method {
	case http.MethodGet:
		accountScopeID, workspaceID, _, err := s.resolveEnvironmentScope(r, r.URL.Query().Get("workspace_id"), r.URL.Query().Get("workspace_path"))
		if err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}

		var settings environments.WorkspaceSettings
		if s.workspaceEnvSettings != nil {
			st, _, _ := s.workspaceEnvSettings.GetWorkspaceSettings(accountScopeID, workspaceID)
			settings = st
		}

		envID := strings.TrimSpace(r.URL.Query().Get("id"))
		if envID != "" {
			env, found, getErr := s.environments.Get(accountScopeID, workspaceID, envID)
			if getErr != nil {
				writeError(w, http.StatusInternalServerError, getErr)
				return
			}
			if !found {
				writeError(w, http.StatusNotFound, fmt.Errorf("environment %q not found", envID))
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "environment": env, "settings": settings})
			return
		}

		limit := 100
		if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
			if parsed, pErr := strconv.Atoi(rawLimit); pErr == nil && parsed > 0 {
				limit = parsed
			}
		}

		list, listErr := s.environments.List(accountScopeID, workspaceID, limit)
		if listErr != nil {
			writeError(w, http.StatusInternalServerError, listErr)
			return
		}
		if list == nil {
			list = []environments.Environment{}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"ok":           true,
			"environments": list,
			"settings":     settings,
			"count":        len(list),
		})

	case http.MethodPost:
		var req environmentMutationRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		accountScopeID, workspaceID, _, err := s.resolveEnvironmentScope(r, req.WorkspaceID, req.WorkspacePath)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}

		action := strings.ToLower(strings.TrimSpace(req.Action))
		switch action {
		case "create", "update":
			if req.Environment == nil {
				writeError(w, http.StatusBadRequest, errors.New("environment object is required"))
				return
			}

			env := *req.Environment
			env.AccountScopeID = accountScopeID
			env.WorkspaceID = workspaceID
			if strings.TrimSpace(env.ID) == "" {
				env.ID = "env-" + uuid.NewString()[:8]
			}
			if env.CreatedAt == 0 {
				env.CreatedAt = time.Now().UnixMilli()
			}
			env.UpdatedAt = time.Now().UnixMilli()

			if action == "update" {
				existing, found, getErr := s.environments.Get(accountScopeID, workspaceID, env.ID)
				if getErr != nil {
					writeError(w, http.StatusInternalServerError, getErr)
					return
				}
				if found && existing.CreatedAt > 0 {
					env.CreatedAt = existing.CreatedAt
				}
			}

			if valErr := env.Validate(); valErr != nil {
				writeError(w, http.StatusBadRequest, valErr)
				return
			}

			saved, saveErr := s.environments.Save(env)
			if saveErr != nil {
				writeError(w, http.StatusInternalServerError, saveErr)
				return
			}

			var currentSettings environments.WorkspaceSettings
			if s.workspaceEnvSettings != nil {
				st, _, _ := s.workspaceEnvSettings.GetWorkspaceSettings(accountScopeID, workspaceID)
				currentSettings = st
			}

			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "environment": saved, "settings": currentSettings})

		case "delete":
			envID := strings.TrimSpace(req.ID)
			if envID == "" {
				writeError(w, http.StatusBadRequest, errors.New("environment id is required for delete"))
				return
			}

			deleted, delErr := s.environments.Delete(accountScopeID, workspaceID, envID)
			if delErr != nil {
				writeError(w, http.StatusInternalServerError, delErr)
				return
			}

			if s.workspaceEnvSettings != nil {
				st, found, _ := s.workspaceEnvSettings.GetWorkspaceSettings(accountScopeID, workspaceID)
				if found && st.DefaultTestEnvironmentID == envID {
					empty := ""
					_, _ = s.workspaceEnvSettings.UpdateWorkspaceSettings(accountScopeID, workspaceID, &empty, nil)
				}
			}

			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": deleted, "id": envID})

		case "set_default_test_environment":
			if s.workspaceEnvSettings == nil {
				writeError(w, http.StatusInternalServerError, errors.New("workspace settings store not configured"))
				return
			}

			targetID := strings.TrimSpace(req.DefaultTestEnvironmentID)
			if targetID != "" {
				_, found, getErr := s.environments.Get(accountScopeID, workspaceID, targetID)
				if getErr != nil {
					writeError(w, http.StatusInternalServerError, getErr)
					return
				}
				if !found {
					writeError(w, http.StatusNotFound, fmt.Errorf("environment %q not found", targetID))
					return
				}
			}

			_, updateErr := s.workspaceEnvSettings.UpdateWorkspaceSettings(accountScopeID, workspaceID, &targetID, nil)
			if updateErr != nil {
				writeError(w, http.StatusInternalServerError, updateErr)
				return
			}

			updatedSettings, _, _ := s.workspaceEnvSettings.GetWorkspaceSettings(accountScopeID, workspaceID)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "settings": updatedSettings})

		case "set_default_connection":
			if s.workspaceEnvSettings == nil {
				writeError(w, http.StatusInternalServerError, errors.New("workspace settings store not configured"))
				return
			}

			targetID := strings.TrimSpace(req.DefaultConnectionID)
			if targetID != "" && s.connections != nil {
				_, found, getErr := s.connections.Get(accountScopeID, workspaceID, targetID)
				if getErr != nil {
					writeError(w, http.StatusInternalServerError, getErr)
					return
				}
				if !found {
					writeError(w, http.StatusNotFound, fmt.Errorf("connection %q not found", targetID))
					return
				}
			}

			_, updateErr := s.workspaceEnvSettings.UpdateWorkspaceSettings(accountScopeID, workspaceID, nil, &targetID)
			if updateErr != nil {
				writeError(w, http.StatusInternalServerError, updateErr)
				return
			}

			updatedSettings, _, _ := s.workspaceEnvSettings.GetWorkspaceSettings(accountScopeID, workspaceID)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "settings": updatedSettings})

		default:
			writeError(w, http.StatusBadRequest, fmt.Errorf("unknown environment action %q", action))
		}

	default:
		methodNotAllowed(w)
	}
}

// =============================================================================
// Deployments API (/v1/deployments)
// =============================================================================

type deploymentMutationRequest struct {
	Action         string                    `json:"action"`
	WorkspaceID    string                    `json:"workspace_id"`
	WorkspacePath  string                    `json:"workspace_path"`
	DeploymentID   string                    `json:"deployment_id"`
	LeaseID        string                    `json:"lease_id"`
	EnvironmentID  string                    `json:"environment_id"`
	ConnectionID   string                    `json:"connection_id"`
	ConsumerType   environments.ConsumerType `json:"consumer_type"`
	ConsumerID     string                    `json:"consumer_id"`
	DeploymentName string                    `json:"deployment_name"`
	ReleaseReason  string                    `json:"release_reason"`
	Force          bool                      `json:"force"`
}

func (s *Server) handleDeployments(w http.ResponseWriter, r *http.Request) {
	if s.deployments == nil {
		writeError(w, http.StatusInternalServerError, errors.New("deployment service not configured"))
		return
	}

	switch r.Method {
	case http.MethodGet:
		accountScopeID, workspaceID, _, err := s.resolveEnvironmentScope(r, r.URL.Query().Get("workspace_id"), r.URL.Query().Get("workspace_path"))
		if err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}

		depID := strings.TrimSpace(r.URL.Query().Get("id"))
		if depID != "" {
			dep, found, getErr := s.deployments.GetDeployment(accountScopeID, workspaceID, depID)
			if getErr != nil {
				writeError(w, http.StatusInternalServerError, getErr)
				return
			}
			if !found {
				writeError(w, http.StatusNotFound, fmt.Errorf("deployment %q not found", depID))
				return
			}
			lease, hasLease, _ := s.deployments.GetActiveLease(accountScopeID, workspaceID, dep.ID)
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":               true,
				"deployment":       dep,
				"active_lease":     lease,
				"has_active_lease": hasLease,
			})
			return
		}

		limit := 100
		if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
			if parsed, pErr := strconv.Atoi(rawLimit); pErr == nil && parsed > 0 {
				limit = parsed
			}
		}

		envFilter := strings.TrimSpace(r.URL.Query().Get("environment_id"))
		var list []environments.Deployment
		var listErr error
		if envFilter != "" {
			list, listErr = s.deployments.ListDeploymentsByEnvironment(accountScopeID, workspaceID, envFilter, limit)
		} else {
			list, listErr = s.deployments.ListDeployments(accountScopeID, workspaceID, limit)
		}
		if listErr != nil {
			writeError(w, http.StatusInternalServerError, listErr)
			return
		}
		if list == nil {
			list = []environments.Deployment{}
		}

		activeLeases := make(map[string]environments.DeploymentLease)
		for _, dep := range list {
			if lease, ok, _ := s.deployments.GetActiveLease(accountScopeID, workspaceID, dep.ID); ok {
				activeLeases[dep.ID] = lease
			}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"ok":            true,
			"deployments":   list,
			"active_leases": activeLeases,
			"count":         len(list),
		})

	case http.MethodPost:
		var req deploymentMutationRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		accountScopeID, workspaceID, workspacePath, err := s.resolveEnvironmentScope(r, req.WorkspaceID, req.WorkspacePath)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}

		action := strings.ToLower(strings.TrimSpace(req.Action))
		switch action {
		case "ensure":
			consumerType := req.ConsumerType
			if consumerType == "" {
				consumerType = environments.ConsumerTypeSession
			}
			consumerID := strings.TrimSpace(req.ConsumerID)
			if consumerID == "" {
				consumerID = "client-" + uuid.NewString()[:8]
			}

			ensureReq := lifecycle.EnsureDeploymentRequest{
				AccountScopeID: accountScopeID,
				WorkspaceID:    workspaceID,
				EnvironmentID:  strings.TrimSpace(req.EnvironmentID),
				ConnectionID:   strings.TrimSpace(req.ConnectionID),
				ConsumerType:   consumerType,
				ConsumerID:     consumerID,
				DeploymentName: strings.TrimSpace(req.DeploymentName),
				WorkspacePath:  workspacePath,
			}

			result, ensureErr := s.deployments.EnsureDeployment(r.Context(), ensureReq)
			if ensureErr != nil {
				writeError(w, http.StatusBadRequest, ensureErr)
				return
			}

			writeJSON(w, http.StatusOK, map[string]any{
				"ok":         true,
				"deployment": result.Deployment,
				"lease":      result.Lease,
				"reused":     result.Reused,
			})

		case "start":
			depID := strings.TrimSpace(req.DeploymentID)
			if depID == "" {
				writeError(w, http.StatusBadRequest, errors.New("deployment_id is required"))
				return
			}
			if startErr := s.deployments.StartDeployment(r.Context(), accountScopeID, workspaceID, depID); startErr != nil {
				writeError(w, http.StatusInternalServerError, startErr)
				return
			}
			dep, _, _ := s.deployments.GetDeployment(accountScopeID, workspaceID, depID)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deployment": dep})

		case "stop":
			depID := strings.TrimSpace(req.DeploymentID)
			if depID == "" {
				writeError(w, http.StatusBadRequest, errors.New("deployment_id is required"))
				return
			}
			if stopErr := s.deployments.StopDeployment(r.Context(), accountScopeID, workspaceID, depID); stopErr != nil {
				writeError(w, http.StatusInternalServerError, stopErr)
				return
			}
			dep, _, _ := s.deployments.GetDeployment(accountScopeID, workspaceID, depID)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deployment": dep})

		case "release":
			leaseID := strings.TrimSpace(req.LeaseID)
			if leaseID == "" {
				// If lease_id wasn't provided, try resolving from deployment_id
				depID := strings.TrimSpace(req.DeploymentID)
				if depID != "" {
					if active, ok, _ := s.deployments.GetActiveLease(accountScopeID, workspaceID, depID); ok {
						leaseID = active.ID
					}
				}
			}
			if leaseID == "" {
				writeError(w, http.StatusBadRequest, errors.New("lease_id or deployment_id with active lease is required"))
				return
			}

			reason := strings.TrimSpace(req.ReleaseReason)
			if reason == "" {
				reason = "user released via desktop ui"
			}

			releaseReq := lifecycle.ReleaseDeploymentRequest{
				AccountScopeID: accountScopeID,
				WorkspaceID:    workspaceID,
				LeaseID:        leaseID,
				Reason:         reason,
			}

			result, relErr := s.deployments.ReleaseDeployment(r.Context(), releaseReq)
			if relErr != nil {
				writeError(w, http.StatusBadRequest, relErr)
				return
			}

			writeJSON(w, http.StatusOK, map[string]any{
				"ok":         true,
				"lease":      result.Lease,
				"deployment": result.Deployment,
			})

		case "destroy":
			depID := strings.TrimSpace(req.DeploymentID)
			if depID == "" {
				writeError(w, http.StatusBadRequest, errors.New("deployment_id is required"))
				return
			}

			destroyReq := lifecycle.DestroyDeploymentRequest{
				AccountScopeID: accountScopeID,
				WorkspaceID:    workspaceID,
				DeploymentID:   depID,
				Reason:         "user destroyed via desktop ui",
			}

			if destErr := s.deployments.DestroyDeployment(r.Context(), destroyReq); destErr != nil {
				writeError(w, http.StatusInternalServerError, destErr)
				return
			}

			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "destroyed": true, "id": depID})

		default:
			writeError(w, http.StatusBadRequest, fmt.Errorf("unknown deployment action %q", action))
		}

	default:
		methodNotAllowed(w)
	}
}
