package lifecycle

import (
	"context"
	"errors"

	"swarm-refactor/swarmtui/pkg/environments"
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
