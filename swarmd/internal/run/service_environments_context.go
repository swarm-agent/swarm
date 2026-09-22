package run

import (
	"context"
	"fmt"
	"strings"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/tool"
)

// MaxWorkspaceEnvironmentPromptBytes defines the upper bound for the injected
// workspace environment context block to prevent excessive token overhead.
const MaxWorkspaceEnvironmentPromptBytes = 1536

type environmentConnectionStore interface {
	Get(accountScopeID, workspaceID, connectionID string) (environments.Connection, bool, error)
	List(accountScopeID, workspaceID string, limit int) ([]environments.Connection, error)
}

type environmentDefinitionStore interface {
	Get(accountScopeID, workspaceID, environmentID string) (environments.Environment, bool, error)
	List(accountScopeID, workspaceID string, limit int) ([]environments.Environment, error)
}

type environmentDeploymentService interface {
	ResolveConnection(ctx context.Context, accountScopeID, workspaceID string, explicitConnectionID string, env *environments.Environment) (*environments.Connection, error)
	ListDeployments(accountScopeID, workspaceID string, limit int) ([]environments.Deployment, error)
	GetActiveLease(accountScopeID, workspaceID, deploymentID string) (environments.DeploymentLease, bool, error)
}

type environmentWorkspaceSettingsStore interface {
	GetWorkspaceSettings(accountScopeID, workspaceID string) (environments.WorkspaceSettings, bool, error)
}

// ActiveDeploymentSummary captures concise runtime deployment and lease state for prompt context.
type ActiveDeploymentSummary struct {
	DeploymentID  string `json:"deployment_id"`
	EnvironmentID string `json:"environment_id"`
	Status        string `json:"status"`
	LeaseID       string `json:"lease_id,omitempty"`
	ConsumerType  string `json:"consumer_type,omitempty"`
	ConsumerID    string `json:"consumer_id,omitempty"`
}

// WorkspaceEnvironmentContext represents the resolved environment context for a workspace.
type WorkspaceEnvironmentContext struct {
	WorkspaceID              string                     `json:"workspace_id"`
	AccountScopeID           string                     `json:"account_scope_id"`
	DefaultTestEnvironmentID string                     `json:"default_test_environment_id,omitempty"`
	DefaultTestEnvironment   *environments.Environment  `json:"default_test_environment,omitempty"`
	DefaultConnectionID      string                     `json:"default_connection_id,omitempty"`
	ResolvedConnection       *environments.Connection   `json:"resolved_connection,omitempty"`
	ConnectionTier           int                        `json:"connection_tier,omitempty"` // 1: explicit, 2: env preferred, 3: workspace default, 0: none
	ConnectionResolutionNote string                     `json:"connection_resolution_note,omitempty"`
	AvailableEnvironments    []environments.Environment `json:"available_environments,omitempty"`
	ActiveDeployments        []ActiveDeploymentSummary  `json:"active_deployments,omitempty"`
	FallbackReason           string                     `json:"fallback_reason,omitempty"`
}

// SetEnvironmentServices wires the environment, connection, deployment, and workspace settings stores.
func (s *Service) SetEnvironmentServices(
	connections environmentConnectionStore,
	definitions environmentDefinitionStore,
	deployments environmentDeploymentService,
	settings environmentWorkspaceSettingsStore,
) {
	if s == nil {
		return
	}
	s.envConnections = connections
	s.envDefinitions = definitions
	s.envDeployments = deployments
	s.envWorkspaceSettings = settings
}

