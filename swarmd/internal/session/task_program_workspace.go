package session

import (
	"errors"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// EnsureWorkspaceTransitionIdle is a read-only scheduling guard. Even a running
// program without an allocated repository lane can still launch a job against
// its captured parent identity. Admission must not reconcile or mutate it.
func (s *Service) EnsureWorkspaceTransitionIdle(parentID string) error {
	if s == nil || s.store == nil {
		return errors.New("task program store unavailable")
	}
	records, err := s.store.ListTaskPrograms(parentID)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.State == pebblestore.TaskProgramStateRunning || record.State == pebblestore.TaskProgramStateDeclared {
			for _, job := range record.Definition.Jobs {
				// Managed Designers have no mutable checkout assignment. Finder
				// and workspace jobs still capture repository discovery identity.
				if job.AgentType == "designer" && (job.OutputMode == "" || job.OutputMode == "managed") {
					continue
				}
				return errors.New("workspace transition is pinned by an active Task Program repository assignment; finish or stop scheduling first")
			}
		}
	}
	return nil
}

// TaskProgramRepositoryLanes returns only destinations already persisted by the
// scheduler for this parent. Callers must independently authorize their source.
func (s *Service) TaskProgramRepositoryLanes(parentID string) ([]pebblestore.TaskProgramRepositoryLane, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("task program store unavailable")
	}
	records, err := s.store.ListTaskPrograms(parentID)
	if err != nil {
		return nil, err
	}
	return taskProgramRepositoryLanes(records, parentID, nil)
}

// TaskProgramRepositoryLanesForAdmission is scheduler-only: the exact persisted
// admission may look up retained lanes without rejecting itself. Ordinary tools
// and workspace transitions must still use TaskProgramRepositoryLanes. This is
// read-only and never reconciles a competing scheduler or a terminal owner run.
func (s *Service) TaskProgramRepositoryLanesForAdmission(admission pebblestore.TaskProgramRecord) ([]pebblestore.TaskProgramRepositoryLane, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("task program store unavailable")
	}
	if admission.ParentSessionID == "" || admission.ProgramID == "" || admission.ReservationRunID == "" || admission.Revision == 0 {
		return nil, errors.New("task program admission identity is incomplete")
	}
	intent, found, err := s.store.GetV3SessionRunIntent(admission.ParentSessionID, admission.ReservationRunID)
	if err != nil {
		return nil, err
	}
	if found {
		switch intent.Status {
		case pebblestore.V3RunIntentCompleted, pebblestore.V3RunIntentFailed, pebblestore.V3RunIntentCancelled, pebblestore.V3RunIntentExpired, pebblestore.V3RunIntentInterrupted:
			return nil, errors.New("task program repository admission owner run ended")
		}
	}
	records, err := s.store.ListTaskPrograms(admission.ParentSessionID)
	if err != nil {
		return nil, err
	}
	return taskProgramRepositoryLanes(records, admission.ParentSessionID, &admission)
}

func taskProgramRepositoryLanes(records []pebblestore.TaskProgramRecord, parentID string, admission *pebblestore.TaskProgramRecord) ([]pebblestore.TaskProgramRepositoryLane, error) {
	lanes := []pebblestore.TaskProgramRepositoryLane{}
	found := admission == nil
	for _, record := range records {
		if record.ParentSessionID != parentID {
			return nil, errors.New("task program repository parent mismatch")
		}
		active := record.State == pebblestore.TaskProgramStateRunning || record.State == pebblestore.TaskProgramStateDeclared
		if admission != nil && record.ProgramID == admission.ProgramID {
			if !active || record.Revision != admission.Revision || record.ReservationRunID != admission.ReservationRunID || record.DefinitionHash != admission.DefinitionHash {
				return nil, errors.New("task program repository admission is stale")
			}
			if (record.RepositoryLane == nil) != (admission.RepositoryLane == nil) || (record.RepositoryLane != nil && *record.RepositoryLane != *admission.RepositoryLane) {
				return nil, errors.New("task program repository admission lane mismatch")
			}
			found = true
		} else if active {
			return nil, errors.New("active Task Program requires explicit reconciliation before lane lookup")
		}
		if record.RepositoryLane != nil {
			lanes = append(lanes, *record.RepositoryLane)
		}
	}
	if !found {
		return nil, errors.New("task program repository admission is missing")
	}
	return lanes, nil
}
