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

	// DefaultLeaseTTLMillis defines the default lease lifetime (1 hour) when no explicit TTL is requested.
	DefaultLeaseTTLMillis int64 = 60 * 60 * 1000
)

// resolveLeaseExpiresAt calculates the lease expiration timestamp: explicit TTL > idle timeout > default 1 hour.
func resolveLeaseExpiresAt(now, reqTTLMillis int64, idleTimeoutSeconds int) int64 {
	if reqTTLMillis > 0 {
		return now + reqTTLMillis
	}
	if idleTimeoutSeconds > 0 {
		return now + int64(idleTimeoutSeconds)*1000
	}
	return now + DefaultLeaseTTLMillis
}

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
	ConsumerType     environments.ConsumerType `json:"consumer_type"`           // session, test_run, worker, custom
	ConsumerID       string                    `json:"consumer_id"`
	ConsumerMetadata map[string]string         `json:"consumer_metadata,omitempty"`
	DeploymentID     string                    `json:"deployment_id,omitempty"` // Durable pre-assigned deployment ID
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
	DeploymentID     string                    `json:"deployment_id,omitempty"` // Durable pre-assigned deployment ID
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
// limit enforcement, cancellation-aware locking, and durable supervised operations.
type DeploymentManager struct {
	connections  ConnectionReader
	environments EnvironmentReader
	deployments  DeploymentManagerStore
	workspaces   WorkspaceSettingsReader
	registry     *provider.Registry
	operations   OperationStore

	mu       sync.Mutex
	envLocks map[string]*contextMutex
	depLocks map[string]*contextMutex

	activeOps   map[string]*activeOpState
	activeOpsMu sync.RWMutex

	rootCtx    context.Context
	rootCancel context.CancelFunc

	heartbeatInterval time.Duration
	cleanupTimeout    time.Duration
	probeTimeout      time.Duration
	defaultTimeout    time.Duration
	maxTimeout        time.Duration
	maxConcurrentOps  int
	opSem             chan struct{}
}

var _ OperationService = (*DeploymentManager)(nil)

// NewDeploymentManager creates a new DeploymentManager instance with backward-compatible options.
func NewDeploymentManager(
	connections ConnectionReader,
	environments EnvironmentReader,
	deployments DeploymentManagerStore,
	workspaces WorkspaceSettingsReader,
	registry *provider.Registry,
	opts ...DeploymentManagerOption,
) *DeploymentManager {
	var opStore OperationStore
	if dsOps, ok := deployments.(interface{ Operations() *pebblestore.EnvironmentOperationStore }); ok && dsOps != nil {
		opStore = dsOps.Operations()
	}

	rootCtx, rootCancel := context.WithCancel(context.Background())
	mgr := &DeploymentManager{
		connections:       connections,
		environments:      environments,
		deployments:       deployments,
		workspaces:        workspaces,
		registry:          registry,
		operations:        opStore,
		envLocks:          make(map[string]*contextMutex),
		depLocks:          make(map[string]*contextMutex),
		activeOps:         make(map[string]*activeOpState),
		rootCtx:           rootCtx,
		rootCancel:        rootCancel,
		heartbeatInterval: 5 * time.Second,
		cleanupTimeout:    provider.CleanupTimeout,
		probeTimeout:      provider.ProbeTimeout,
		defaultTimeout:    provider.DefaultOperationTimeout,
		maxTimeout:        provider.MaxOperationTimeout,
		maxConcurrentOps:  50,
		opSem:             make(chan struct{}, 50),
	}

	for _, opt := range opts {
		if opt != nil {
			opt(mgr)
		}
	}

	return mgr
}

// getEnvLock retrieves or initializes a cancellation-aware mutex for an environment.
func (m *DeploymentManager) getEnvLock(accountScopeID, workspaceID, environmentID string) *contextMutex {
	key := accountScopeID + "/" + workspaceID + "/" + environmentID
	m.mu.Lock()
	defer m.mu.Unlock()
	lock, exists := m.envLocks[key]
	if !exists {
		lock = newContextMutex()
		m.envLocks[key] = lock
	}
	return lock
}

