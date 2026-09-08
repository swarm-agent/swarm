package session

import (
	"errors"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

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
