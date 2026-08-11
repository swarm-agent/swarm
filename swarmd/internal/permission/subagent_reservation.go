package permission

import (
	"errors"
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type SubagentReservationDecision string

const (
	SubagentReservationApprove SubagentReservationDecision = "approve"
	SubagentReservationAsk     SubagentReservationDecision = "ask"
	SubagentReservationDeny    SubagentReservationDecision = "deny"
)

type SubagentReservationRequest struct {
	SessionID      string
	AccountScopeID string
	RunID          string
	CallID         string
	ManifestHash   string
	LaunchCount    int
	SwarmMode      bool
	Delegated      bool
}

type SubagentReservationResult struct {
	Decision    SubagentReservationDecision
	Reason      string
	Reservation pebblestore.SubagentWaveReservation
}

// ReserveSubagentWave atomically resolves and persists one complete wave before
// any child session or worktree is created. The service mutex serializes racing
// calls while Pebble makes the resulting accounting durable across restarts.
func (s *Service) ReserveSubagentWave(request SubagentReservationRequest) (SubagentReservationResult, error) {
	if s == nil || s.store == nil {
		return SubagentReservationResult{}, errors.New("permission service is not configured")
	}
	request.SessionID = strings.TrimSpace(request.SessionID)
	request.RunID = strings.TrimSpace(request.RunID)
	request.CallID = strings.TrimSpace(request.CallID)
	request.ManifestHash = strings.TrimSpace(request.ManifestHash)
	if request.SessionID == "" || request.RunID == "" || request.CallID == "" || request.ManifestHash == "" {
		return SubagentReservationResult{}, errors.New("subagent reservation requires session, run, call, and manifest IDs")
	}
	if request.LaunchCount < 1 {
		return SubagentReservationResult{}, errors.New("subagent wave must contain at least one launch")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok, err := s.store.GetSubagentWaveReservation(request.SessionID, request.RunID, request.CallID); err != nil {
		return SubagentReservationResult{}, err
	} else if ok {
		if existing.ManifestHash != request.ManifestHash || existing.LaunchCount != request.LaunchCount {
			return SubagentReservationResult{}, errors.New("task call already reserved with a different exact wave")
		}
		return reservationResult(existing), nil
	}
	// Reservation limits must be authoritative at call time. Reload the
	// account policy so an edit made after this run started immediately applies
	// to both the automatic wave budget and active-child concurrency ceiling.
	state, err := s.refreshPermissionStatePolicyLocked(request.AccountScopeID)
	if err != nil {
		return SubagentReservationResult{}, err
	}
	policy := NormalizePolicy(state.Policy).Subagents
	launchLimit := policy.ActiveChildLimit
	limitLabel := "default subagent limit"
	if request.SwarmMode {
		launchLimit = policy.SwarmActiveChildLimit
		limitLabel = "swarm-mode subagent limit"
	}
	decision := SubagentReservationApprove
	reason := "wave is within the bounded automatic delegation allowance"
	if request.Delegated {
		decision, reason = SubagentReservationDeny, "task delegation is parent-only; child sessions cannot delegate"
	} else if request.LaunchCount > launchLimit {
		decision, reason = SubagentReservationDeny, fmt.Sprintf("subagent wave exceeds %s of %d", limitLabel, launchLimit)
	} else if policy.Mode == SubagentModeDirect {
		decision, reason = SubagentReservationDeny, "direct orchestration mode denies delegation"
	} else if policy.Mode == SubagentModeAsk {
		decision, reason = SubagentReservationAsk, "ask orchestration mode reviews every exact wave"
	}
	reservations, err := s.store.ListSubagentWaveReservations(request.SessionID, request.RunID)
	if err != nil {
		return SubagentReservationResult{}, err
	}
	automaticWaves, activeChildren := 0, 0
	for _, reservation := range reservations {
		if reservation.Status == "denied" {
			continue
		}
		// Each accepted task call consumes exactly one automatic wave, regardless
		// of how many children it launches. Completion releases only concurrency.
		automaticWaves++
		// Regular and swarm calls have independent configured concurrency pools.
		// Legacy records omit SwarmMode and therefore remain regular reservations.
		if reservation.SwarmMode == request.SwarmMode {
			activeChildren += reservation.ActiveCount
		}
	}
	if decision != SubagentReservationDeny && activeChildren+request.LaunchCount > launchLimit {
		decision, reason = SubagentReservationDeny, fmt.Sprintf("%s would be exceeded by active child concurrency", limitLabel)
	}
	if decision == SubagentReservationApprove && automaticWaves >= policy.AutomaticLaunchesPerParentRun {
		if policy.OverBudgetAction == SubagentOverBudgetDeny {
			decision, reason = SubagentReservationDeny, "automatic wave budget is exhausted"
		} else {
			decision, reason = SubagentReservationAsk, "wave requires approval because the automatic wave budget is exhausted"
		}
	}
	status := string(decision)
	record := pebblestore.SubagentWaveReservation{SessionID: request.SessionID, RunID: request.RunID, CallID: request.CallID, ManifestHash: request.ManifestHash, LaunchCount: request.LaunchCount, SwarmMode: request.SwarmMode, Status: status}
	if decision != SubagentReservationDeny {
		record.ActiveCount = request.LaunchCount
	}
	if err := s.store.PutSubagentWaveReservation(record); err != nil {
		return SubagentReservationResult{}, err
	}
	return SubagentReservationResult{Decision: decision, Reason: reason, Reservation: record}, nil
}

func reservationResult(record pebblestore.SubagentWaveReservation) SubagentReservationResult {
	decision := SubagentReservationDecision(record.Status)
	return SubagentReservationResult{Decision: decision, Reason: "idempotent task wave reservation", Reservation: record}
}

func (s *Service) FinishSubagentWave(sessionID, runID, callID, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok, err := s.store.GetSubagentWaveReservation(sessionID, runID, callID)
	if err != nil || !ok {
		return err
	}
	record.ActiveCount = 0
	record.Status = strings.TrimSpace(status)
	return s.store.PutSubagentWaveReservation(record)
}
