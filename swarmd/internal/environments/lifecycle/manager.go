package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/provider"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

var (
	// ErrConnectionNotFound indicates that the requested connection ID was not found.
	ErrConnectionNotFound = errors.New("connection not found")

	// ErrEnvironmentNotFound indicates that the requested environment ID was not found.
	ErrEnvironmentNotFound = errors.New("environment not found")

	// ErrDeploymentNotFound indicates that the requested deployment ID was not found.
	ErrDeploymentNotFound = errors.New("deployment not found")

	// ErrLeaseNotFound indicates that the requested deployment lease ID was not found.
	ErrLeaseNotFound = errors.New("deployment lease not found")

	// ErrLeaseAlreadyReleased indicates that the lease has already been released.
	ErrLeaseAlreadyReleased = errors.New("deployment lease already released")

	// ErrNoConnectionResolved indicates that 3-tier resolution found no connection.
	ErrNoConnectionResolved = errors.New("no connection resolved: neither explicit connection_id, environment preferred_connection_id, nor workspace default_connection_id is configured")

	// ErrProviderNotRegistered indicates that no provider is registered for the connection kind.
	ErrProviderNotRegistered = errors.New("no provider registered for connection kind")

	// ErrMaxInstancesReached indicates that the environment capacity limit has been reached.
	ErrMaxInstancesReached = errors.New("maximum deployment instances reached for environment")

	// ErrDeploymentUnusable indicates that the deployment is not in a usable state.
	ErrDeploymentUnusable = errors.New("deployment is unusable")

	// ErrDeploymentLeaseHeld indicates that the deployment has an active unexpired lease held by another consumer.
	ErrDeploymentLeaseHeld = pebblestore.ErrDeploymentLeaseHeld
)

// ConnectionReader provides read access to Connection records.
type ConnectionReader interface {
	Get(accountScopeID, workspaceID, connectionID string) (environments.Connection, bool, error)
	List(accountScopeID, workspaceID string, limit int) ([]environments.Connection, error)
}

// EnvironmentReader provides read access to Environment records.
type EnvironmentReader interface {
	Get(accountScopeID, workspaceID, environmentID string) (environments.Environment, bool, error)
	List(accountScopeID, workspaceID string, limit int) ([]environments.Environment, error)
}

// DeploymentManagerStore abstracts Deployment and Lease storage operations.
type DeploymentManagerStore interface {
	Get(accountScopeID, workspaceID, deploymentID string) (environments.Deployment, bool, error)
	List(accountScopeID, workspaceID string, limit int) ([]environments.Deployment, error)
	ListByEnvironment(accountScopeID, workspaceID, environmentID string, limit int) ([]environments.Deployment, error)
	Save(dep environments.Deployment) (environments.Deployment, error)
	UpdateStatus(accountScopeID, workspaceID, deploymentID string, status environments.DeploymentStatus, health environments.HealthStatus, errMsg string) (environments.Deployment, error)
	UpdateRuntime(accountScopeID, workspaceID, deploymentID string, runtime environments.RuntimeMetadata) (environments.Deployment, error)
	Delete(accountScopeID, workspaceID, deploymentID string) (bool, error)
	AcquireLease(lease environments.DeploymentLease) (environments.DeploymentLease, error)
	ReleaseLease(accountScopeID, workspaceID, leaseID string, reason string) (environments.DeploymentLease, error)
	GetActiveLease(accountScopeID, workspaceID, deploymentID string) (environments.DeploymentLease, bool, error)
	Leases() *pebblestore.LeaseStore
}

// WorkspaceSettingsReader retrieves workspace-level settings including default connection.
type WorkspaceSettingsReader interface {
	GetWorkspaceSettings(accountScopeID, workspaceID string) (environments.WorkspaceSettings, bool, error)
}

// EnsureDeploymentRequest specifies parameters for acquiring or provisioning an environment deployment.
type EnsureDeploymentRequest struct {
	AccountScopeID   string                    `json:"account_scope_id"`
	WorkspaceID      string                    `json:"workspace_id"`
	EnvironmentID    string                    `json:"environment_id"`
	ConnectionID     string                    `json:"connection_id,omitempty"` // Tier 1 explicit connection override
	ConsumerType     environments.ConsumerType `json:"consumer_type"`          // session, test_run, worker, custom
	ConsumerID       string                    `json:"consumer_id"`
	ConsumerMetadata map[string]string         `json:"consumer_metadata,omitempty"`
	DeploymentName   string                    `json:"deployment_name,omitempty"`
	WorkspacePath    string                    `json:"workspace_path,omitempty"` // Host workspace path for local_mount
	EnvOverrides     map[string]string         `json:"env_overrides,omitempty"`
	TTLMillis        int64                     `json:"ttl_millis,omitempty"` // 0 = held until explicitly released
}

// Validate checks that required fields on EnsureDeploymentRequest are provided.
func (r *EnsureDeploymentRequest) Validate() error {
	r.AccountScopeID = strings.TrimSpace(r.AccountScopeID)
	if r.AccountScopeID == "" {
		return errors.New("account_scope_id is required")
	}
	r.WorkspaceID = strings.TrimSpace(r.WorkspaceID)
	if r.WorkspaceID == "" {
		return errors.New("workspace_id is required")
	}
	r.EnvironmentID = strings.TrimSpace(r.EnvironmentID)
	if r.EnvironmentID == "" {
		return errors.New("environment_id is required")
	}
	r.ConsumerID = strings.TrimSpace(r.ConsumerID)
	if r.ConsumerID == "" {
		return errors.New("consumer_id is required")
	}
	switch r.ConsumerType {
	case environments.ConsumerTypeSession, environments.ConsumerTypeTestRun, environments.ConsumerTypeWorker, environments.ConsumerTypeCustom:
		// valid
	default:
		return fmt.Errorf("unsupported consumer type: %q", r.ConsumerType)
	}
	return nil
}

