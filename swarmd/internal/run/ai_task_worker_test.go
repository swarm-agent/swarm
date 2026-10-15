package run

import (
	"errors"

	"sync"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type recordingAITaskV2Store struct {
	mu          sync.Mutex
	recovery    []pebblestore.AITaskV2QueueRecord
	transitions []AITaskV2Transition
	deleted     []string
	started     chan string
}

func (s *recordingAITaskV2Store) LoadAITaskV2RecoveryQueue(int) ([]pebblestore.AITaskV2QueueRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]pebblestore.AITaskV2QueueRecord(nil), s.recovery...), nil
}

func (s *recordingAITaskV2Store) DeleteAITaskV2QueueRecord(key string) error {
	s.mu.Lock()
	s.deleted = append(s.deleted, key)
	s.mu.Unlock()
	return nil
}

func (s *recordingAITaskV2Store) AppendAITaskAudit(_, _, _ string, _ pebblestore.AITaskAuditRecord) error {
	return nil
}

func (s *recordingAITaskV2Store) TransitionAITaskV2(input AITaskV2Transition) (pebblestore.WorkspaceTodoItem, error) {
	s.mu.Lock()
	s.transitions = append(s.transitions, input)
	s.mu.Unlock()
	if s.started != nil {
		select {
		case s.started <- input.Item.ID:
		default:
		}
	}
	return pebblestore.WorkspaceTodoItem{}, errors.New("stop before deployment")
}
