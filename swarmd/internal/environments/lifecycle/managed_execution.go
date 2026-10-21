package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"path"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/provider"
)

// ExecTaskLease consumes an existing exact task receipt. It never provisions or
// substitutes another deployment. The first supported Bash backend is local
// Podman; provider supervision owns descendant cancellation and bounded output.
func (m *DeploymentManager) ExecTaskLease(ctx context.Context, account, workspace, leaseID string, attribution environments.OperationAttribution, req provider.ExecRequest) (executionResult *provider.ExecResult, executionErr error) {
	access, err := m.ResolveLeaseAccess(ctx, account, workspace, leaseID, attribution)
	if err != nil {
		return nil, err
	}
	if access == nil || !access.ExecSupported || !path.IsAbs(access.RemoteWorkspacePath) {
		return nil, errors.New("managed execution workspace unavailable")
	}
	lease, found, err := m.GetLease(account, workspace, leaseID)
	if err != nil {
		return nil, err
	}
	if !found || lease.TaskBinding == nil || lease.PreparedSource == nil || !ownsLease(attribution, lease) || !lease.IsHeld(time.Now().UnixMilli()) {
		return nil, ErrDeploymentLeaseHeld
	}
	dep, found, err := m.GetDeployment(account, workspace, lease.DeploymentID)
	if err != nil {
		return nil, err
	}
	if !found || !lease.PreparedSource.Matches(dep) || dep.ReviewExpired(time.Now().UnixMilli()) {
		return nil, ErrDeploymentUnusable
	}
	conn, found, err := m.connections.Get(account, workspace, dep.ConnectionID)
	if err != nil {
		return nil, err
	}
	if !found || conn.Kind != environments.ConnectionKindLocalPodman {
		return nil, errors.New("managed Bash currently requires local_podman")
	}
	if err := m.validateTaskLease(ctx, lease); err != nil {
		return nil, err
	}
	// The provider's checkout, never the host worktree or a model-supplied cwd.
	// Submit owns durable busy guards, provider-boundary revalidation, progress,
	// deadline, cancellation and reconciliation. Do not call prov.Exec directly.
	op, err := m.Submit(ctx, SubmitOperationRequest{Action: environments.OperationActionExec,
		AccountScopeID: account, WorkspaceID: workspace, DeploymentID: dep.ID, LeaseID: leaseID,
		Attribution: attribution, Command: req.Command, WorkingDir: access.RemoteWorkspacePath,
		Timeout: req.Timeout, MaxOutput: req.MaxOutput,
	})
	if err != nil {
		return nil, err
	}
	defer func() {
		if executionResult == nil {
			executionResult = &provider.ExecResult{OperationID: op.OperationID, ExitCode: -1}
		}
		if op != nil && op.IsActive() {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.cleanupTimeout)
			defer cancel()
			_, cleanupErr := m.Cancel(cleanupCtx, CancelOperationRequest{AccountScopeID: account, WorkspaceID: workspace, OperationID: op.OperationID, Reason: "Bash waiter ended before terminal receipt"})
			executionErr = errors.Join(executionErr, cleanupErr)
		}
		if executionErr != nil {
			executionErr = fmt.Errorf("managed operation %s: %w", op.OperationID, executionErr)
		}
	}()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	nextValidation := time.Now().Add(time.Second)
	for {
		if !op.IsActive() {
			result := &provider.ExecResult{OperationID: op.OperationID, ExitCode: op.Result.ExitCode, Stdout: op.Result.Stdout, Stderr: op.Result.Stderr, Truncated: op.Result.Truncated}
			switch op.Status {
			case environments.OperationStatusSucceeded:
				return result, nil
			case environments.OperationStatusTimedOut:
				return result, provider.ErrOperationTimedOut
			case environments.OperationStatusCancelled:
				return result, provider.ErrOperationCancelled
			case environments.OperationStatusFailed:
				if op.Result.ExitCode != 0 && op.Result.FailureKind == "non_zero_exit" {
					return result, nil
				}
			}
			return result, errors.New("managed operation " + string(op.Status) + ": " + op.Result.ErrorMessage)
		}
		select {
		case <-ctx.Done():
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.cleanupTimeout)
			cancelled, cancelErr := m.Cancel(cleanupCtx, CancelOperationRequest{AccountScopeID: account, WorkspaceID: workspace, OperationID: op.OperationID, Reason: "Bash caller cancelled"})
			if cancelled != nil {
				op = cancelled
			}
			cancel()
			return nil, errors.Join(ctx.Err(), cancelErr)
		case <-ticker.C:
			if !time.Now().Before(nextValidation) {
				nextValidation = time.Now().Add(time.Second)
				currentLease, held, checkErr := m.GetLease(account, workspace, leaseID)
				if checkErr == nil && (!held || !currentLease.IsHeld(time.Now().UnixMilli())) {
					checkErr = errors.New("managed execution lease expired or released during command")
				}
				if checkErr == nil {
					checkErr = m.validateActiveTaskLease(ctx, currentLease)
				}
				if checkErr != nil {
					cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.cleanupTimeout)
					cancelled, cancelErr := m.Cancel(cleanupCtx, CancelOperationRequest{AccountScopeID: account, WorkspaceID: workspace, OperationID: op.OperationID, Reason: "managed lease invalidated during command"})
					cancel()
					if cancelled != nil {
						op = cancelled
					}
					return nil, errors.Join(checkErr, cancelErr)
				}
			}
			current, found, err := m.Get(ctx, account, workspace, op.OperationID)
			if err != nil {
				return nil, err
			}
			if !found {
				return nil, errors.New("managed operation disappeared")
			}
			op = &current
		}
	}
}