// ResolveDefaultTestbench resolves the default test environment, connection precedence,
// available environments, and active deployments for the specified workspace.
func (s *Service) ResolveDefaultTestbench(ctx context.Context, accountScopeID, workspaceID string) (*WorkspaceEnvironmentContext, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	workspaceID = strings.TrimSpace(workspaceID)
	if accountScopeID == "" || workspaceID == "" {
		return &WorkspaceEnvironmentContext{
			AccountScopeID: accountScopeID,
			WorkspaceID:    workspaceID,
			FallbackReason: "account scope ID or workspace ID is missing",
		}, nil
	}

	result := &WorkspaceEnvironmentContext{
		AccountScopeID: accountScopeID,
		WorkspaceID:    workspaceID,
	}

	// 1. Resolve workspace settings (default_test_environment_id, default_connection_id)
	var defaultTestEnvID string
	var defaultConnID string
	if s != nil && s.envWorkspaceSettings != nil {
		settings, found, err := s.envWorkspaceSettings.GetWorkspaceSettings(accountScopeID, workspaceID)
		if err == nil && found {
			defaultTestEnvID = strings.TrimSpace(settings.DefaultTestEnvironmentID)
			defaultConnID = strings.TrimSpace(settings.DefaultConnectionID)
			result.DefaultTestEnvironmentID = defaultTestEnvID
			result.DefaultConnectionID = defaultConnID
		}
	}

	// 2. Resolve default test environment definition and connection precedence
	if defaultTestEnvID != "" {
		if s != nil && s.envDefinitions != nil {
			env, found, err := s.envDefinitions.Get(accountScopeID, workspaceID, defaultTestEnvID)
			if err != nil {
				result.FallbackReason = fmt.Sprintf("failed to load default test environment %q: %v", defaultTestEnvID, err)
			} else if !found {
				result.FallbackReason = fmt.Sprintf("configured default test environment %q not found or deleted", defaultTestEnvID)
			} else {
				result.DefaultTestEnvironment = &env
				// Resolve connection using 3-tier precedence:
				// Tier 1: Explicit (none in prompt context)
				// Tier 2: env.PreferredConnectionID
				// Tier 3: settings.DefaultConnectionID
				if s.envDeployments != nil {
					conn, err := s.envDeployments.ResolveConnection(ctx, accountScopeID, workspaceID, "", &env)
					if err == nil && conn != nil {
						result.ResolvedConnection = conn
						prefID := strings.TrimSpace(env.PreferredConnectionID)
						if prefID != "" && conn.ID == prefID {
							result.ConnectionTier = 2
							result.ConnectionResolutionNote = "environment preferred connection"
						} else if defaultConnID != "" && conn.ID == defaultConnID {
							result.ConnectionTier = 3
							result.ConnectionResolutionNote = "workspace default connection"
						} else {
							result.ConnectionTier = 1
							result.ConnectionResolutionNote = "resolved connection"
						}
					} else {
						result.ConnectionTier = 0
						if prefID := strings.TrimSpace(env.PreferredConnectionID); prefID != "" {
							result.ConnectionResolutionNote = fmt.Sprintf("preferred connection %q not found", prefID)
						} else if defaultConnID != "" {
							result.ConnectionResolutionNote = fmt.Sprintf("default connection %q not found", defaultConnID)
						} else {
							result.ConnectionResolutionNote = "no connection configured"
						}
					}
				} else if s.envConnections != nil {
					// Fallback when deploymentManager is not wired
					prefID := strings.TrimSpace(env.PreferredConnectionID)
					if prefID != "" {
						conn, found, _ := s.envConnections.Get(accountScopeID, workspaceID, prefID)
						if found {
							result.ResolvedConnection = &conn
							result.ConnectionTier = 2
							result.ConnectionResolutionNote = "environment preferred connection"
						} else {
							result.ConnectionResolutionNote = fmt.Sprintf("preferred connection %q not found", prefID)
						}
					} else if defaultConnID != "" {
						conn, found, _ := s.envConnections.Get(accountScopeID, workspaceID, defaultConnID)
						if found {
							result.ResolvedConnection = &conn
							result.ConnectionTier = 3
							result.ConnectionResolutionNote = "workspace default connection"
						} else {
							result.ConnectionResolutionNote = fmt.Sprintf("default connection %q not found", defaultConnID)
						}
					} else {
						result.ConnectionResolutionNote = "no connection configured"
					}
				}
			}
		} else {
			result.FallbackReason = "environment definitions store not configured"
		}
	} else {
		result.FallbackReason = "no default test environment configured"
	}

	// 3. Resolve available environments (bounded to 10)
	if s != nil && s.envDefinitions != nil {
		envs, err := s.envDefinitions.List(accountScopeID, workspaceID, 10)
		if err == nil {
			result.AvailableEnvironments = envs
		}
	}

	// 4. Resolve active deployments and leases (bounded to 10)
	if s != nil && s.envDeployments != nil {
		deps, err := s.envDeployments.ListDeployments(accountScopeID, workspaceID, 10)
		if err == nil {
			for _, dep := range deps {
				activeLease, hasLease, _ := s.envDeployments.GetActiveLease(accountScopeID, workspaceID, dep.ID)
				isLeased := hasLease && activeLease.Active
				if dep.Status == environments.DeploymentStatusRunning || dep.Status == environments.DeploymentStatusReady || isLeased {
					summary := ActiveDeploymentSummary{
						DeploymentID:  dep.ID,
						EnvironmentID: dep.EnvironmentID,
						Status:        string(dep.Status),
					}
					if isLeased {
						summary.LeaseID = activeLease.ID
						summary.ConsumerType = string(activeLease.ConsumerType)
						summary.ConsumerID = activeLease.ConsumerID
					}
					result.ActiveDeployments = append(result.ActiveDeployments, summary)
				}
			}
		}
	}

	return result, nil
}

