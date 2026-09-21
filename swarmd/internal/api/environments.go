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
	Submit(ctx context.Context, req lifecycle.SubmitOperationRequest) (*environments.EnvironmentOperation, error)
	Get(ctx context.Context, accountScopeID, workspaceID, operationID string) (environments.EnvironmentOperation, bool, error)
	History(ctx context.Context, q environments.OperationHistoryQuery) (environments.OperationHistoryPage, error)
	Summary(ctx context.Context, accountScopeID, workspaceID string) (environments.EnvironmentSummary, error)
	Cancel(ctx context.Context, req lifecycle.CancelOperationRequest) (*environments.EnvironmentOperation, error)
	CancelOwner(ctx context.Context, req lifecycle.CancelOwnerRequest) (int, error)
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

	if rawWorkspaceID == "" && rawPath == "" {
		return "", "", "", errors.New("workspace_id or workspace_path is required")
	}

	if s.workspace == nil {
		if rawWorkspaceID != "" {
			return accountScopeID, rawWorkspaceID, rawPath, nil
		}
		return accountScopeID, "ws-test", rawPath, nil
	}

	var entryPath string
	if rawWorkspaceID != "" {
		entry, found, err := s.workspace.GetByWorkspaceIDForPrincipal(principal, rawWorkspaceID)
		if err != nil {
			return "", "", "", fmt.Errorf("lookup workspace %q: %w", rawWorkspaceID, err)
		}
		if !found {
			return "", "", "", fmt.Errorf("workspace %q not found", rawWorkspaceID)
		}
		workspaceID = entry.WorkspaceID
		entryPath = entry.Path
	}

	if rawPath != "" {
		scope, err := s.workspace.ScopeForPathForPrincipal(principal, rawPath)
		if err != nil {
			return "", "", "", fmt.Errorf("resolve workspace path %q: %w", rawPath, err)
		}
		if !scope.Matched || strings.TrimSpace(scope.WorkspaceID) == "" {
			return "", "", "", fmt.Errorf("path %q does not belong to an authorized workspace", rawPath)
		}
		if workspaceID != "" && scope.WorkspaceID != workspaceID {
			return "", "", "", fmt.Errorf("workspace ID %q and path %q do not match", workspaceID, rawPath)
		}
		workspaceID = scope.WorkspaceID
		workspacePath = scope.WorkspacePath
	} else {
		workspacePath = entryPath
	}

	return accountScopeID, workspaceID, workspacePath, nil
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
	EnvironmentID            string                    `json:"environment_id"`
	DeploymentID             string                    `json:"deployment_id"`
	OperationID              string                    `json:"operation_id"`
	LeaseID                  string                    `json:"lease_id"`
	Environment              *environments.Environment `json:"environment,omitempty"`
	DefaultTestEnvironmentID string                    `json:"default_test_environment_id,omitempty"`
	DefaultConnectionID      string                    `json:"default_connection_id,omitempty"`
	ConnectionID             string                    `json:"connection_id,omitempty"`
	DeploymentName           string                    `json:"deployment_name,omitempty"`
	ConsumerType             environments.ConsumerType `json:"consumer_type,omitempty"`
	ConsumerID               string                    `json:"consumer_id,omitempty"`
	SessionID                string                    `json:"session_id,omitempty"`
	Command                  []string                  `json:"command,omitempty"`
	WorkingDir               string                    `json:"working_dir,omitempty"`
	Env                      map[string]string         `json:"env,omitempty"`
	EnvOverrides             map[string]string         `json:"env_overrides,omitempty"`
	TTLMillis                int64                     `json:"ttl_millis,omitempty"`
	TimeoutMS                int                       `json:"timeout_ms,omitempty"`
	Deadline                 int64                     `json:"deadline,omitempty"`
	IdempotencyKey           string                    `json:"idempotency_key,omitempty"`
	Reason                   string                    `json:"reason,omitempty"`
	ReleaseReason            string                    `json:"release_reason,omitempty"`
	MaxOutput                int                       `json:"max_output,omitempty"`
}

