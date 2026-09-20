package run

import (
	"context"

	"swarm/packages/swarmd/internal/executioncapacity"
)

// runWithParkedExecutionLease parks any active execution lease found in ctx for sessionID and runID
// before executing fn, and reacquires it when fn finishes. If the lease is already parked or absent,
// fn is executed directly without double-parking.
func runWithParkedExecutionLease(ctx context.Context, sessionID, runID string, fn func() (string, error)) (res string, err error) {
	lease, ok := executioncapacity.LeaseFromContext(ctx, sessionID, runID)
	if !ok || lease == nil || lease.IsParked() {
		return fn()
	}
	if parkErr := lease.Park(); parkErr != nil {
		return "", parkErr
	}
	defer func() {
		if reacquireErr := lease.Reacquire(ctx); reacquireErr != nil && err == nil {
			err = reacquireErr
		}
	}()
	return fn()
}