// getDepLock retrieves or initializes a cancellation-aware mutex for a deployment.
func (m *DeploymentManager) getDepLock(accountScopeID, workspaceID, deploymentID string) *contextMutex {
	key := accountScopeID + "/" + workspaceID + "/" + deploymentID
	m.mu.Lock()
	defer m.mu.Unlock()
	lock, exists := m.depLocks[key]
	if !exists {
		lock = newContextMutex()
		m.depLocks[key] = lock
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

	// 4. Synchronize across concurrent calls for this environment with cancellation-aware lock
	envLock := m.getEnvLock(req.AccountScopeID, req.WorkspaceID, req.EnvironmentID)
	if err := envLock.Lock(ctx); err != nil {
		return nil, fmt.Errorf("acquire environment lock: %w", err)
	}
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
			if dep.ConnectionID != conn.ID {
				continue
			}
			if !dep.IsUsable() {
				continue
			}

			// Check if deployment has an unresolved or active operation
			if m.operations != nil {
				activeOp, hasOp, _ := m.operations.GetActiveOperationForDeployment(req.AccountScopeID, req.WorkspaceID, dep.ID)
				if hasOp && (activeOp.IsActive() || activeOp.IsUnresolved()) {
					callingOpID := operationIDFromContext(ctx)
					if callingOpID == "" || activeOp.OperationID != callingOpID {
						continue
					}
				}
			}

			// Check if deployment is currently leased
			activeLease, hasActive, err := m.deployments.GetActiveLease(req.AccountScopeID, req.WorkspaceID, dep.ID)
			if err != nil {
				continue
			}
			if hasActive && activeLease.Active && !activeLease.IsExpired(now) {
				continue
			}
			if hasActive && activeLease.Active && activeLease.IsExpired(now) {
				_, _ = m.deployments.ReleaseLease(req.AccountScopeID, req.WorkspaceID, activeLease.ID, "expired_before_reuse")
			}

			// Per-deployment lock during reuse
			depLock := m.getDepLock(req.AccountScopeID, req.WorkspaceID, dep.ID)
			if err := depLock.Lock(ctx); err != nil {
				return nil, fmt.Errorf("acquire deployment lock: %w", err)
			}

			leaseID := "lease_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			expiresAt := resolveLeaseExpiresAt(now, req.TTLMillis, env.DeploymentPolicy.IdleTimeoutSeconds)
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
				depLock.Unlock()
				if errors.Is(err, pebblestore.ErrDeploymentLeaseHeld) {
					continue
				}
				return nil, fmt.Errorf("acquire lease on reused deployment %q: %w", dep.ID, err)
			}

			// Transition deployment status to busy, propagating errors without swallowing
			updatedDep, err := m.deployments.UpdateStatus(req.AccountScopeID, req.WorkspaceID, dep.ID, environments.DeploymentStatusBusy, dep.Health, "")
			if err != nil {
				_, _ = m.deployments.ReleaseLease(req.AccountScopeID, req.WorkspaceID, acquiredLease.ID, "status_update_failed")
				depLock.Unlock()
				return nil, fmt.Errorf("update deployment status to busy: %w", err)
			}
			depLock.Unlock()

			// Resolve runtime access with bounded probe timeout
			probeCtx, probeCancel := context.WithTimeout(ctx, m.probeTimeout)
			access, _ := prov.ResolveAccess(probeCtx, conn, &updatedDep)
			probeCancel()

			if m.operations != nil {
				_ = m.operations.UpdateDeploymentCount(req.AccountScopeID, req.WorkspaceID)
			}

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
	depID := strings.TrimSpace(req.DeploymentID)
	if depID == "" {
		depID = "dep_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
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
	savedDep.Status = environments.DeploymentStatusBusy

	if _, err := m.deployments.UpdateRuntime(req.AccountScopeID, req.WorkspaceID, depID, savedDep.Runtime); err != nil {
		_ = prov.Destroy(ctx, conn, &savedDep)
		_, _ = m.deployments.UpdateStatus(req.AccountScopeID, req.WorkspaceID, depID, environments.DeploymentStatusFailed, environments.HealthStatusUnhealthy, err.Error())
		return nil, fmt.Errorf("update deployment runtime: %w", err)
	}

	updatedDep, err := m.deployments.UpdateStatus(req.AccountScopeID, req.WorkspaceID, depID, environments.DeploymentStatusBusy, savedDep.Health, "")
	if err != nil {
		_ = prov.Destroy(ctx, conn, &savedDep)
		return nil, fmt.Errorf("update deployment status: %w", err)
	}
	savedDep = updatedDep

	// Acquire lease for the consumer
	leaseID := "lease_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	expiresAt := resolveLeaseExpiresAt(now, req.TTLMillis, env.DeploymentPolicy.IdleTimeoutSeconds)
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
		_ = prov.Destroy(ctx, conn, &savedDep)
		_, _ = m.deployments.UpdateStatus(req.AccountScopeID, req.WorkspaceID, depID, environments.DeploymentStatusFailed, environments.HealthStatusUnhealthy, "lease acquisition failed: "+err.Error())
		return nil, fmt.Errorf("acquire lease on new deployment: %w", err)
	}

	probeCtx, probeCancel := context.WithTimeout(ctx, m.probeTimeout)
	access, _ := prov.ResolveAccess(probeCtx, conn, &savedDep)
	probeCancel()

	if m.operations != nil {
		_ = m.operations.UpdateDeploymentCount(req.AccountScopeID, req.WorkspaceID)
	}

	return &EnsureDeploymentResult{
		Deployment: savedDep,
		Lease:      acquiredLease,
		Reused:     false,
		Access:     access,
	}, nil
}

