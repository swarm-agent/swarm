package permission

import (
	"errors"
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type SessionDeployReservationDecision string

const (
	SessionDeployReservationApprove SessionDeployReservationDecision = "approve"
	SessionDeployReservationAsk     SessionDeployReservationDecision = "ask"
	SessionDeployReservationDeny    SessionDeployReservationDecision = "deny"
)

type SessionDeployReservationRequest struct {
	SessionID      string
	AccountScopeID string
	RunID          string
	CallID         string
	ManifestHash   string
	DeployCount    int
}

type SessionDeployReservationResult struct {
	Decision    SessionDeployReservationDecision
	Reason      string
	Reservation pebblestore.SessionDeployReservation
}

// ReserveSessionDeploy atomically accounts for selected deployments before any
// child session or worktree is created.
func (s *Service) ReserveSessionDeploy(request SessionDeployReservationRequest) (SessionDeployReservationResult, error) {
	if s == nil || s.store == nil {
		return SessionDeployReservationResult{}, errors.New("permission service is not configured")
	}
	request.SessionID = strings.TrimSpace(request.SessionID)
	request.RunID = strings.TrimSpace(request.RunID)
	request.CallID = strings.TrimSpace(request.CallID)
	request.ManifestHash = strings.TrimSpace(request.ManifestHash)
	if request.SessionID == "" || request.RunID == "" || request.CallID == "" || request.ManifestHash == "" {
		return SessionDeployReservationResult{}, errors.New("session deployment reservation requires session, run, call, and manifest IDs")
	}
	if request.DeployCount < 1 {
		return SessionDeployReservationResult{}, errors.New("session deployment reservation requires at least one deployment")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.refreshPermissionStatePolicyLocked(request.AccountScopeID)
	if err != nil {
		return SessionDeployReservationResult{}, err
	}
	policy := NormalizePolicy(state.Policy).SessionDeploy
	if existing, ok, err := s.store.GetSessionDeployReservation(request.SessionID, request.RunID, request.CallID); err != nil {
		return SessionDeployReservationResult{}, err
	} else if ok {
		if existing.ManifestHash != request.ManifestHash || existing.DeployCount != request.DeployCount {
			return SessionDeployReservationResult{}, errors.New("manage-sessions deploy call already reserved with different canonical arguments")
		}
		if existing.Status == string(SessionDeployReservationAsk) && policy.Mode == CapabilityModeAlwaysAllow {
			existing.Status = string(SessionDeployReservationApprove)
			if err := s.store.PutSessionDeployReservation(existing); err != nil {
				return SessionDeployReservationResult{}, err
			}
			return SessionDeployReservationResult{Decision: SessionDeployReservationApprove, Reason: "session deployment capability is always allowed", Reservation: existing}, nil
		}
		return sessionDeployReservationResult(existing), nil
	}
	decision := SessionDeployReservationAsk
	reason := "session deployment policy requires approval"
	switch policy.Mode {
	case CapabilityModeAlwaysAllow:
		decision, reason = SessionDeployReservationApprove, "session deployment capability is always allowed"
	case CapabilityModeBounded:
		reservations, err := s.store.ListSessionDeployReservations(request.SessionID, request.RunID)
		if err != nil {
			return SessionDeployReservationResult{}, err
		}
		total := 0
		for _, reservation := range reservations {
			if reservation.Status == string(SessionDeployReservationApprove) {
				total += reservation.DeployCount
			}
		}
		if total+request.DeployCount <= policy.AutomaticDeploymentsPerParentRun {
			decision, reason = SessionDeployReservationApprove, "deployment is within the bounded automatic allowance"
		} else if policy.OverLimitAction == SessionDeployOverLimitDeny {
			decision, reason = SessionDeployReservationDeny, fmt.Sprintf("automatic deployment limit of %d is exhausted", policy.AutomaticDeploymentsPerParentRun)
		} else {
			decision, reason = SessionDeployReservationAsk, fmt.Sprintf("deployment exceeds the automatic limit of %d", policy.AutomaticDeploymentsPerParentRun)
		}
	}
	record := pebblestore.SessionDeployReservation{SessionID: request.SessionID, RunID: request.RunID, CallID: request.CallID, ManifestHash: request.ManifestHash, DeployCount: request.DeployCount, Status: string(decision)}
	if err := s.store.PutSessionDeployReservation(record); err != nil {
		return SessionDeployReservationResult{}, err
	}
	return SessionDeployReservationResult{Decision: decision, Reason: reason, Reservation: record}, nil
}

func sessionDeployReservationResult(record pebblestore.SessionDeployReservation) SessionDeployReservationResult {
	return SessionDeployReservationResult{Decision: SessionDeployReservationDecision(record.Status), Reason: "idempotent session deployment reservation", Reservation: record}
}

// MarkSessionDeployApproved promotes an ask reservation after the user approved
// the exact canonical manifest so subsequent bounded calls count it.
func (s *Service) MarkSessionDeployApproved(sessionID, runID, callID string, deployCount int) error {
	if s == nil || s.store == nil {
		return errors.New("permission service is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok, err := s.store.GetSessionDeployReservation(strings.TrimSpace(sessionID), strings.TrimSpace(runID), strings.TrimSpace(callID))
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("session deployment reservation was not found")
	}
	if deployCount > 0 {
		record.DeployCount = deployCount
	}
	record.Status = string(SessionDeployReservationApprove)
	return s.store.PutSessionDeployReservation(record)
}
