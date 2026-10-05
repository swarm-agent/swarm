package lifecycle

import (
	"context"
	"errors"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
)

// AcquirePreparedLeaseRequest contains trusted service inputs, NOT a tool/API
// payload. The attachment resolver must first authorize the durable attachment,
// account, project, source catalog generations and consumer role (never Coder).
// Attribution must come from authenticated runtime identity, never user arguments.
type AcquirePreparedLeaseRequest struct {
	Binding     *environments.TaskLeaseBinding
	Source      environments.PreparedDeploymentSource
	Attribution environments.OperationAttribution
	TTLMillis   int64
}

func leaseConsumer(a environments.OperationAttribution) (environments.ConsumerType, string) {
	if a.SessionID != "" {
		return environments.ConsumerTypeSession, a.SessionID
	}
	if a.WorkerID != "" {
		return environments.ConsumerTypeWorker, a.WorkerID
	}
	return environments.ConsumerTypeCustom, a.Actor
}

func ownsLease(a environments.OperationAttribution, lease environments.DeploymentLease) bool {
	kind, id := leaseConsumer(a)
	return id != "" && id == lease.ConsumerID && kind == lease.ConsumerType
}

// AcquirePreparedLease acquires an independent expiring receipt without provider
// effects or provisioning. It is not public authorization: callers must satisfy
// the resolver contract above. Ordinary Ensure remains exclusive.
func (m *DeploymentManager) AcquirePreparedLease(ctx context.Context, req AcquirePreparedLeaseRequest) (environments.DeploymentLease, error) {
	if m == nil || m.deployments == nil || m.deployments.Leases() == nil {
		return environments.DeploymentLease{}, errors.New("lease store is not configured")
	}
	kind, id := leaseConsumer(req.Attribution)
	if id == "" {
		return environments.DeploymentLease{}, ErrMissingAttribution
	}
	if req.TTLMillis < 0 || req.TTLMillis > DefaultLeaseTTLMillis {
		return environments.DeploymentLease{}, errors.New("prepared lease TTL must not exceed one hour")
	}
	s := req.Source
	candidate := environments.DeploymentLease{AccountScopeID: s.AccountScopeID, WorkspaceID: s.WorkspaceID, EnvironmentID: s.EnvironmentID, DeploymentID: s.DeploymentID, PreparedSource: &s, TaskBinding: req.Binding, ConsumerType: kind, ConsumerID: id}
	if err := m.validateTaskLease(ctx, candidate); err != nil {
		return environments.DeploymentLease{}, err
	}
	envLock := m.getEnvLock(s.AccountScopeID, s.WorkspaceID, s.EnvironmentID)
	if err := envLock.Lock(ctx); err != nil {
		return environments.DeploymentLease{}, err
	}
	defer envLock.Unlock()
	depLock := m.getDepLock(s.AccountScopeID, s.WorkspaceID, s.DeploymentID)
	if err := depLock.Lock(ctx); err != nil {
		return environments.DeploymentLease{}, err
	}
	defer depLock.Unlock()
	dep, found, err := m.deployments.Get(s.AccountScopeID, s.WorkspaceID, s.DeploymentID)
	if err != nil {
		return environments.DeploymentLease{}, err
	}
	if !found || !s.Matches(dep) || ManagedReviewExpired(dep, time.Now().UnixMilli()) {
		return environments.DeploymentLease{}, errors.New("prepared deployment source is stale or unavailable")
	}
	if ops := m.Operations(); ops != nil {
		_, active, err := ops.GetActiveOperationForDeployment(s.AccountScopeID, s.WorkspaceID, s.DeploymentID)
		if err != nil {
			return environments.DeploymentLease{}, err
		}
		if active {
			return environments.DeploymentLease{}, environments.ErrDeploymentOperationConflict
		}
	}
	if err := ctx.Err(); err != nil {
		return environments.DeploymentLease{}, err
	}
	// Durable authorization is repeated at exec's provider boundary, without
	// holding lifecycle locks (validators may call back into the manager).
	candidate.ExpiresAt = resolveLeaseExpiresAt(time.Now().UnixMilli(), req.TTLMillis, 0)
	return m.deployments.Leases().AcquireSharedLease(candidate)
}