// DeployDeployment directly provisions a new deployment for an environment within max_instances limits.
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

	env, found, err := m.environments.Get(req.AccountScopeID, req.WorkspaceID, req.EnvironmentID)
	if err != nil {
		return nil, fmt.Errorf("get environment %q: %w", req.EnvironmentID, err)
	}
	if !found {
		return nil, fmt.Errorf("environment %q: %w", req.EnvironmentID, ErrEnvironmentNotFound)
	}

	conn, err := m.ResolveConnection(ctx, req.AccountScopeID, req.WorkspaceID, req.ConnectionID, &env)
	if err != nil {
		return nil, fmt.Errorf("resolve connection for environment %q: %w", req.EnvironmentID, err)
	}

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

	envLock := m.getEnvLock(req.AccountScopeID, req.WorkspaceID, req.EnvironmentID)
	if err := envLock.Lock(ctx); err != nil {
		return nil, fmt.Errorf("acquire environment lock: %w", err)
	}
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
	depID := strings.TrimSpace(req.DeploymentID)
	if depID == "" {
		depID = "dep_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
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
		expiresAt := resolveLeaseExpiresAt(now, req.TTLMillis, env.DeploymentPolicy.IdleTimeoutSeconds)
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
		_ = prov.Destroy(ctx, conn, &savedDep)
		return nil, fmt.Errorf("update deployment runtime: %w", err)
	}
	updatedDep, err := m.deployments.UpdateStatus(req.AccountScopeID, req.WorkspaceID, depID, initialStatus, savedDep.Health, "")
	if err != nil {
		_ = prov.Destroy(ctx, conn, &savedDep)
		return nil, fmt.Errorf("update deployment status: %w", err)
	}
	savedDep = updatedDep

	probeCtx, probeCancel := context.WithTimeout(ctx, m.probeTimeout)
	access, _ := prov.ResolveAccess(probeCtx, conn, &savedDep)
	probeCancel()

	if m.operations != nil {
		_ = m.operations.UpdateDeploymentCount(req.AccountScopeID, req.WorkspaceID)
	}

	return &DeployDeploymentResult{
		Deployment: savedDep,
		Lease:      acquiredLease,
		Access:     access,
	}, nil
}

