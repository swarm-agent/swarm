package session

import (
	"errors"
	"strings"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// InspectTaskProgram reads without owner-run reconciliation. Repository inventory
// must not mutate programs while presenting retained worker lifecycle evidence.
func (s *Service) InspectTaskProgram(parentSessionID, programID string) (pebblestore.TaskProgramRecord, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.TaskProgramRecord{}, false, errors.New("session service is not configured")
	}
	return s.store.GetTaskProgram(strings.TrimSpace(parentSessionID), strings.TrimSpace(programID))
}
