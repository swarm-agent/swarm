package session

import (
	"errors"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Delegated child generation methods keep Task orchestration on the canonical
// session service boundary rather than exposing the raw persistence store.
func (s *Service) GetTurnUsage(sessionID, runID string) (pebblestore.SessionTurnUsageSnapshot, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.SessionTurnUsageSnapshot{}, false, errors.New("session service is not configured")
	}
	return s.store.GetTurnUsage(sessionID, runID)
}

func (s *Service) GetDelegatedChildLineage(accountScopeID, logicalTaskID string) (pebblestore.DelegatedChildLineageRecord, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.DelegatedChildLineageRecord{}, false, errors.New("session service is not configured")
	}
	return s.store.GetDelegatedChildLineage(accountScopeID, logicalTaskID)
}

func (s *Service) GetDelegatedChildGeneration(accountScopeID, logicalTaskID string, generation int) (pebblestore.DelegatedChildGenerationRecord, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.DelegatedChildGenerationRecord{}, false, errors.New("session service is not configured")
	}
	return s.store.GetDelegatedChildGeneration(accountScopeID, logicalTaskID, generation)
}

func (s *Service) GetDelegatedChildGenerationBySession(accountScopeID, sessionID string) (pebblestore.DelegatedChildGenerationRecord, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.DelegatedChildGenerationRecord{}, false, errors.New("session service is not configured")
	}
	return s.store.GetDelegatedChildGenerationBySession(accountScopeID, sessionID)
}

func (s *Service) GetDelegatedChildHandoff(accountScopeID, logicalTaskID string, predecessorGeneration int) (pebblestore.DelegatedChildTargetedHandoff, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.DelegatedChildTargetedHandoff{}, false, errors.New("session service is not configured")
	}
	return s.store.GetDelegatedChildHandoff(accountScopeID, logicalTaskID, predecessorGeneration)
}

func (s *Service) GetDelegatedWorktreeOwner(accountScopeID, workspacePath string) (pebblestore.ManagedWorktreeOwnerLease, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.ManagedWorktreeOwnerLease{}, false, errors.New("session service is not configured")
	}
	return s.store.GetDelegatedWorktreeOwner(accountScopeID, workspacePath)
}

func (s *Service) CreateDelegatedChildLineage(lineage pebblestore.DelegatedChildLineageRecord, generation pebblestore.DelegatedChildGenerationRecord, mutationID string) (pebblestore.DelegatedChildLineageRecord, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.DelegatedChildLineageRecord{}, false, errors.New("session service is not configured")
	}
	return s.store.CreateDelegatedChildLineage(lineage, generation, mutationID)
}

func (s *Service) UpdateDelegatedChildRun(input pebblestore.UpdateDelegatedChildRunInput) (pebblestore.DelegatedChildLineageRecord, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.DelegatedChildLineageRecord{}, false, errors.New("session service is not configured")
	}
	return s.store.UpdateDelegatedChildRun(input)
}

func (s *Service) BeginDelegatedChildRetirement(input pebblestore.RetireDelegatedChildInput) (pebblestore.DelegatedChildLineageRecord, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.DelegatedChildLineageRecord{}, false, errors.New("session service is not configured")
	}
	return s.store.BeginDelegatedChildRetirement(input)
}

func (s *Service) RotateDelegatedChild(input pebblestore.RotateDelegatedChildInput) (pebblestore.DelegatedChildLineageRecord, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.DelegatedChildLineageRecord{}, false, errors.New("session service is not configured")
	}
	return s.store.RotateDelegatedChild(input)
}

func (s *Service) FinishDelegatedChild(input pebblestore.FinishDelegatedChildInput) (pebblestore.DelegatedChildLineageRecord, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.DelegatedChildLineageRecord{}, false, errors.New("session service is not configured")
	}
	return s.store.FinishDelegatedChild(input)
}