// ReleaseDeployment releases an active deployment lease executing configured release behavior.
// Cleanup must complete safely before releasing reusable ownership.
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

	dep, foundDep, err := m.deployments.Get(req.AccountScopeID, req.WorkspaceID, lease.DeploymentID)
	if err != nil {
		return nil, fmt.Errorf("get deployment %q: %w", lease.DeploymentID, err)
	}
	if !foundDep {
		reason := req.Reason
		if reason == "" {
			reason = "released_orphaned_deployment"
		}
		relLease, relErr := m.deployments.ReleaseLease(req.AccountScopeID, req.WorkspaceID, req.LeaseID, reason)
		if relErr != nil {
			return nil, fmt.Errorf("release orphaned lease: %w", relErr)
		}
		return &ReleaseDeploymentResult{Lease: relLease}, fmt.Errorf("deployment %q: %w", lease.DeploymentID, ErrDeploymentNotFound)
	}

	envLock := m.getEnvLock(req.AccountScopeID, req.WorkspaceID, dep.EnvironmentID)
	if err := envLock.Lock(ctx); err != nil {
		return nil, fmt.Errorf("acquire environment lock: %w", err)
	}
	defer envLock.Unlock()

	depLock := m.getDepLock(req.AccountScopeID, req.WorkspaceID, dep.ID)
	if err := depLock.Lock(ctx); err != nil {
		return nil, fmt.Errorf("acquire deployment lock: %w", err)
	}
	defer depLock.Unlock()

	releaseBehavior := environments.ReleaseBehaviorNone
	if m.environments != nil {
		env, foundEnv, _ := m.environments.Get(req.AccountScopeID, req.WorkspaceID, dep.EnvironmentID)
		if foundEnv && env.DeploymentPolicy.ReleaseBehavior != "" {
			releaseBehavior = env.DeploymentPolicy.ReleaseBehavior
		}
	}

	var prov provider.DeploymentProvider
	var conn environments.Connection
	if m.connections != nil {
		c, foundConn, err := m.connections.Get(req.AccountScopeID, req.WorkspaceID, dep.ConnectionID)
		if err != nil {
			return nil, fmt.Errorf("lookup connection: %w", err)
		}
		if foundConn {
			conn = c
			if m.registry != nil {
				prov, _ = m.registry.Get(conn.Kind)
			}
		}
	}

	// Execute release cleanup safely BEFORE releasing the lease
	actionTaken := ""
	switch releaseBehavior {
	case environments.ReleaseBehaviorRestart:
		actionTaken = "restarted"
		if prov != nil {
			if err := prov.Stop(ctx, &conn, &dep); err != nil {
				_, _ = m.deployments.UpdateStatus(req.AccountScopeID, req.WorkspaceID, dep.ID, environments.DeploymentStatusFailed, environments.HealthStatusUnhealthy, "restart stop failed: "+err.Error())
				return nil, fmt.Errorf("stop container during restart: %w", err)
			}
			if err := prov.Start(ctx, &conn, &dep); err != nil {
				_, _ = m.deployments.UpdateStatus(req.AccountScopeID, req.WorkspaceID, dep.ID, environments.DeploymentStatusFailed, environments.HealthStatusUnhealthy, "restart start failed: "+err.Error())
				return nil, fmt.Errorf("start container during restart: %w", err)
			}
		}
		updatedDep, err := m.deployments.UpdateStatus(req.AccountScopeID, req.WorkspaceID, dep.ID, environments.DeploymentStatusReady, environments.HealthStatusHealthy, "")
		if err != nil {
			return nil, fmt.Errorf("update deployment status: %w", err)
		}
		dep = updatedDep

	case environments.ReleaseBehaviorRecreate:
		actionTaken = "recreated"
		if prov != nil {
			if err := prov.Destroy(ctx, &conn, &dep); err != nil {
				_, _ = m.deployments.UpdateStatus(req.AccountScopeID, req.WorkspaceID, dep.ID, environments.DeploymentStatusFailed, environments.HealthStatusUnhealthy, "recreate destroy failed: "+err.Error())
				return nil, fmt.Errorf("destroy container during recreate: %w", err)
			}
		}
		// Retain deployment record: mark as terminated, do not delete from store
		updatedDep, err := m.deployments.UpdateStatus(req.AccountScopeID, req.WorkspaceID, dep.ID, environments.DeploymentStatusTerminated, environments.HealthStatusUnknown, "released with recreate policy")
		if err != nil {
			return nil, fmt.Errorf("update deployment status: %w", err)
		}
		dep = updatedDep

	case environments.ReleaseBehaviorNone:
		fallthrough
	default:
		actionTaken = "retained"
		updatedDep, err := m.deployments.UpdateStatus(req.AccountScopeID, req.WorkspaceID, dep.ID, environments.DeploymentStatusReady, environments.HealthStatusHealthy, "")
		if err != nil {
			return nil, fmt.Errorf("update deployment status: %w", err)
		}
		dep = updatedDep
	}

	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "released_by_consumer"
	}
	releasedLease, err := m.deployments.ReleaseLease(req.AccountScopeID, req.WorkspaceID, req.LeaseID, reason)
	if err != nil {
		return nil, fmt.Errorf("release lease in store: %w", err)
	}

	if m.operations != nil {
		_ = m.operations.UpdateDeploymentCount(req.AccountScopeID, req.WorkspaceID)
	}

	return &ReleaseDeploymentResult{
		Lease:           releasedLease,
		Deployment:      dep,
		ReleaseBehavior: releaseBehavior,
		ActionTaken:     actionTaken,
	}, nil
}

