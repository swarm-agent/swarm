package session

import (
	"errors"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Service) GetTaskProgram(parentSessionID, programID string) (pebblestore.TaskProgramRecord, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.TaskProgramRecord{}, false, errors.New("session service is not configured")
	}
	return s.store.GetTaskProgram(strings.TrimSpace(parentSessionID), strings.TrimSpace(programID))
}

func (s *Service) CreateTaskProgram(record pebblestore.TaskProgramRecord) (pebblestore.TaskProgramRecord, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.TaskProgramRecord{}, false, errors.New("session service is not configured")
	}
	return s.store.CreateTaskProgram(record)
}

func (s *Service) TransitionTaskProgram(parentSessionID, programID string, transition pebblestore.TaskProgramTransition) (pebblestore.TaskProgramRecord, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.TaskProgramRecord{}, false, errors.New("session service is not configured")
	}
	return s.store.TransitionTaskProgram(strings.TrimSpace(parentSessionID), strings.TrimSpace(programID), transition)
}
