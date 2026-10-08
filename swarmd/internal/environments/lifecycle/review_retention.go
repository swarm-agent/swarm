package lifecycle

import (
	"context"
	"errors"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
)

const ManagedReviewRetention = environments.ManagedReviewRetention

func ManagedReviewExpired(dep environments.Deployment, now int64) bool { return dep.ReviewExpired(now) }

type reviewPager interface {
	ReviewPage(context.Context, string) ([]environments.Deployment, string, error)
}

// RunReviewCleanup performs one bounded page immediately (including on restart),
// then advances every minute. One goroutine, no fanout, no task/status polling.
// Failed stops remain eligible on the next traversal; deadlines never move.
func (m *DeploymentManager) RunReviewCleanup(ctx context.Context, report func(error)) {
	pager, ok := m.deployments.(reviewPager)
	if !ok {
		if report != nil {
			report(errors.New("review cleanup pagination unavailable"))
		}
		return
	}
	cursor := ""
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.rootCtx.Done():
			return
		case <-timer.C:
		}
		pageCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		deps, next, err := pager.ReviewPage(pageCtx, cursor)
		if err == nil {
			for _, dep := range deps {
				_, cleanupErr := m.cleanupReviewDeployment(pageCtx, dep)
				err = errors.Join(err, cleanupErr)
				if pageCtx.Err() != nil {
					break
				}
			}
			if pageCtx.Err() == nil {
				cursor = next
			}
		}
		cancel()
		if err != nil && report != nil {
			report(err)
		}
		timer.Reset(time.Minute)
	}
}

func (m *DeploymentManager) CleanupReviewDeployments(ctx context.Context, account, workspace string) ([]string, error) {
	if m == nil || m.deployments == nil {
		return nil, errors.New("deployment store unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	deps, err := m.deployments.List(account, workspace, 64)
	if err != nil {
		return nil, err
	}
	var stopped []string
	for _, dep := range deps {
		didStop, e := m.cleanupReviewDeployment(ctx, dep)
		err = errors.Join(err, e)
		if didStop {
			stopped = append(stopped, dep.ID)
		}
		if ctx.Err() != nil {
			break
		}
	}
	return stopped, err
}

func (m *DeploymentManager) cleanupReviewDeployment(ctx context.Context, candidate environments.Deployment) (bool, error) {
	if !candidate.ReviewExpired(time.Now().UnixMilli()) || candidate.Status == environments.DeploymentStatusStopped || candidate.Status == environments.DeploymentStatusTerminated {
		return false, nil
	}
	lock := m.getEnvLock(candidate.AccountScopeID, candidate.WorkspaceID, candidate.EnvironmentID)
	if err := lock.Lock(ctx); err != nil {
		return false, err
	}
	defer lock.Unlock()
	dep, found, err := m.deployments.Get(candidate.AccountScopeID, candidate.WorkspaceID, candidate.ID)
	if err != nil {
		return false, err
	}
	if !found || dep.CreatedAt != candidate.CreatedAt || dep.Runtime.ContainerID != candidate.Runtime.ContainerID || !dep.ReviewExpired(time.Now().UnixMilli()) {
		return false, nil
	}
	if err := m.guardDeploymentIdle(ctx, dep); err != nil {
		if errors.Is(err, ErrDeploymentLeaseHeld) || errors.Is(err, environments.ErrDeploymentOperationConflict) {
			return false, nil
		}
		return false, err
	}
	if err := m.stopDeploymentLocked(ctx, dep.AccountScopeID, dep.WorkspaceID, dep.ID); err != nil {
		return false, err
	}
	return true, nil
}

// Called under lifecycle locks. An operation may mutate only its own target;
// independent active and unresolved operations always prevent destructive work.
func (m *DeploymentManager) guardDeploymentIdle(ctx context.Context, dep environments.Deployment) error {
	lease, found, err := m.deployments.GetActiveLease(dep.AccountScopeID, dep.WorkspaceID, dep.ID)
	if err != nil {
		return err
	}
	if found && lease.IsHeld(time.Now().UnixMilli()) {
		return ErrDeploymentLeaseHeld
	}
	if m.operations != nil {
		op, active, err := m.operations.GetActiveOperationForDeployment(dep.AccountScopeID, dep.WorkspaceID, dep.ID)
		if err != nil {
			return err
		}
		if active && op.OperationID != operationIDFromContext(ctx) {
			return environments.ErrDeploymentOperationConflict
		}
	}
	return ctx.Err()
}

func (m *DeploymentManager) requireDeploymentProvider(dep environments.Deployment) error {
	if m.connections == nil || m.registry == nil {
		return errors.New("provider unavailable")
	}
	conn, found, err := m.connections.Get(dep.AccountScopeID, dep.WorkspaceID, dep.ConnectionID)
	if err != nil {
		return err
	}
	if !found {
		return ErrConnectionNotFound
	}
	if dep.Build != nil && (dep.Build.ConnectionID != conn.ID || dep.Build.ConnectionDigest != environments.ConnectionTransportDigest(&conn)) {
		return errors.New("deployment build connection provenance changed")
	}
	if _, ok := m.registry.Get(conn.Kind); !ok {
		return ErrProviderNotRegistered
	}
	return nil
}