// DestroyDeployment forcibly terminates and removes a deployment container,
// releasing active lease and retaining the deployment record with status terminated.
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
	if err := envLock.Lock(ctx); err != nil {
		return fmt.Errorf("acquire environment lock: %w", err)
	}
	defer envLock.Unlock()

	depLock := m.getDepLock(req.AccountScopeID, req.WorkspaceID, req.DeploymentID)
	if err := depLock.Lock(ctx); err != nil {
		return fmt.Errorf("acquire deployment lock: %w", err)
	}
	defer depLock.Unlock()

	// 1. Release active lease if held
	activeLease, hasActive, err := m.deployments.GetActiveLease(req.AccountScopeID, req.WorkspaceID, req.DeploymentID)
	if err == nil && hasActive && activeLease.Active {
		reason := "deployment_destroyed"
		if req.Reason != "" {
			reason += ": " + req.Reason
		}
		_, _ = m.deployments.ReleaseLease(req.AccountScopeID, req.WorkspaceID, activeLease.ID, reason)
	}

	// 2. Destroy container via provider (propagate provider errors without swallowing)
	if m.connections != nil && m.registry != nil {
		conn, foundConn, err := m.connections.Get(req.AccountScopeID, req.WorkspaceID, dep.ConnectionID)
		if err != nil {
			return fmt.Errorf("lookup connection: %w", err)
		}
		if foundConn {
			if prov, ok := m.registry.Get(conn.Kind); ok {
				if err := prov.Destroy(ctx, &conn, &dep); err != nil {
					_, _ = m.deployments.UpdateStatus(req.AccountScopeID, req.WorkspaceID, req.DeploymentID, environments.DeploymentStatusFailed, environments.HealthStatusUnhealthy, "destroy failed: "+err.Error())
					return fmt.Errorf("destroy container on provider: %w", err)
				}
			}
		}
	}

	// 3. Mark terminated and retain record in store (do not delete)
	_, err = m.deployments.UpdateStatus(req.AccountScopeID, req.WorkspaceID, req.DeploymentID, environments.DeploymentStatusTerminated, environments.HealthStatusUnknown, "forcibly destroyed: "+req.Reason)
	if err != nil {
		return fmt.Errorf("update deployment status to terminated: %w", err)
	}

	if m.operations != nil {
		_ = m.operations.UpdateDeploymentCount(req.AccountScopeID, req.WorkspaceID)
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

	depLock := m.getDepLock(accountScopeID, workspaceID, deploymentID)
	if err := depLock.Lock(ctx); err != nil {
		return fmt.Errorf("acquire deployment lock: %w", err)
	}
	defer depLock.Unlock()

	if m.connections != nil && m.registry != nil {
		conn, foundConn, err := m.connections.Get(accountScopeID, workspaceID, dep.ConnectionID)
		if err != nil {
			return fmt.Errorf("lookup connection: %w", err)
		}
		if foundConn {
			if prov, ok := m.registry.Get(conn.Kind); ok {
				if err := prov.Stop(ctx, &conn, &dep); err != nil {
					_, _ = m.deployments.UpdateStatus(accountScopeID, workspaceID, deploymentID, environments.DeploymentStatusFailed, environments.HealthStatusUnhealthy, "stop failed: "+err.Error())
					return fmt.Errorf("stop deployment container %q: %w", deploymentID, err)
				}
			}
		}
	}

	_, err = m.deployments.UpdateStatus(accountScopeID, workspaceID, deploymentID, environments.DeploymentStatusStopped, environments.HealthStatusUnknown, "")
	if err != nil {
		return fmt.Errorf("update deployment status: %w", err)
	}

	if m.operations != nil {
		_ = m.operations.UpdateDeploymentCount(accountScopeID, workspaceID)
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

	depLock := m.getDepLock(accountScopeID, workspaceID, deploymentID)
	if err := depLock.Lock(ctx); err != nil {
		return fmt.Errorf("acquire deployment lock: %w", err)
	}
	defer depLock.Unlock()

	if m.connections != nil && m.registry != nil {
		conn, foundConn, err := m.connections.Get(accountScopeID, workspaceID, dep.ConnectionID)
		if err != nil {
			return fmt.Errorf("lookup connection: %w", err)
		}
		if foundConn {
			if prov, ok := m.registry.Get(conn.Kind); ok {
				if err := prov.Start(ctx, &conn, &dep); err != nil {
					_, _ = m.deployments.UpdateStatus(accountScopeID, workspaceID, deploymentID, environments.DeploymentStatusFailed, environments.HealthStatusUnhealthy, "start failed: "+err.Error())
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

	if m.operations != nil {
		_ = m.operations.UpdateDeploymentCount(accountScopeID, workspaceID)
	}

	return nil
}

// InspectDeployment queries provider for live status/health, never leaving unknown connectivity healthy.
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

	probeTimeout := m.probeTimeout
	if probeTimeout <= 0 {
		probeTimeout = provider.ProbeTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	if m.connections == nil || m.registry == nil {
		_, _ = m.deployments.UpdateStatus(accountScopeID, workspaceID, deploymentID, dep.Status, environments.HealthStatusUnknown, "connections or registry not configured")
		return &dep, errors.New("connections or provider registry not configured")
	}

	conn, foundConn, err := m.connections.Get(accountScopeID, workspaceID, dep.ConnectionID)
	if err != nil {
		_, _ = m.deployments.UpdateStatus(accountScopeID, workspaceID, deploymentID, dep.Status, environments.HealthStatusUnknown, "lookup connection failed: "+err.Error())
		return &dep, fmt.Errorf("lookup connection: %w", err)
	}
	if !foundConn {
		_, _ = m.deployments.UpdateStatus(accountScopeID, workspaceID, deploymentID, dep.Status, environments.HealthStatusUnknown, "connection not found")
		return &dep, fmt.Errorf("connection %q: %w", dep.ConnectionID, ErrConnectionNotFound)
	}

	prov, ok := m.registry.Get(conn.Kind)
	if !ok {
		_, _ = m.deployments.UpdateStatus(accountScopeID, workspaceID, deploymentID, dep.Status, environments.HealthStatusUnknown, "provider not registered")
		return &dep, fmt.Errorf("provider for kind %q: %w", conn.Kind, ErrProviderNotRegistered)
	}

	res, err := prov.Inspect(probeCtx, &conn, &dep)
	if err != nil {
		updated, storeErr := m.deployments.UpdateStatus(accountScopeID, workspaceID, deploymentID, dep.Status, environments.HealthStatusUnhealthy, "provider observation unavailable")
		if storeErr != nil {
			return &dep, errors.Join(fmt.Errorf("inspect container on provider: %w", err), storeErr)
		}
		return &updated, fmt.Errorf("inspect container on provider: %w", err)
	}

	if res.Runtime.ContainerID != "" {
		dep.Runtime = res.Runtime
		_, _ = m.deployments.UpdateRuntime(accountScopeID, workspaceID, deploymentID, dep.Runtime)
	}

	health := res.Health
	if health == "" {
		health = environments.HealthStatusUnknown
	}
	updatedDep, err := m.deployments.UpdateStatus(accountScopeID, workspaceID, deploymentID, res.Status, health, res.ErrorMessage)
	if err != nil {
		return &dep, fmt.Errorf("update inspected status: %w", err)
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

	probeTimeout := m.probeTimeout
	if probeTimeout <= 0 {
		probeTimeout = provider.ProbeTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	return prov.ResolveAccess(probeCtx, &conn, &dep)
}

// Exec executes a command inside the running deployment container.
// Validates deployment usability and active lease ownership.
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

	// Validate current active lease is held
	activeLease, hasActive, err := m.deployments.GetActiveLease(accountScopeID, workspaceID, deploymentID)
	if err != nil {
		return nil, fmt.Errorf("get active lease: %w", err)
	}
	if !hasActive || !activeLease.IsHeld(time.Now().UnixMilli()) {
		return nil, fmt.Errorf("active lease required to exec in deployment %q", deploymentID)
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

// GetDeployment retrieves a deployment record by ID (side-effect free).
func (m *DeploymentManager) GetDeployment(accountScopeID, workspaceID, deploymentID string) (environments.Deployment, bool, error) {
	if m == nil || m.deployments == nil {
		return environments.Deployment{}, false, errors.New("deployment store is not configured")
	}
	return m.deployments.Get(accountScopeID, workspaceID, deploymentID)
}

// ListDeployments lists deployments for a workspace (side-effect free).
func (m *DeploymentManager) ListDeployments(accountScopeID, workspaceID string, limit int) ([]environments.Deployment, error) {
	if m == nil || m.deployments == nil {
		return nil, errors.New("deployment store is not configured")
	}
	return m.deployments.List(accountScopeID, workspaceID, limit)
}

// ListDeploymentsByEnvironment lists deployments for an environment (side-effect free).
func (m *DeploymentManager) ListDeploymentsByEnvironment(accountScopeID, workspaceID, environmentID string, limit int) ([]environments.Deployment, error) {
	if m == nil || m.deployments == nil {
		return nil, errors.New("deployment store is not configured")
	}
	return m.deployments.ListByEnvironment(accountScopeID, workspaceID, environmentID, limit)
}

// GetActiveLease retrieves the active lease for a deployment if held and unexpired (side-effect free).
func (m *DeploymentManager) GetActiveLease(accountScopeID, workspaceID, deploymentID string) (environments.DeploymentLease, bool, error) {
	if m == nil || m.deployments == nil {
		return environments.DeploymentLease{}, false, errors.New("deployment store is not configured")
	}
	lease, found, err := m.deployments.GetActiveLease(accountScopeID, workspaceID, deploymentID)
	if err != nil || !found {
		return environments.DeploymentLease{}, found, err
	}
	if !lease.IsHeld(time.Now().UnixMilli()) {
		return environments.DeploymentLease{}, false, nil
	}
	return lease, true, nil
}

// ReapExpired releases expired leases and triggers environment release behavior for idle/expired deployments.
func (m *DeploymentManager) ReapExpired(ctx context.Context, accountScopeID, workspaceID string) ([]string, error) {
	if m == nil || m.deployments == nil {
		return nil, errors.New("deployment store is not configured")
	}

	accountScopeID = strings.TrimSpace(accountScopeID)
	workspaceID = strings.TrimSpace(workspaceID)
	if accountScopeID == "" || workspaceID == "" {
		return nil, errors.New("account scope id and workspace id are required")
	}

	deps, err := m.deployments.List(accountScopeID, workspaceID, 1000)
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}

	now := time.Now().UnixMilli()
	var reaped []string
	for _, dep := range deps {
		if !dep.IsActive() {
			continue
		}
		activeLease, hasActive, err := m.deployments.GetActiveLease(accountScopeID, workspaceID, dep.ID)
		if err == nil && hasActive && activeLease.Active && activeLease.IsExpired(now) {
			_, relErr := m.ReleaseDeployment(ctx, ReleaseDeploymentRequest{
				AccountScopeID: accountScopeID,
				WorkspaceID:    workspaceID,
				LeaseID:        activeLease.ID,
				Reason:         "lease_ttl_expired",
			})
			if relErr == nil {
				reaped = append(reaped, fmt.Sprintf("lease %s on deployment %s expired and released", activeLease.ID, dep.ID))
			}
		}
	}

	return reaped, nil
}

// GetLease retrieves a deployment lease by lease ID (side-effect free).
func (m *DeploymentManager) GetLease(accountScopeID, workspaceID, leaseID string) (environments.DeploymentLease, bool, error) {
	if m == nil || m.deployments == nil || m.deployments.Leases() == nil {
		return environments.DeploymentLease{}, false, errors.New("lease store is not configured")
	}
	return m.deployments.Leases().Get(accountScopeID, workspaceID, leaseID)
}
