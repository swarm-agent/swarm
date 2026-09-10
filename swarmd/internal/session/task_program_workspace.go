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
	lanes := []pebblestore.TaskProgramRepositoryLane{}
	for _, record := range records {
		if record.State == pebblestore.TaskProgramStateRunning {
			reconciled, ok, err := s.GetTaskProgram(parentID, record.ProgramID)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, errors.New("task program disappeared during lane lookup")
			}
			record = reconciled
		}
		if record.ParentSessionID == parentID && record.RepositoryLane != nil {
			if record.State == pebblestore.TaskProgramStateRunning {
				return nil, errors.New("another Task Program owns an active repository lane")
			}
			lanes = append(lanes, *record.RepositoryLane)
		}
	}
	return lanes, nil
}
