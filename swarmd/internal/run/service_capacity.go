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

// Pending admissions contain cancellation handles only; durable session state
// remains the authority. They let stop cancel a child before it owns a slot.
func (s *Service) registerPendingAdmission(sessionID, runID string, cancel context.CancelFunc) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.pendingAdmissions == nil {
		s.pendingAdmissions = make(map[string]map[string]context.CancelFunc)
	}
	if s.pendingAdmissions[sessionID] == nil {
		s.pendingAdmissions[sessionID] = make(map[string]context.CancelFunc)
	}
	if s.pendingAdmissions[sessionID][runID] != nil {
		return ErrSessionAlreadyActive
	}
	s.pendingAdmissions[sessionID][runID] = cancel
	return nil
}
func (s *Service) removePendingAdmission(sessionID, runID string) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	delete(s.pendingAdmissions[sessionID], runID)
	if len(s.pendingAdmissions[sessionID]) == 0 {
		delete(s.pendingAdmissions, sessionID)
	}
}
