package lifecycle

import (
	"context"
	"errors"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/provider"
)

// SetTaskLeaseValidator installs the durable authorization boundary at service
// wiring time. The callback is read under mu, then invoked without manager locks.
func (m *DeploymentManager) SetTaskLeaseValidator(fn func(context.Context, environments.DeploymentLease) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.taskLeaseValidator = fn
}

func (m *DeploymentManager) validateTaskLease(ctx context.Context, lease environments.DeploymentLease) error {
	m.mu.Lock()
	fn := m.taskLeaseValidator
	m.mu.Unlock()
	if lease.TaskBinding == nil {
		if lease.Shared || lease.PreparedSource != nil {
			return errors.New("prepared lease requires a durable task attachment binding")
		}
		return nil
	}
	if fn == nil {
		return errors.New("task attachment authorization unavailable")
	}
	return fn(ctx, lease)
}

// ResolveLeaseAccess is the task consumer access primitive. The caller supplies
// authenticated attribution; no receipt/source contents are accepted from models.
func (m *DeploymentManager) ResolveLeaseAccess(ctx context.Context, account, workspace, leaseID string, attribution environments.OperationAttribution) (*provider.DeploymentAccess, error) {
	lease, found, err := m.GetLease(account, workspace, leaseID)
	if err != nil { return nil, err }
	if !found || !ownsLease(attribution, lease) || !lease.IsHeld(time.Now().UnixMilli()) { return nil, ErrDeploymentLeaseHeld }
	if err := m.validateTaskLease(ctx, lease); err != nil { return nil, err }
	dep, found, err := m.GetDeployment(account, workspace, lease.DeploymentID)
	if err != nil { return nil, err }
	if !found || !dep.IsUsable() || dep.ReviewExpired(time.Now().UnixMilli()) || (lease.PreparedSource != nil && !lease.PreparedSource.Matches(dep)) { return nil, ErrDeploymentUnusable }
	if err := m.requireDeploymentProvider(dep); err != nil { return nil, err }
	conn, _, err := m.connections.Get(account, workspace, dep.ConnectionID)
	if err != nil { return nil, err }
	prov, _ := m.registry.Get(conn.Kind)
	probeCtx, cancel := context.WithTimeout(ctx, m.probeTimeout)
	defer cancel()
	if err := m.validateTaskLease(probeCtx, lease); err != nil { return nil, err }
	return prov.ResolveAccess(probeCtx, &conn, &dep)
}