// FormatWorkspaceEnvironmentPromptBlock formats the resolved environment context into a concise,
// human- and model-readable prompt block bounded by MaxWorkspaceEnvironmentPromptBytes.
func FormatWorkspaceEnvironmentPromptBlock(envCtx *WorkspaceEnvironmentContext) string {
	if envCtx == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("Workspace test environments:\n")

	// 1. Default test environment
	if envCtx.DefaultTestEnvironment != nil {
		env := envCtx.DefaultTestEnvironment
		connInfo := "none resolved"
		if envCtx.ResolvedConnection != nil {
			conn := envCtx.ResolvedConnection
			note := ""
			if envCtx.ConnectionResolutionNote != "" {
				note = ", " + envCtx.ConnectionResolutionNote
			}
			connInfo = fmt.Sprintf("%s (%s, %s%s)", conn.ID, conn.Name, conn.Kind, note)
		} else if envCtx.ConnectionResolutionNote != "" {
			connInfo = envCtx.ConnectionResolutionNote
		}
		sb.WriteString(fmt.Sprintf("- default_test_environment: %s (%s, role: %s, image: %s, connection: %s)\n",
			env.ID, env.Name, env.Role, env.Container.Image, connInfo))
	} else if envCtx.FallbackReason != "" {
		sb.WriteString(fmt.Sprintf("- default_test_environment: none (%s)\n", envCtx.FallbackReason))
	} else {
		sb.WriteString("- default_test_environment: none configured\n")
	}

	// 2. Workspace default connection when no default test environment is configured
	if envCtx.DefaultTestEnvironment == nil && envCtx.DefaultConnectionID != "" {
		sb.WriteString(fmt.Sprintf("- workspace_default_connection: %s\n", envCtx.DefaultConnectionID))
	}

	// 3. Available environments (bounded list)
	if len(envCtx.AvailableEnvironments) == 0 {
		sb.WriteString("- available_environments: none\n")
	} else {
		const maxDisplayEnvs = 5
		parts := make([]string, 0, len(envCtx.AvailableEnvironments))
		for i, env := range envCtx.AvailableEnvironments {
			if i >= maxDisplayEnvs {
				remaining := len(envCtx.AvailableEnvironments) - maxDisplayEnvs
				parts = append(parts, fmt.Sprintf("... and %d more", remaining))
				break
			}
			parts = append(parts, fmt.Sprintf("%s (%s, %s)", env.ID, env.Name, env.Role))
		}
		sb.WriteString(fmt.Sprintf("- available_environments: %s\n", strings.Join(parts, ", ")))
	}

	// 4. Active deployments (bounded list)
	if len(envCtx.ActiveDeployments) == 0 {
		sb.WriteString("- active_deployments: none\n")
	} else {
		sb.WriteString("- active_deployments:\n")
		const maxDisplayDeps = 3
		for i, dep := range envCtx.ActiveDeployments {
			if i >= maxDisplayDeps {
				remaining := len(envCtx.ActiveDeployments) - maxDisplayDeps
				sb.WriteString(fmt.Sprintf("  - ... and %d more\n", remaining))
				break
			}
			leaseInfo := "unleased"
			if dep.LeaseID != "" {
				consumer := dep.ConsumerID
				if dep.ConsumerType != "" {
					consumer = dep.ConsumerType + ":" + dep.ConsumerID
				}
				leaseInfo = fmt.Sprintf("leased by %s (lease: %s)", consumer, dep.LeaseID)
			}
			sb.WriteString(fmt.Sprintf("  - %s (env: %s, status: %s, %s)\n", dep.DeploymentID, dep.EnvironmentID, dep.Status, leaseInfo))
		}
	}

	// 5. Agent test orchestration guidance
	if envCtx.DefaultTestEnvironment != nil {
		sb.WriteString(fmt.Sprintf("- test_guidance: Inspect selected environment definition and worktree via `manage_environments` action=\"get\" (environment_id=%q). When executing tests, acquire deployment lease via `manage_environments` action=\"ensure\", run commands via `manage_environments` action=\"exec\" using returned receipts without busy polling, and release with `manage_environments` action=\"release\" when done.\n", envCtx.DefaultTestEnvironment.ID))
	} else {
		sb.WriteString("- test_guidance: When tests require a container/remote testbench, inspect available environments via `manage_environments` action=\"list\" or specify environment_id with `manage_environments` action=\"ensure\". Use returned receipts without busy polling, and release with action=\"release\" when done.\n")
	}

	content := strings.TrimSpace(sb.String())
	if len(content) > MaxWorkspaceEnvironmentPromptBytes {
		content = truncatePromptBytes(content, MaxWorkspaceEnvironmentPromptBytes, "\n- ... (environment context truncated)")
	}
	return content
}

