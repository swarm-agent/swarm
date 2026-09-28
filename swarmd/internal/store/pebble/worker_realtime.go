package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/cockroachdb/pebble"
	"github.com/google/uuid"
)

// WorkerUpdatedEventType is the canonical event type for worker invalidation updates.
const WorkerUpdatedEventType = "worker.updated"

// ErrWorkerInvalid indicates that a worker mutation violates domain invariants or scope boundaries.
var ErrWorkerInvalid = errors.New("invalid worker mutation")

// workerRealtimeMutation is a private participant that attaches atomic worker writes
// to the canonical V3 session mutation and realtime outbox.
// Lock order is workersMu -> canonical session mutation lock.
type workerRealtimeMutation struct {
	accountScopeID string
	workerID       string
	writes         map[string][]byte
	deletes        []string
	eventPayload   json.RawMessage
	outbox         *V3RealtimeOutboxRecord
}

func (m *workerRealtimeMutation) put(key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if m.writes == nil {
		m.writes = make(map[string][]byte)
	}
	m.writes[key] = data
	return nil
}

func (m *workerRealtimeMutation) putBytes(key string, data []byte) {
	if m.writes == nil {
		m.writes = make(map[string][]byte)
	}
	m.writes[key] = data
}

func (m *workerRealtimeMutation) delete(key string) {
	m.deletes = append(m.deletes, key)
}

func isAllowedWorkerKey(accountScopeID string, key string) bool {
	accountPart := keyPart(accountScopeID)
	if accountPart == "" {
		return false
	}
	workerPrefix := KeyWorkerAccountPrefix + accountPart + "/"
	historyPrefix := KeyWorkerHistoryAccountPrefix + accountPart + "/"
	automationPrefix := KeyWorkerAutomationAccountPrefix + accountPart + "/"
	runPrefix := KeyWorkerRunAccountPrefix + accountPart + "/"
	runOccPrefix := KeyWorkerRunOccAccountPrefix + accountPart + "/"
	idempPrefix := KeyWorkerIdempotencyAccountPrefix + accountPart + "/"
	return strings.HasPrefix(key, workerPrefix) ||
		strings.HasPrefix(key, historyPrefix) ||
		strings.HasPrefix(key, automationPrefix) ||
		strings.HasPrefix(key, runPrefix) ||
		strings.HasPrefix(key, runOccPrefix) ||
		strings.HasPrefix(key, idempPrefix)
}

func setWorkerRealtimeMutationInBatch(batch *pebble.Batch, account string, m *workerRealtimeMutation) error {
	if m == nil || m.accountScopeID == "" || m.accountScopeID != account || strings.TrimSpace(m.workerID) == "" {
		return ErrWorkerInvalid
	}
	if len(m.writes) == 0 && len(m.deletes) == 0 {
		return ErrWorkerInvalid
	}
	keys := make([]string, 0, len(m.writes))
	for key := range m.writes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		data := m.writes[key]
		if !isAllowedWorkerKey(account, key) || !json.Valid(data) {
			return ErrWorkerInvalid
		}
		if err := batch.Set([]byte(key), data, nil); err != nil {
			return err
		}
	}
	for _, key := range m.deletes {
		if !isAllowedWorkerKey(account, key) {
			return ErrWorkerInvalid
		}
		if err := batch.Delete([]byte(key), nil); err != nil {
			return err
		}
	}
	return nil
}

// SetWorkerPublisher installs an instance-owned wakeup for realtime worker changes.
// Callbacks run after releasing domain and canonical mutation locks.
func (s *Store) SetWorkerPublisher(publish func(V3RealtimeOutboxRecord)) {
	s.workerPublisherMu.Lock()
	defer s.workerPublisherMu.Unlock()
	s.workerPublisher = publish
}

func (s *Store) commitWorkerRealtime(m *workerRealtimeMutation) error {
	requestID := uuid.NewString()
	payload := m.eventPayload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	// Session ID is lowercase to satisfy canonical session validation
	sessionID := fmt.Sprintf("__worker__:%s:%s", strings.ToLower(strings.TrimSpace(m.accountScopeID)), strings.ToLower(strings.TrimSpace(m.workerID)))
	result, err := NewSessionStore(s).ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:       sessionID,
		AccountScopeID:  m.accountScopeID,
		UserID:          "desktop",
		ClientRequestID: requestID,
		IdempotencyKey:  requestID,
		PayloadHash:     requestID,
		Kind:            WorkerUpdatedEventType,
		EventType:       WorkerUpdatedEventType,
		EventPayload:    payload,
		workerRealtime:  m,
	})
	if err != nil {
		return err
	}
	m.outbox = result.RealtimeOutbox
	return nil
}

func (s *Store) publishWorkerRealtime(m *workerRealtimeMutation) {
	if m == nil || m.outbox == nil {
		return
	}
	s.workerPublisherMu.RLock()
	publish := s.workerPublisher
	s.workerPublisherMu.RUnlock()
	if publish != nil {
		publish(*m.outbox)
	}
}
