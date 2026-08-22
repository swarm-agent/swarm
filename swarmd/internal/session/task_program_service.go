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

func (s *Service) GetTaskProgramHandoffMessage(sessionID string, globalSeq uint64) (pebblestore.MessageSnapshot, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.MessageSnapshot{}, false, errors.New("session service is not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || globalSeq == 0 {
		return pebblestore.MessageSnapshot{}, false, errors.New("session id and message global seq are required")
	}
	messages, err := s.store.ListV3SessionMessages(sessionID, globalSeq-1, 1)
	if err != nil {
		return pebblestore.MessageSnapshot{}, false, err
	}
	if len(messages) != 1 || messages[0].GlobalSeq != globalSeq {
		return pebblestore.MessageSnapshot{}, false, nil
	}
	return messages[0], true, nil
}