// WorkspaceEnvironmentPromptBlock generates the formatted environment prompt block for the given workspace scope.
func (s *Service) WorkspaceEnvironmentPromptBlock(ctx context.Context, scope tool.WorkspaceScope) string {
	if s == nil {
		return ""
	}
	accountScopeID := strings.TrimSpace(scope.Principal.AccountScopeID)
	if accountScopeID == "" {
		return ""
	}
	workspaceID := s.resolveWorkspaceIDForScope(scope)
	if workspaceID == "" {
		return ""
	}
	return s.WorkspaceEnvironmentPromptBlockForWorkspace(ctx, accountScopeID, workspaceID)
}

// WorkspaceEnvironmentPromptBlockForWorkspace generates the formatted environment prompt block for the specified workspace.
func (s *Service) WorkspaceEnvironmentPromptBlockForWorkspace(ctx context.Context, accountScopeID, workspaceID string) string {
	if s == nil {
		return ""
	}
	envCtx, err := s.ResolveDefaultTestbench(ctx, accountScopeID, workspaceID)
	if err != nil || envCtx == nil {
		return ""
	}
	return FormatWorkspaceEnvironmentPromptBlock(envCtx)
}

func (s *Service) appendWorkspaceEnvironmentPromptBlock(base string, scope tool.WorkspaceScope) string {
	if s == nil || (s.envDefinitions == nil && s.envWorkspaceSettings == nil) {
		return strings.TrimSpace(base)
	}
	block := s.WorkspaceEnvironmentPromptBlock(context.Background(), scope)
	if block == "" {
		return strings.TrimSpace(base)
	}
	base = strings.TrimSpace(base)
	if base == "" {
		return block
	}
	return base + "\n\n" + block
}

func (s *Service) resolveWorkspaceIDForScope(scope tool.WorkspaceScope) string {
	if s == nil {
		return ""
	}
	if s.workspace != nil {
		targetPaths := make([]string, 0, 2+len(scope.Roots))
		if p := strings.TrimSpace(scope.SourceWorkspacePath); p != "" {
			targetPaths = append(targetPaths, p)
		}
		if p := strings.TrimSpace(scope.PrimaryPath); p != "" {
			targetPaths = append(targetPaths, p)
		}
		for _, r := range scope.Roots {
			if p := strings.TrimSpace(r); p != "" {
				targetPaths = append(targetPaths, p)
			}
		}
		for _, p := range targetPaths {
			wsScope, err := s.workspace.ScopeForPathForPrincipal(scope.Principal, p)
			if err == nil && wsScope.Matched && strings.TrimSpace(wsScope.WorkspaceID) != "" {
				return wsScope.WorkspaceID
			}
		}
	}
	// Fallback for direct testing when workspace service is nil
	if p := strings.TrimSpace(scope.SourceWorkspacePath); p != "" {
		return p
	}
	if p := strings.TrimSpace(scope.PrimaryPath); p != "" {
		return p
	}
	if len(scope.Roots) > 0 && strings.TrimSpace(scope.Roots[0]) != "" {
		return strings.TrimSpace(scope.Roots[0])
	}
	return ""
}
