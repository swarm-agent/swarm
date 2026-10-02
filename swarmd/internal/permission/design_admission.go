package permission

import (
	"encoding/json"
	"errors"
	"sync"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

var ErrDesignAdmissionDenied = errors.New("design execution denied by orchestration policy")

// TryAdmitDesign reserves a daemon-owned candidate slot in the account Swarm
// pool. A nil release leaves the request in its durable queue. Slots cover repair
// attempts as well as execution-capacity waits; restart reconciles bound attempts
// rather than replaying them, so these process-lifetime slots need no replay.
func (s *Service) TryAdmitDesign(accountID, sessionID, runID, candidateID string) (release func(), err error) {
	if s == nil || s.store == nil {
		return nil, errors.New("permission service is not configured")
	}
	s.designAdmissionMu.Lock()
	defer s.designAdmissionMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.refreshPermissionStatePolicyLocked(accountID)
	if err != nil {
		return nil, err
	}
	policy := NormalizePolicy(state.Policy).Subagents
	args, err := json.Marshal(map[string]string{"action": "execute", "candidate_id": candidateID})
	if err != nil {
		return nil, err
	}
	if policy.Mode == SubagentModeDirect || ExplainPolicy("auto", "manage_design", string(args), state.Policy).Decision == PolicyDecisionDeny {
		return nil, ErrDesignAdmissionDenied
	}
	callID := "design-admission-" + candidateID
	record, found, err := s.findByRunAndCallLocked(sessionID, runID, callID)
	if err != nil {
		return nil, err
	}
	if found || policy.Mode == SubagentModeAsk {
		if !found {
			// CreatePending owns the same mutex. designAdmissionMu prevents
			// duplicate prompts; persisted call identity is reused after restart.
			s.mu.Unlock()
			_, err = s.CreatePending(CreateInput{SessionID: sessionID, RunID: runID, CallID: callID, ToolName: "manage_design", ToolArguments: string(args), Requirement: "subagent", Mode: "auto"})
			s.mu.Lock()
			return nil, err
		}
		if record.Status == pebblestore.PermissionStatusPending {
			return nil, nil
		}
		if record.Status != pebblestore.PermissionStatusApproved {
			return nil, ErrDesignAdmissionDenied
		}
	}
	active, err := s.accountSwarmChildrenLocked(accountID)
	if err != nil {
		return nil, err
	}
	if active+len(s.designSlots[accountID]) >= policy.SwarmActiveChildLimit {
		return nil, nil
	}
	if s.designSlots == nil {
		s.designSlots = make(map[string]map[string]struct{})
	}
	if s.designSlots[accountID] == nil {
		s.designSlots[accountID] = make(map[string]struct{})
	}
	if _, exists := s.designSlots[accountID][candidateID]; exists {
		return nil, nil
	}
	s.designSlots[accountID][candidateID] = struct{}{}
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			delete(s.designSlots[accountID], candidateID)
			if len(s.designSlots[accountID]) == 0 {
				delete(s.designSlots, accountID)
			}
		})
	}, nil
}