func (s *Server) handleEnvironments(w http.ResponseWriter, r *http.Request) {
	if s.environments == nil {
		writeError(w, http.StatusInternalServerError, errors.New("environment service not configured"))
		return
	}

	subpath := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/environments"), "/")

	switch r.Method {
	case http.MethodGet:
		accountScopeID, workspaceID, _, err := s.resolveEnvironmentScope(r, r.URL.Query().Get("workspace_id"), r.URL.Query().Get("workspace_path"))
		if err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}

		actionQuery := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("action")))

		if subpath == "summary" || actionQuery == "summary" {
			if s.deployments == nil {
				writeError(w, http.StatusInternalServerError, errors.New("deployment supervisor not configured"))
				return
			}
			summary, sErr := s.deployments.Summary(r.Context(), accountScopeID, workspaceID)
			if sErr != nil {
				writeError(w, http.StatusInternalServerError, sErr)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "summary": summary})
			return
		}

		if subpath == "history" || actionQuery == "history" {
			if s.deployments == nil {
				writeError(w, http.StatusInternalServerError, errors.New("deployment supervisor not configured"))
				return
			}
			limit := 50
			if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
				if parsed, pErr := strconv.Atoi(rawLimit); pErr == nil && parsed > 0 {
					limit = parsed
				}
			}
			q := environments.OperationHistoryQuery{
				AccountScopeID: accountScopeID,
				WorkspaceID:    workspaceID,
				EnvironmentID:  strings.TrimSpace(r.URL.Query().Get("environment_id")),
				DeploymentID:   strings.TrimSpace(r.URL.Query().Get("deployment_id")),
				Actor:          strings.TrimSpace(r.URL.Query().Get("actor")),
				SessionID:      strings.TrimSpace(r.URL.Query().Get("session_id")),
				WorkerID:       strings.TrimSpace(r.URL.Query().Get("worker_id")),
				Status:         environments.OperationStatus(strings.TrimSpace(r.URL.Query().Get("status"))),
				Action:         strings.TrimSpace(r.URL.Query().Get("action_filter")),
				Timezone:       strings.TrimSpace(r.URL.Query().Get("timezone")),
				StartDate:      strings.TrimSpace(r.URL.Query().Get("start_date")),
				EndDate:        strings.TrimSpace(r.URL.Query().Get("end_date")),
				Cursor:         strings.TrimSpace(r.URL.Query().Get("cursor")),
				Limit:          limit,
			}
			page, hErr := s.deployments.History(r.Context(), q)
			if hErr != nil {
				writeError(w, http.StatusInternalServerError, hErr)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":           true,
				"history":      page,
				"operations":   page.Operations,
				"daily_totals": page.DailyTotals,
				"summary":      page.Summary,
				"next_cursor":  page.NextCursor,
				"has_more":     page.HasMore,
			})
			return
		}

		if subpath == "operations" || actionQuery == "get_operation" {
			if s.deployments == nil {
				writeError(w, http.StatusInternalServerError, errors.New("deployment supervisor not configured"))
				return
			}
			opID := firstNonEmptyString(strings.TrimSpace(r.URL.Query().Get("operation_id")), strings.TrimSpace(r.URL.Query().Get("id")))
			if opID == "" {
				writeError(w, http.StatusBadRequest, errors.New("operation_id is required"))
				return
			}
			op, found, getErr := s.deployments.Get(r.Context(), accountScopeID, workspaceID, opID)
			if getErr != nil {
				writeError(w, http.StatusInternalServerError, getErr)
				return
			}
			if !found {
				writeError(w, http.StatusNotFound, fmt.Errorf("operation %q not found", opID))
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "operation": op, "operation_id": op.OperationID, "status": op.Status})
			return
		}

		if actionQuery == "list_deployments" {
			if s.deployments == nil {
				writeError(w, http.StatusInternalServerError, errors.New("deployment supervisor not configured"))
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
			return
		}

		if actionQuery == "get_deployment" {
			if s.deployments == nil {
				writeError(w, http.StatusInternalServerError, errors.New("deployment supervisor not configured"))
				return
			}
			depID := strings.TrimSpace(r.URL.Query().Get("deployment_id"))
			if depID == "" {
				writeError(w, http.StatusBadRequest, errors.New("deployment_id is required"))
				return
			}
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

		var settings environments.WorkspaceSettings
		if s.workspaceEnvSettings != nil {
			st, _, _ := s.workspaceEnvSettings.GetWorkspaceSettings(accountScopeID, workspaceID)
			settings = st
		}

		envID := firstNonEmptyString(strings.TrimSpace(r.URL.Query().Get("environment_id")), strings.TrimSpace(r.URL.Query().Get("id")))
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

		accountScopeID, workspaceID, workspacePath, err := s.resolveEnvironmentScope(r, req.WorkspaceID, req.WorkspacePath)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}

		action := strings.ToLower(strings.TrimSpace(req.Action))

		// Cancellation routing
		if subpath == "cancel" || subpath == "operations/cancel" || action == "cancel" || action == "cancel_operation" {
			if s.deployments == nil {
				writeError(w, http.StatusInternalServerError, errors.New("deployment supervisor not configured"))
				return
			}
			opID := firstNonEmptyString(strings.TrimSpace(req.OperationID), strings.TrimSpace(req.ID))
			if opID == "" {
				writeError(w, http.StatusBadRequest, errors.New("operation_id is required for cancel"))
				return
			}
			reason := firstNonEmptyString(strings.TrimSpace(req.Reason), "cancelled via api")
			op, cErr := s.deployments.Cancel(r.Context(), lifecycle.CancelOperationRequest{
				AccountScopeID: accountScopeID,
				WorkspaceID:    workspaceID,
				OperationID:    opID,
				Reason:         reason,
			})
			if cErr != nil {
				writeError(w, http.StatusBadRequest, cErr)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "operation": op, "operation_id": op.OperationID, "status": op.Status})
			return
		}

		// Supervised runtime mutations on /v1/environments
		switch action {
		case "summary":
			if s.deployments == nil {
				writeError(w, http.StatusInternalServerError, errors.New("deployment supervisor not configured"))
				return
			}
			summary, sErr := s.deployments.Summary(r.Context(), accountScopeID, workspaceID)
			if sErr != nil {
				writeError(w, http.StatusInternalServerError, sErr)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "summary": summary})
			return

		case "history":
			if s.deployments == nil {
				writeError(w, http.StatusInternalServerError, errors.New("deployment supervisor not configured"))
				return
			}
			q := environments.OperationHistoryQuery{
				AccountScopeID: accountScopeID,
				WorkspaceID:    workspaceID,
				EnvironmentID:  strings.TrimSpace(req.EnvironmentID),
				DeploymentID:   strings.TrimSpace(req.DeploymentID),
				Actor:          strings.TrimSpace(req.ConsumerID),
				SessionID:      strings.TrimSpace(req.SessionID),
				Reason:         strings.TrimSpace(req.Reason),
			}
			page, hErr := s.deployments.History(r.Context(), q)
			if hErr != nil {
				writeError(w, http.StatusInternalServerError, hErr)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":           true,
				"history":      page,
				"operations":   page.Operations,
				"daily_totals": page.DailyTotals,
				"summary":      page.Summary,
				"next_cursor":  page.NextCursor,
				"has_more":     page.HasMore,
			})
			return

		case "ensure", "deploy", "exec", "start", "stop", "destroy", "release":
			if s.deployments == nil {
				writeError(w, http.StatusInternalServerError, errors.New("deployment supervisor not configured"))
				return
			}

			principal, _ := PrincipalFromRequest(r)
			callerActor := principal.UserID
			callerSessionID := ""
			if req.SessionID != "" && s.sessions != nil {
				if sess, found, _ := s.sessions.GetSession(strings.TrimSpace(req.SessionID)); found && sess.AccountScopeID == accountScopeID && sess.UserID == principal.UserID {
					callerSessionID = sess.ID
					if sess.WorktreeEnabled && strings.TrimSpace(sess.WorktreeRootPath) != "" {
						workspacePath = sess.WorktreeRootPath
					}
				}
			}

			subReq := lifecycle.SubmitOperationRequest{
				AccountScopeID: accountScopeID,
				WorkspaceID:    workspaceID,
				Action:         action,
				EnvironmentID:  firstNonEmptyString(strings.TrimSpace(req.EnvironmentID), strings.TrimSpace(req.ID)),
				DeploymentID:   strings.TrimSpace(req.DeploymentID),
				LeaseID:        strings.TrimSpace(req.LeaseID),
				ConnectionID:   strings.TrimSpace(req.ConnectionID),
				DeploymentName: strings.TrimSpace(req.DeploymentName),
				WorkspacePath:  workspacePath,
				ConsumerType:   req.ConsumerType,
				ConsumerID:     firstNonEmptyString(callerSessionID, strings.TrimSpace(req.ConsumerID)),
				Command:        req.Command,
				WorkingDir:     strings.TrimSpace(req.WorkingDir),
				Env:            req.Env,
				EnvOverrides:   req.EnvOverrides,
				TTLMillis:      req.TTLMillis,
				Deadline:       req.Deadline,
				IdempotencyKey: strings.TrimSpace(req.IdempotencyKey),
				Reason:         firstNonEmptyString(strings.TrimSpace(req.ReleaseReason), strings.TrimSpace(req.Reason)),
				MaxOutput:      req.MaxOutput,
				Attribution: environments.OperationAttribution{
					Actor:     callerActor,
					SessionID: callerSessionID,
				},
			}
			if req.TimeoutMS > 0 {
				subReq.Timeout = time.Duration(req.TimeoutMS) * time.Millisecond
			}

			op, submitErr := s.deployments.Submit(r.Context(), subReq)
			if submitErr != nil {
				writeError(w, http.StatusBadRequest, submitErr)
				return
			}

			resp := map[string]any{
				"ok":           true,
				"status":       op.Status,
				"operation_id": op.OperationID,
				"operation":    op,
			}
			if op.DeploymentID != "" {
				resp["deployment_id"] = op.DeploymentID
				if dep, found, _ := s.deployments.GetDeployment(accountScopeID, workspaceID, op.DeploymentID); found {
					resp["deployment"] = dep
				}
				if lease, hasLease, _ := s.deployments.GetActiveLease(accountScopeID, workspaceID, op.DeploymentID); hasLease {
					resp["lease"] = lease
				}
			}
			if action == "destroy" {
				resp["destroyed"] = true
				resp["id"] = op.DeploymentID
			}
			writeJSON(w, http.StatusOK, resp)
			return

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
			envID := firstNonEmptyString(strings.TrimSpace(req.EnvironmentID), strings.TrimSpace(req.ID))
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
	SessionID      string                    `json:"session_id"`
	DeploymentName string                    `json:"deployment_name"`
	ReleaseReason  string                    `json:"release_reason"`
	Reason         string                    `json:"reason"`
	Force          bool                      `json:"force"`
	Command        []string                  `json:"command"`
	WorkingDir     string                    `json:"working_dir"`
	Env            map[string]string         `json:"env"`
	EnvOverrides   map[string]string         `json:"env_overrides"`
	TTLMillis      int64                     `json:"ttl_millis"`
	TimeoutMS      int                       `json:"timeout_ms"`
	Deadline       int64                     `json:"deadline"`
	IdempotencyKey string                    `json:"idempotency_key"`
	MaxOutput      int                       `json:"max_output"`
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

		principal, _ := PrincipalFromRequest(r)
		callerActor := principal.UserID
		callerSessionID := ""
		if req.SessionID != "" && s.sessions != nil {
			if sess, found, _ := s.sessions.GetSession(strings.TrimSpace(req.SessionID)); found && sess.AccountScopeID == accountScopeID && sess.UserID == principal.UserID {
				callerSessionID = sess.ID
				if sess.WorktreeEnabled && strings.TrimSpace(sess.WorktreeRootPath) != "" {
					workspacePath = sess.WorktreeRootPath
				}
			}
		}

		action := strings.ToLower(strings.TrimSpace(req.Action))
		depID := strings.TrimSpace(req.DeploymentID)
		leaseID := strings.TrimSpace(req.LeaseID)

		subReq := lifecycle.SubmitOperationRequest{
			AccountScopeID: accountScopeID,
			WorkspaceID:    workspaceID,
			Action:         action,
			EnvironmentID:  strings.TrimSpace(req.EnvironmentID),
			DeploymentID:   depID,
			LeaseID:        leaseID,
			ConnectionID:   strings.TrimSpace(req.ConnectionID),
			DeploymentName: strings.TrimSpace(req.DeploymentName),
			WorkspacePath:  workspacePath,
			ConsumerType:   req.ConsumerType,
			ConsumerID:     firstNonEmptyString(callerSessionID, strings.TrimSpace(req.ConsumerID)),
			Command:        req.Command,
			WorkingDir:     strings.TrimSpace(req.WorkingDir),
			Env:            req.Env,
			EnvOverrides:   req.EnvOverrides,
			TTLMillis:      req.TTLMillis,
			Deadline:       req.Deadline,
			IdempotencyKey: strings.TrimSpace(req.IdempotencyKey),
			Reason:         firstNonEmptyString(strings.TrimSpace(req.ReleaseReason), strings.TrimSpace(req.Reason)),
			MaxOutput:      req.MaxOutput,
			Attribution: environments.OperationAttribution{
				Actor:     callerActor,
				SessionID: callerSessionID,
			},
		}
		if req.TimeoutMS > 0 {
			subReq.Timeout = time.Duration(req.TimeoutMS) * time.Millisecond
		}

		op, submitErr := s.deployments.Submit(r.Context(), subReq)
		if submitErr != nil {
			writeError(w, http.StatusBadRequest, submitErr)
			return
		}

		resp := map[string]any{
			"ok":           true,
			"status":       op.Status,
			"operation_id": op.OperationID,
			"operation":    op,
		}
		if op.DeploymentID != "" {
			resp["deployment_id"] = op.DeploymentID
			if dep, found, _ := s.deployments.GetDeployment(accountScopeID, workspaceID, op.DeploymentID); found {
				resp["deployment"] = dep
			}
			if lease, hasLease, _ := s.deployments.GetActiveLease(accountScopeID, workspaceID, op.DeploymentID); hasLease {
				resp["lease"] = lease
			}
		}
		if action == "destroy" {
			resp["destroyed"] = true
			resp["id"] = op.DeploymentID
		}
		writeJSON(w, http.StatusOK, resp)

	default:
		methodNotAllowed(w)
	}
}