// EnsureDeploymentResult contains the deployment, active lease, and access metadata.
type EnsureDeploymentResult struct {
	Deployment environments.Deployment      `json:"deployment"`
	Lease      environments.DeploymentLease `json:"lease"`
	Reused     bool                         `json:"reused"`
	Access     *provider.DeploymentAccess   `json:"access,omitempty"`
}

// ReleaseDeploymentRequest specifies parameters for releasing a held lease.
type ReleaseDeploymentRequest struct {
	AccountScopeID string `json:"account_scope_id"`
	WorkspaceID    string `json:"workspace_id"`
	LeaseID        string `json:"lease_id"`
	Reason         string `json:"reason,omitempty"`
}

// ReleaseDeploymentResult contains the release outcome and action executed on the container.
type ReleaseDeploymentResult struct {
	Lease           environments.DeploymentLease `json:"lease"`
	Deployment      environments.Deployment      `json:"deployment"`
	ReleaseBehavior environments.ReleaseBehavior `json:"release_behavior"`
	ActionTaken     string                       `json:"action_taken"` // "retained", "restarted", "recreated"
}

// DestroyDeploymentRequest specifies parameters for forcibly terminating a deployment.
type DestroyDeploymentRequest struct {
	AccountScopeID string `json:"account_scope_id"`
	WorkspaceID    string `json:"workspace_id"`
	DeploymentID   string `json:"deployment_id"`
	Reason         string `json:"reason,omitempty"`
}

// DeployDeploymentRequest specifies parameters for directly provisioning a new deployment.
type DeployDeploymentRequest struct {
	AccountScopeID   string                    `json:"account_scope_id"`
	WorkspaceID      string                    `json:"workspace_id"`
	EnvironmentID    string                    `json:"environment_id"`
	ConnectionID     string                    `json:"connection_id,omitempty"` // Tier 1 explicit connection override
	DeploymentName   string                    `json:"deployment_name,omitempty"`
	WorkspacePath    string                    `json:"workspace_path,omitempty"` // Host workspace path for local_mount
	EnvOverrides     map[string]string         `json:"env_overrides,omitempty"`
	ConsumerType     environments.ConsumerType `json:"consumer_type,omitempty"` // optional lease acquisition
	ConsumerID       string                    `json:"consumer_id,omitempty"`
	ConsumerMetadata map[string]string         `json:"consumer_metadata,omitempty"`
	TTLMillis        int64                     `json:"ttl_millis,omitempty"`
}

// DeployDeploymentResult contains the newly provisioned deployment, optional lease, and access info.
type DeployDeploymentResult struct {
	Deployment environments.Deployment       `json:"deployment"`
	Lease      *environments.DeploymentLease `json:"lease,omitempty"`
	Access     *provider.DeploymentAccess    `json:"access,omitempty"`
}

// DeploymentManager coordinates deployment lifecycle, connection resolution, leasing, reuse pooling,
// limit enforcement, and release behavior.
type DeploymentManager struct {
	connections  ConnectionReader
	environments EnvironmentReader
	deployments  DeploymentManagerStore
	workspaces   WorkspaceSettingsReader
	registry     *provider.Registry

	mu       sync.Mutex
	envLocks map[string]*sync.Mutex
}

// NewDeploymentManager creates a new DeploymentManager instance.
func NewDeploymentManager(
	connections ConnectionReader,
	environments EnvironmentReader,
	deployments DeploymentManagerStore,
	workspaces WorkspaceSettingsReader,
	registry *provider.Registry,
) *DeploymentManager {
	return &DeploymentManager{
		connections:  connections,
		environments: environments,
		deployments:  deployments,
		workspaces:   workspaces,
		registry:     registry,
		envLocks:     make(map[string]*sync.Mutex),
	}
}

// getEnvLock retrieves or initializes a per-environment mutex to serialize provisioning and capacity checks.
func (m *DeploymentManager) getEnvLock(accountScopeID, workspaceID, environmentID string) *sync.Mutex {
	key := accountScopeID + "/" + workspaceID + "/" + environmentID
	m.mu.Lock()
	defer m.mu.Unlock()
	lock, exists := m.envLocks[key]
	if !exists {
		lock = &sync.Mutex{}
		m.envLocks[key] = lock
	}
	return lock
}

// ResolveConnection resolves a target Connection using 3-tier precedence:
// Tier 1: Explicitly supplied connection_id in request.
// Tier 2: Environment preferred_connection_id.
// Tier 3: Workspace default_connection_id from WorkspaceSettings.
func (m *DeploymentManager) ResolveConnection(
	ctx context.Context,
	accountScopeID, workspaceID string,
	explicitConnectionID string,
	env *environments.Environment,
) (*environments.Connection, error) {
	if m == nil || m.connections == nil {
		return nil, errors.New("connection store is not configured")
	}

	accountScopeID = strings.TrimSpace(accountScopeID)
	workspaceID = strings.TrimSpace(workspaceID)
	if accountScopeID == "" || workspaceID == "" {
		return nil, errors.New("account scope id and workspace id are required")
	}

	// Tier 1: Explicit connection ID
	explicitConnectionID = strings.TrimSpace(explicitConnectionID)
	if explicitConnectionID != "" {
		conn, found, err := m.connections.Get(accountScopeID, workspaceID, explicitConnectionID)
		if err != nil {
			return nil, fmt.Errorf("lookup explicit connection %q: %w", explicitConnectionID, err)
		}
		if !found {
			return nil, fmt.Errorf("explicit connection %q: %w", explicitConnectionID, ErrConnectionNotFound)
		}
		return &conn, nil
	}

	// Tier 2: Environment preferred connection ID
	if env != nil && strings.TrimSpace(env.PreferredConnectionID) != "" {
		prefID := strings.TrimSpace(env.PreferredConnectionID)
		conn, found, err := m.connections.Get(accountScopeID, workspaceID, prefID)
		if err != nil {
			return nil, fmt.Errorf("lookup environment preferred connection %q: %w", prefID, err)
		}
		if !found {
			return nil, fmt.Errorf("environment preferred connection %q: %w", prefID, ErrConnectionNotFound)
		}
		return &conn, nil
	}

	// Tier 3: Workspace default connection ID
	if m.workspaces != nil {
		settings, found, err := m.workspaces.GetWorkspaceSettings(accountScopeID, workspaceID)
		if err == nil && found && strings.TrimSpace(settings.DefaultConnectionID) != "" {
			defaultID := strings.TrimSpace(settings.DefaultConnectionID)
			conn, foundConn, err := m.connections.Get(accountScopeID, workspaceID, defaultID)
			if err != nil {
				return nil, fmt.Errorf("lookup workspace default connection %q: %w", defaultID, err)
			}
			if !foundConn {
				return nil, fmt.Errorf("workspace default connection %q: %w", defaultID, ErrConnectionNotFound)
			}
			return &conn, nil
		}
	}

	return nil, ErrNoConnectionResolved
}

