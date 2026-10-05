package lifecycle

import (
	"context"
	"errors"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
)

const ManagedReviewRetention = 24 * time.Hour

// CleanupReviewDeployments is an explicit bounded cleanup sweep, not a polling
// loop. Managed deployments have a finite review hold measured from creation.
// Live leases and operations postpone cleanup; attachment expiry is unrelated.
func (m *DeploymentManager) CleanupReviewDeployments(ctx context.Context, account, workspace string) ([]string, error) {
	if m == nil || m.deployments == nil { return nil, errors.New("deployment store unavailable") }
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	deps, err := m.deployments.List(account, workspace, 1000)
	if err != nil { return nil, err }
	var stopped []string
	for _, dep := range deps {
		if err := ctx.Err(); err != nil { return stopped, err }
		if dep.Build == nil || !dep.IsActive() || dep.CreatedAt <= 0 || time.Now().UnixMilli() < dep.CreatedAt + ManagedReviewRetention.Milliseconds() { continue }
		// Ensure/acquire serialize on the environment lock before the deployment lock.
		lock := m.getEnvLock(account, workspace, dep.EnvironmentID)
		if err := lock.Lock(ctx); err != nil { return stopped, err }
		err := func() error {
			defer lock.Unlock()
			lease, found, err := m.deployments.GetActiveLease(account, workspace, dep.ID)
			if err != nil { return err }
			if found && lease.IsHeld(time.Now().UnixMilli()) { return nil }
			if m.operations != nil {
				_, active, err := m.operations.GetActiveOperationForDeployment(account, workspace, dep.ID)
				if err != nil { return err }
				if active { return nil }
			}
			if err := m.StopDeployment(ctx, account, workspace, dep.ID); err != nil { return err }
			stopped = append(stopped, dep.ID)
			return nil
		}()
		if err != nil { return stopped, err }
	}
	return stopped, nil
}

// ManagedReviewExpired prevents a new consumer from extending an expired hold.
// Existing consumers remain releasable and are never stopped by attachment expiry.
func ManagedReviewExpired(dep environments.Deployment, now int64) bool {
	return dep.Build != nil && dep.CreatedAt > 0 && now >= dep.CreatedAt + ManagedReviewRetention.Milliseconds()
}