// EnsureDeployment atomically acquires an existing compatible unleased deployment if reuse is enabled,
// or provisions a new deployment on the resolved connection within max_instances limits.
// Returns the active Deployment and DeploymentLease.
func (m *DeploymentManager) EnsureDeployment(ctx context.Context, req EnsureDeploymentRequest) (*EnsureDeploymentResult, error) {
	if m == nil || m.deployments == nil || m.environments == nil || m.connections == nil {
		return nil, errors.New("deployment manager is not fully configured")
	}

	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("invalid ensure deployment request: %w", err)
	}

	// 1. Fetch Environment definition
	env, found, err := m.environments.Get(req.AccountScopeID, req.WorkspaceID, req.EnvironmentID)
	if err != nil {
		return nil, fmt.Errorf("get environment %q: %w", req.EnvironmentID, err)
	}
	if !found {
		return nil, fmt.Errorf("environment %q: %w", req.EnvironmentID, ErrEnvironmentNotFound)
	}

	// 2. Resolve Connection using 3-tier precedence
	conn, err := m.ResolveConnection(ctx, req.AccountScopeID, req.WorkspaceID, req.ConnectionID, &env)
	if err != nil {
		return nil, fmt.Errorf("resolve connection for environment %q: %w", req.EnvironmentID, err)
	}

	// 3. Verify provider registration
	if m.registry == nil {
		return nil, errors.New("provider registry is not configured")
	}
	prov, ok := m.registry.Get(conn.Kind)
	if !ok {
		return nil, fmt.Errorf("provider for connection kind %q: %w", conn.Kind, ErrProviderNotRegistered)
	}

	// Check Docker support capability if container definition specifies an image
	if env.Container.Image != "" && !conn.Capabilities.SupportsDocker {
		return nil, fmt.Errorf("connection %q does not support Docker required by environment %q", conn.ID, env.ID)
	}

	// 4. Synchronize across concurrent calls for this environment
	envLock := m.getEnvLock(req.AccountScopeID, req.WorkspaceID, req.EnvironmentID)
	envLock.Lock()
	defer envLock.Unlock()

	maxInstances := env.DeploymentPolicy.MaxInstances
	if maxInstances <= 0 {
		maxInstances = 1
	}

	// Fetch existing deployments for this environment
	existingDeps, err := m.deployments.ListByEnvironment(req.AccountScopeID, req.WorkspaceID, req.EnvironmentID, 1000)
	if err != nil {
		return nil, fmt.Errorf("list deployments for environment %q: %w", req.EnvironmentID, err)
	}

	now := time.Now().UnixMilli()

	// 5. Attempt reuse if enabled by policy
	if env.DeploymentPolicy.Reuse {
		for _, dep := range existingDeps {
			// Must match the resolved connection
			if dep.ConnectionID != conn.ID {
				continue
			}
			// Must be in a usable (running/ready) state and healthy
			if !dep.IsUsable() {
				continue
			}

			// Check if deployment is currently leased
			activeLease, hasActive, err := m.deployments.GetActiveLease(req.AccountScopeID, req.WorkspaceID, dep.ID)
			if err != nil {
				continue
			}
			if hasActive && activeLease.Active && !activeLease.IsExpired(now) {
				// Held by another consumer
				continue
			}

			// Attempt atomic lease acquisition
			leaseID := "lease_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			var expiresAt int64
			if req.TTLMillis > 0 {
				expiresAt = now + req.TTLMillis
			}
			leaseToAcquire := environments.DeploymentLease{
				ID:               leaseID,
				AccountScopeID:   req.AccountScopeID,
				WorkspaceID:      req.WorkspaceID,
				DeploymentID:     dep.ID,
				EnvironmentID:    req.EnvironmentID,
				ConsumerType:     req.ConsumerType,
				ConsumerID:       req.ConsumerID,
				ConsumerMetadata: req.ConsumerMetadata,
				AcquiredAt:       now,
				ExpiresAt:        expiresAt,
				Active:           true,
			}

			acquiredLease, err := m.deployments.AcquireLease(leaseToAcquire)
			if err != nil {
				if errors.Is(err, pebblestore.ErrDeploymentLeaseHeld) {
					// Raced with another consumer, check next candidate
					continue
				}
				return nil, fmt.Errorf("acquire lease on reused deployment %q: %w", dep.ID, err)
			}

			// Transition deployment status to busy
			updatedDep, err := m.deployments.UpdateStatus(req.AccountScopeID, req.WorkspaceID, dep.ID, environments.DeploymentStatusBusy, dep.Health, "")
			if err != nil {
				updatedDep = dep
				updatedDep.Status = environments.DeploymentStatusBusy
			}

			// Resolve runtime access
			access, _ := prov.ResolveAccess(ctx, conn, &updatedDep)

			return &EnsureDeploymentResult{
				Deployment: updatedDep,
				Lease:      acquiredLease,
				Reused:     true,
				Access:     access,
			}, nil
		}
	}

	// 6. Check max_instances capacity before provisioning new deployment
	activeCount := 0
	for _, dep := range existingDeps {
		if dep.IsActive() {
			activeCount++
		}
	}

	if activeCount >= maxInstances {
		return nil, fmt.Errorf("environment %q reached max_instances capacity (%d/%d active deployments): %w",
			req.EnvironmentID, activeCount, maxInstances, ErrMaxInstancesReached)
	}

	// 7. Provision a new deployment
	depID := "dep_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	depName := strings.TrimSpace(req.DeploymentName)
	if depName == "" {
		depName = fmt.Sprintf("%s-%s", env.Name, depID[4:12])
	}

	newDep := environments.Deployment{
		ID:             depID,
		AccountScopeID: req.AccountScopeID,
		WorkspaceID:    req.WorkspaceID,
		EnvironmentID:  req.EnvironmentID,
		ConnectionID:   conn.ID,
		Name:           depName,
		Status:         environments.DeploymentStatusProvisioning,
		Health:         environments.HealthStatusStarting,
		Lifecycle: environments.DeploymentLifecycle{
			CreatedAt: now,
			StartedAt: now,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	// Save initial provisioning record
	savedDep, err := m.deployments.Save(newDep)
	if err != nil {
		return nil, fmt.Errorf("save initial deployment record: %w", err)
	}

	// Prepare deploy request
	dReq := provider.DeployRequest{
		Connection:    conn,
		Environment:   &env,
		Deployment:    &savedDep,
		WorkspacePath: req.WorkspacePath,
		EnvOverrides:  req.EnvOverrides,
	}

	deployRes, err := prov.Deploy(ctx, dReq)
	if err != nil {
		_, _ = m.deployments.UpdateStatus(req.AccountScopeID, req.WorkspaceID, depID, environments.DeploymentStatusFailed, environments.HealthStatusUnhealthy, err.Error())
		return nil, fmt.Errorf("deploy environment on host: %w", err)
	}

	// Update deployment with runtime attributes
	savedDep.Runtime = deployRes.Runtime
	savedDep.Health = deployRes.Health
	if savedDep.Health == "" {
		savedDep.Health = environments.HealthStatusHealthy
	}
	savedDep.Status = environments.DeploymentStatusBusy

	if _, err := m.deployments.UpdateRuntime(req.AccountScopeID, req.WorkspaceID, depID, savedDep.Runtime); err != nil {
		// Log or retain error
	}
	savedDep, _ = m.deployments.UpdateStatus(req.AccountScopeID, req.WorkspaceID, depID, environments.DeploymentStatusBusy, savedDep.Health, "")

	// Acquire lease for the consumer
	leaseID := "lease_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	var expiresAt int64
	if req.TTLMillis > 0 {
		expiresAt = now + req.TTLMillis
	}
	leaseToAcquire := environments.DeploymentLease{
		ID:               leaseID,
		AccountScopeID:   req.AccountScopeID,
		WorkspaceID:      req.WorkspaceID,
		DeploymentID:     savedDep.ID,
		EnvironmentID:    req.EnvironmentID,
		ConsumerType:     req.ConsumerType,
		ConsumerID:       req.ConsumerID,
		ConsumerMetadata: req.ConsumerMetadata,
		AcquiredAt:       now,
		ExpiresAt:        expiresAt,
		Active:           true,
	}

	acquiredLease, err := m.deployments.AcquireLease(leaseToAcquire)
	if err != nil {
		// Clean up container if lease fails
		_ = prov.Destroy(ctx, conn, &savedDep)
		_, _ = m.deployments.UpdateStatus(req.AccountScopeID, req.WorkspaceID, depID, environments.DeploymentStatusFailed, environments.HealthStatusUnhealthy, "lease acquisition failed: "+err.Error())
		return nil, fmt.Errorf("acquire lease on new deployment: %w", err)
	}

	// Resolve access metadata
	access, _ := prov.ResolveAccess(ctx, conn, &savedDep)

	return &EnsureDeploymentResult{
		Deployment: savedDep,
		Lease:      acquiredLease,
		Reused:     false,
		Access:     access,
	}, nil
}

// DeployDeployment directly provisions a new deployment for an environment within max_instances limits,
// optionally acquiring a lease if a consumer is provided.
func (m *DeploymentManager) DeployDeployment(ctx context.Context, req DeployDeploymentRequest) (*DeployDeploymentResult, error) {
	if m == nil || m.deployments == nil || m.environments == nil || m.connections == nil {
		return nil, errors.New("deployment manager is not fully configured")
	}

	req.AccountScopeID = strings.TrimSpace(req.AccountScopeID)
	req.WorkspaceID = strings.TrimSpace(req.WorkspaceID)
	req.EnvironmentID = strings.TrimSpace(req.EnvironmentID)
	if req.AccountScopeID == "" || req.WorkspaceID == "" || req.EnvironmentID == "" {
		return nil, errors.New("account scope id, workspace id, and environment id are required")
	}

	// 1. Fetch Environment definition
	env, found, err := m.environments.Get(req.AccountScopeID, req.WorkspaceID, req.EnvironmentID)
	if err != nil {
		return nil, fmt.Errorf("get environment %q: %w", req.EnvironmentID, err)
	}
	if !found {
		return nil, fmt.Errorf("environment %q: %w", req.EnvironmentID, ErrEnvironmentNotFound)
	}

	// 2. Resolve Connection using 3-tier precedence
	conn, err := m.ResolveConnection(ctx, req.AccountScopeID, req.WorkspaceID, req.ConnectionID, &env)
	if err != nil {
		return nil, fmt.Errorf("resolve connection for environment %q: %w", req.EnvironmentID, err)
	}

	// 3. Verify provider registration
	if m.registry == nil {
		return nil, errors.New("provider registry is not configured")
	}
	prov, ok := m.registry.Get(conn.Kind)
	if !ok {
		return nil, fmt.Errorf("provider for connection kind %q: %w", conn.Kind, ErrProviderNotRegistered)
	}

	if env.Container.Image != "" && !conn.Capabilities.SupportsDocker {
		return nil, fmt.Errorf("connection %q does not support Docker required by environment %q", conn.ID, env.ID)
	}

	// 4. Synchronize across concurrent calls for this environment
	envLock := m.getEnvLock(req.AccountScopeID, req.WorkspaceID, req.EnvironmentID)
	envLock.Lock()
	defer envLock.Unlock()

	maxInstances := env.DeploymentPolicy.MaxInstances
	if maxInstances <= 0 {
		maxInstances = 1
	}

	existingDeps, err := m.deployments.ListByEnvironment(req.AccountScopeID, req.WorkspaceID, req.EnvironmentID, 1000)
	if err != nil {
		return nil, fmt.Errorf("list deployments for environment %q: %w", req.EnvironmentID, err)
	}

	activeCount := 0
	for _, dep := range existingDeps {
		if dep.IsActive() {
			activeCount++
		}
	}

	if activeCount >= maxInstances {
		return nil, fmt.Errorf("environment %q reached max_instances capacity (%d/%d active deployments): %w",
			req.EnvironmentID, activeCount, maxInstances, ErrMaxInstancesReached)
	}

	now := time.Now().UnixMilli()
	depID := "dep_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	depName := strings.TrimSpace(req.DeploymentName)
	if depName == "" {
		depName = fmt.Sprintf("%s-%s", env.Name, depID[4:12])
	}

	newDep := environments.Deployment{
		ID:             depID,
		AccountScopeID: req.AccountScopeID,
		WorkspaceID:    req.WorkspaceID,
		EnvironmentID:  req.EnvironmentID,
		ConnectionID:   conn.ID,
		Name:           depName,
		Status:         environments.DeploymentStatusProvisioning,
		Health:         environments.HealthStatusStarting,
		Lifecycle: environments.DeploymentLifecycle{
			CreatedAt: now,
			StartedAt: now,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	savedDep, err := m.deployments.Save(newDep)
	if err != nil {
		return nil, fmt.Errorf("save initial deployment record: %w", err)
	}

	dReq := provider.DeployRequest{
		Connection:    conn,
		Environment:   &env,
		Deployment:    &savedDep,
		WorkspacePath: req.WorkspacePath,
		EnvOverrides:  req.EnvOverrides,
	}

	deployRes, err := prov.Deploy(ctx, dReq)
	if err != nil {
		_, _ = m.deployments.UpdateStatus(req.AccountScopeID, req.WorkspaceID, depID, environments.DeploymentStatusFailed, environments.HealthStatusUnhealthy, err.Error())
		return nil, fmt.Errorf("deploy environment on host: %w", err)
	}

	savedDep.Runtime = deployRes.Runtime
	savedDep.Health = deployRes.Health
	if savedDep.Health == "" {
		savedDep.Health = environments.HealthStatusHealthy
	}

	initialStatus := environments.DeploymentStatusReady
	var acquiredLease *environments.DeploymentLease

	req.ConsumerID = strings.TrimSpace(req.ConsumerID)
	if req.ConsumerID != "" {
		initialStatus = environments.DeploymentStatusBusy
		leaseID := "lease_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		var expiresAt int64
		if req.TTLMillis > 0 {
			expiresAt = now + req.TTLMillis
		}
		cType := req.ConsumerType
		if cType == "" {
			cType = environments.ConsumerTypeSession
		}
		leaseToAcquire := environments.DeploymentLease{
			ID:               leaseID,
			AccountScopeID:   req.AccountScopeID,
			WorkspaceID:      req.WorkspaceID,
			DeploymentID:     savedDep.ID,
			EnvironmentID:    req.EnvironmentID,
			ConsumerType:     cType,
			ConsumerID:       req.ConsumerID,
			ConsumerMetadata: req.ConsumerMetadata,
			AcquiredAt:       now,
			ExpiresAt:        expiresAt,
			Active:           true,
		}
		acq, lErr := m.deployments.AcquireLease(leaseToAcquire)
		if lErr != nil {
			_ = prov.Destroy(ctx, conn, &savedDep)
			_, _ = m.deployments.UpdateStatus(req.AccountScopeID, req.WorkspaceID, depID, environments.DeploymentStatusFailed, environments.HealthStatusUnhealthy, "lease acquisition failed: "+lErr.Error())
			return nil, fmt.Errorf("acquire lease on new deployment: %w", lErr)
		}
		acquiredLease = &acq
	}

	savedDep.Status = initialStatus
	if _, err := m.deployments.UpdateRuntime(req.AccountScopeID, req.WorkspaceID, depID, savedDep.Runtime); err != nil {
		// Retain error
	}
	savedDep, _ = m.deployments.UpdateStatus(req.AccountScopeID, req.WorkspaceID, depID, initialStatus, savedDep.Health, "")

	access, _ := prov.ResolveAccess(ctx, conn, &savedDep)

	return &DeployDeploymentResult{
		Deployment: savedDep,
		Lease:      acquiredLease,
		Access:     access,
	}, nil
}

// ReleaseDeployment releases an active deployment lease separately from destroying the deployment,
// executing the environment's configured release_behavior:
// - none: Keep container running as-is for immediate reuse; status transitions to ready.
// - restart: Restart container for clean process state; status transitions to ready.
// - recreate: Stop and remove container, terminating deployment so a fresh instance is created on next lease.
func (m *DeploymentManager) ReleaseDeployment(ctx context.Context, req ReleaseDeploymentRequest) (*ReleaseDeploymentResult, error) {
	if m == nil || m.deployments == nil {
		return nil, errors.New("deployment store is not configured")
	}

	req.AccountScopeID = strings.TrimSpace(req.AccountScopeID)
	req.WorkspaceID = strings.TrimSpace(req.WorkspaceID)
	req.LeaseID = strings.TrimSpace(req.LeaseID)
	if req.AccountScopeID == "" || req.WorkspaceID == "" || req.LeaseID == "" {
		return nil, errors.New("account scope id, workspace id, and lease id are required")
	}

	// 1. Fetch lease record
	lease, found, err := m.deployments.Leases().Get(req.AccountScopeID, req.WorkspaceID, req.LeaseID)
	if err != nil {
		return nil, fmt.Errorf("get lease %q: %w", req.LeaseID, err)
	}
	if !found {
		return nil, fmt.Errorf("lease %q: %w", req.LeaseID, ErrLeaseNotFound)
	}
	if !lease.Active {
		return nil, fmt.Errorf("lease %q: %w", req.LeaseID, ErrLeaseAlreadyReleased)
	}

	// 2. Fetch deployment
	dep, foundDep, err := m.deployments.Get(req.AccountScopeID, req.WorkspaceID, lease.DeploymentID)
	if err != nil {
		return nil, fmt.Errorf("get deployment %q: %w", lease.DeploymentID, err)
	}
	if !foundDep {
		// Release lease anyway even if deployment record is missing
		reason := req.Reason
		if reason == "" {
			reason = "released_orphaned_deployment"
		}
		relLease, _ := m.deployments.ReleaseLease(req.AccountScopeID, req.WorkspaceID, req.LeaseID, reason)
		return &ReleaseDeploymentResult{Lease: relLease}, fmt.Errorf("deployment %q: %w", lease.DeploymentID, ErrDeploymentNotFound)
	}

	// Lock environment mutex while executing release behavior
	envLock := m.getEnvLock(req.AccountScopeID, req.WorkspaceID, dep.EnvironmentID)
	envLock.Lock()
	defer envLock.Unlock()

	// 3. Release lease in store
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "released_by_consumer"
	}
	releasedLease, err := m.deployments.ReleaseLease(req.AccountScopeID, req.WorkspaceID, req.LeaseID, reason)
	if err != nil {
		return nil, fmt.Errorf("release lease in store: %w", err)
	}

	// 4. Determine release behavior from Environment definition
	releaseBehavior := environments.ReleaseBehaviorNone
	if m.environments != nil {
		env, foundEnv, _ := m.environments.Get(req.AccountScopeID, req.WorkspaceID, dep.EnvironmentID)
		if foundEnv && env.DeploymentPolicy.ReleaseBehavior != "" {
			releaseBehavior = env.DeploymentPolicy.ReleaseBehavior
		}
	}

	// 5. Lookup provider and connection
	var prov provider.DeploymentProvider
	var conn environments.Connection
	if m.connections != nil {
		c, foundConn, _ := m.connections.Get(req.AccountScopeID, req.WorkspaceID, dep.ConnectionID)
		if foundConn {
			conn = c
			if m.registry != nil {
				prov, _ = m.registry.Get(conn.Kind)
			}
		}
	}

	// 6. Execute configured release behavior
	actionTaken := ""
	switch releaseBehavior {
	case environments.ReleaseBehaviorRestart:
		actionTaken = "restarted"
		if prov != nil {
			_ = prov.Stop(ctx, &conn, &dep)
			_ = prov.Start(ctx, &conn, &dep)
		}
		dep, _ = m.deployments.UpdateStatus(req.AccountScopeID, req.WorkspaceID, dep.ID, environments.DeploymentStatusReady, environments.HealthStatusHealthy, "")

	case environments.ReleaseBehaviorRecreate:
		actionTaken = "recreated"
		if prov != nil {
			_ = prov.Destroy(ctx, &conn, &dep)
		}
		_, _ = m.deployments.UpdateStatus(req.AccountScopeID, req.WorkspaceID, dep.ID, environments.DeploymentStatusTerminated, environments.HealthStatusUnknown, "released with recreate policy")
		_, _ = m.deployments.Delete(req.AccountScopeID, req.WorkspaceID, dep.ID)
		dep.Status = environments.DeploymentStatusTerminated

	case environments.ReleaseBehaviorNone:
		fallthrough
	default:
		actionTaken = "retained"
		dep, _ = m.deployments.UpdateStatus(req.AccountScopeID, req.WorkspaceID, dep.ID, environments.DeploymentStatusReady, environments.HealthStatusHealthy, "")
	}

	return &ReleaseDeploymentResult{
		Lease:           releasedLease,
		Deployment:      dep,
		ReleaseBehavior: releaseBehavior,
		ActionTaken:     actionTaken,
	}, nil
}

// DestroyDeployment forcibly terminates and removes a deployment and its underlying container,
// releasing any active lease held on it.
func (m *DeploymentManager) DestroyDeployment(ctx context.Context, req DestroyDeploymentRequest) error {
	if m == nil || m.deployments == nil {
		return errors.New("deployment store is not configured")
	}

	req.AccountScopeID = strings.TrimSpace(req.AccountScopeID)
	req.WorkspaceID = strings.TrimSpace(req.WorkspaceID)
	req.DeploymentID = strings.TrimSpace(req.DeploymentID)
	if req.AccountScopeID == "" || req.WorkspaceID == "" || req.DeploymentID == "" {
		return errors.New("account scope id, workspace id, and deployment id are required")
	}

	dep, found, err := m.deployments.Get(req.AccountScopeID, req.WorkspaceID, req.DeploymentID)
	if err != nil {
		return fmt.Errorf("get deployment %q: %w", req.DeploymentID, err)
	}
	if !found {
		return fmt.Errorf("deployment %q: %w", req.DeploymentID, ErrDeploymentNotFound)
	}

	envLock := m.getEnvLock(req.AccountScopeID, req.WorkspaceID, dep.EnvironmentID)
	envLock.Lock()
	defer envLock.Unlock()

	// 1. Release active lease if held
	activeLease, hasActive, err := m.deployments.GetActiveLease(req.AccountScopeID, req.WorkspaceID, req.DeploymentID)
	if err == nil && hasActive && activeLease.Active {
		reason := "deployment_destroyed"
		if req.Reason != "" {
			reason += ": " + req.Reason
		}
		_, _ = m.deployments.ReleaseLease(req.AccountScopeID, req.WorkspaceID, activeLease.ID, reason)
	}

	// 2. Destroy container via provider
	if m.connections != nil && m.registry != nil {
		conn, foundConn, _ := m.connections.Get(req.AccountScopeID, req.WorkspaceID, dep.ConnectionID)
		if foundConn {
			if prov, ok := m.registry.Get(conn.Kind); ok {
				_ = prov.Destroy(ctx, &conn, &dep)
			}
		}
	}

	// 3. Mark terminated and delete from deployment store
	_, _ = m.deployments.UpdateStatus(req.AccountScopeID, req.WorkspaceID, req.DeploymentID, environments.DeploymentStatusTerminated, environments.HealthStatusUnknown, "forcibly destroyed: "+req.Reason)
	_, err = m.deployments.Delete(req.AccountScopeID, req.WorkspaceID, req.DeploymentID)
	if err != nil {
		return fmt.Errorf("delete deployment from store: %w", err)
	}

	return nil
}

// StopDeployment stops a running deployment container.
func (m *DeploymentManager) StopDeployment(ctx context.Context, accountScopeID, workspaceID, deploymentID string) error {
	if m == nil || m.deployments == nil {
		return errors.New("deployment store is not configured")
	}

	accountScopeID = strings.TrimSpace(accountScopeID)
	workspaceID = strings.TrimSpace(workspaceID)
	deploymentID = strings.TrimSpace(deploymentID)
	if accountScopeID == "" || workspaceID == "" || deploymentID == "" {
		return errors.New("account scope id, workspace id, and deployment id are required")
	}

	dep, found, err := m.deployments.Get(accountScopeID, workspaceID, deploymentID)
	if err != nil {
		return fmt.Errorf("get deployment %q: %w", deploymentID, err)
	}
	if !found {
		return fmt.Errorf("deployment %q: %w", deploymentID, ErrDeploymentNotFound)
	}

	if m.connections != nil && m.registry != nil {
		conn, foundConn, _ := m.connections.Get(accountScopeID, workspaceID, dep.ConnectionID)
		if foundConn {
			if prov, ok := m.registry.Get(conn.Kind); ok {
				_ = prov.Stop(ctx, &conn, &dep)
			}
		}
	}

	_, err = m.deployments.UpdateStatus(accountScopeID, workspaceID, deploymentID, environments.DeploymentStatusStopped, environments.HealthStatusUnknown, "")
	if err != nil {
		return fmt.Errorf("update deployment status: %w", err)
	}
	return nil
}

// StartDeployment starts a stopped deployment container.
func (m *DeploymentManager) StartDeployment(ctx context.Context, accountScopeID, workspaceID, deploymentID string) error {
	if m == nil || m.deployments == nil {
		return errors.New("deployment store is not configured")
	}

	accountScopeID = strings.TrimSpace(accountScopeID)
	workspaceID = strings.TrimSpace(workspaceID)
	deploymentID = strings.TrimSpace(deploymentID)
	if accountScopeID == "" || workspaceID == "" || deploymentID == "" {
		return errors.New("account scope id, workspace id, and deployment id are required")
	}

	dep, found, err := m.deployments.Get(accountScopeID, workspaceID, deploymentID)
	if err != nil {
		return fmt.Errorf("get deployment %q: %w", deploymentID, err)
	}
	if !found {
		return fmt.Errorf("deployment %q: %w", deploymentID, ErrDeploymentNotFound)
	}

	if m.connections != nil && m.registry != nil {
		conn, foundConn, _ := m.connections.Get(accountScopeID, workspaceID, dep.ConnectionID)
		if foundConn {
			if prov, ok := m.registry.Get(conn.Kind); ok {
				if err := prov.Start(ctx, &conn, &dep); err != nil {
					return fmt.Errorf("start deployment container %q: %w", deploymentID, err)
				}
			}
		}
	}

	newStatus := environments.DeploymentStatusReady
	if _, hasLease, _ := m.deployments.GetActiveLease(accountScopeID, workspaceID, deploymentID); hasLease {
		newStatus = environments.DeploymentStatusBusy
	}

	_, err = m.deployments.UpdateStatus(accountScopeID, workspaceID, deploymentID, newStatus, environments.HealthStatusHealthy, "")
	if err != nil {
		return fmt.Errorf("update deployment status: %w", err)
	}
	return nil
}

// InspectDeployment queries the provider for live runtime status and health, updating the stored deployment.
func (m *DeploymentManager) InspectDeployment(ctx context.Context, accountScopeID, workspaceID, deploymentID string) (*environments.Deployment, error) {
	if m == nil || m.deployments == nil {
		return nil, errors.New("deployment store is not configured")
	}

	dep, found, err := m.deployments.Get(accountScopeID, workspaceID, deploymentID)
	if err != nil {
		return nil, fmt.Errorf("get deployment %q: %w", deploymentID, err)
	}
	if !found {
		return nil, fmt.Errorf("deployment %q: %w", deploymentID, ErrDeploymentNotFound)
	}

	if m.connections == nil || m.registry == nil {
		return &dep, nil
	}

	conn, foundConn, err := m.connections.Get(accountScopeID, workspaceID, dep.ConnectionID)
	if err != nil || !foundConn {
		return &dep, nil
	}

	prov, ok := m.registry.Get(conn.Kind)
	if !ok {
		return &dep, nil
	}

	res, err := prov.Inspect(ctx, &conn, &dep)
	if err != nil {
		return &dep, err
	}

	if res.Runtime.ContainerID != "" {
		dep.Runtime = res.Runtime
		_, _ = m.deployments.UpdateRuntime(accountScopeID, workspaceID, deploymentID, dep.Runtime)
	}
	updatedDep, err := m.deployments.UpdateStatus(accountScopeID, workspaceID, deploymentID, res.Status, res.Health, res.ErrorMessage)
	if err != nil {
		return &dep, nil
	}
	return &updatedDep, nil
}

// ResolveAccess returns consumer-relevant access metadata for an active deployment.
func (m *DeploymentManager) ResolveAccess(ctx context.Context, accountScopeID, workspaceID, deploymentID string) (*provider.DeploymentAccess, error) {
	if m == nil || m.deployments == nil {
		return nil, errors.New("deployment store is not configured")
	}

	dep, found, err := m.deployments.Get(accountScopeID, workspaceID, deploymentID)
	if err != nil {
		return nil, fmt.Errorf("get deployment %q: %w", deploymentID, err)
	}
	if !found {
		return nil, fmt.Errorf("deployment %q: %w", deploymentID, ErrDeploymentNotFound)
	}

	conn, foundConn, err := m.connections.Get(accountScopeID, workspaceID, dep.ConnectionID)
	if err != nil {
		return nil, fmt.Errorf("get connection %q: %w", dep.ConnectionID, err)
	}
	if !foundConn {
		return nil, fmt.Errorf("connection %q: %w", dep.ConnectionID, ErrConnectionNotFound)
	}

	prov, ok := m.registry.Get(conn.Kind)
	if !ok {
		return nil, fmt.Errorf("provider for connection kind %q: %w", conn.Kind, ErrProviderNotRegistered)
	}

	return prov.ResolveAccess(ctx, &conn, &dep)
}

// Exec executes a command inside the running deployment container.
func (m *DeploymentManager) Exec(ctx context.Context, accountScopeID, workspaceID, deploymentID string, req provider.ExecRequest) (*provider.ExecResult, error) {
	if m == nil || m.deployments == nil {
		return nil, errors.New("deployment store is not configured")
	}

	dep, found, err := m.deployments.Get(accountScopeID, workspaceID, deploymentID)
	if err != nil {
		return nil, fmt.Errorf("get deployment %q: %w", deploymentID, err)
	}
	if !found {
		return nil, fmt.Errorf("deployment %q: %w", deploymentID, ErrDeploymentNotFound)
	}

	if !dep.IsUsable() {
		return nil, fmt.Errorf("deployment %q is in status %q (health: %q): %w", deploymentID, dep.Status, dep.Health, ErrDeploymentUnusable)
	}

	conn, foundConn, err := m.connections.Get(accountScopeID, workspaceID, dep.ConnectionID)
	if err != nil {
		return nil, fmt.Errorf("get connection %q: %w", dep.ConnectionID, err)
	}
	if !foundConn {
		return nil, fmt.Errorf("connection %q: %w", dep.ConnectionID, ErrConnectionNotFound)
	}

	prov, ok := m.registry.Get(conn.Kind)
	if !ok {
		return nil, fmt.Errorf("provider for connection kind %q: %w", conn.Kind, ErrProviderNotRegistered)
	}

	return prov.Exec(ctx, &conn, &dep, req)
}

// GetDeployment retrieves a deployment record by ID.
func (m *DeploymentManager) GetDeployment(accountScopeID, workspaceID, deploymentID string) (environments.Deployment, bool, error) {
	if m == nil || m.deployments == nil {
		return environments.Deployment{}, false, errors.New("deployment store is not configured")
	}
	return m.deployments.Get(accountScopeID, workspaceID, deploymentID)
}

// ListDeployments lists deployments for a workspace.
func (m *DeploymentManager) ListDeployments(accountScopeID, workspaceID string, limit int) ([]environments.Deployment, error) {
	if m == nil || m.deployments == nil {
		return nil, errors.New("deployment store is not configured")
	}
	return m.deployments.List(accountScopeID, workspaceID, limit)
}

// ListDeploymentsByEnvironment lists deployments for a specific environment definition in a workspace.
func (m *DeploymentManager) ListDeploymentsByEnvironment(accountScopeID, workspaceID, environmentID string, limit int) ([]environments.Deployment, error) {
	if m == nil || m.deployments == nil {
		return nil, errors.New("deployment store is not configured")
	}
	return m.deployments.ListByEnvironment(accountScopeID, workspaceID, environmentID, limit)
}

// GetActiveLease retrieves the active lease for a deployment if one is held.
func (m *DeploymentManager) GetActiveLease(accountScopeID, workspaceID, deploymentID string) (environments.DeploymentLease, bool, error) {
	if m == nil || m.deployments == nil {
		return environments.DeploymentLease{}, false, errors.New("deployment store is not configured")
	}
	return m.deployments.GetActiveLease(accountScopeID, workspaceID, deploymentID)
}

// GetLease retrieves a deployment lease by lease ID.
func (m *DeploymentManager) GetLease(accountScopeID, workspaceID, leaseID string) (environments.DeploymentLease, bool, error) {
	if m == nil || m.deployments == nil || m.deployments.Leases() == nil {
		return environments.DeploymentLease{}, false, errors.New("lease store is not configured")
	}
	return m.deployments.Leases().Get(accountScopeID, workspaceID, leaseID)
}
