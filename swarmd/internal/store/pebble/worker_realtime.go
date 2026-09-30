package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

// WorkerUpdatedEventType is the canonical event type for worker invalidation updates.
const WorkerUpdatedEventType = "worker.updated"

// WorkerRealtimePayload conveys invalidation metadata for live worker updates.
type WorkerRealtimePayload struct {
	BudgetRevision uint64               `json:"budget_revision,omitempty"`
	WorkerID       string               `json:"worker_id"`
	Revision       uint64               `json:"revision,omitempty"`
	LifecycleState WorkerLifecycleState `json:"lifecycle_state,omitempty"`
	ChangeSummary  string               `json:"change_summary,omitempty"`
}

// ErrWorkerInvalid indicates that a worker mutation violates domain invariants or scope boundaries.
var ErrWorkerInvalid = errors.New("invalid worker mutation")

// workerRealtimeMutation is a private participant that holds atomic worker writes
// and outbox records committed in a single Pebble batch.
type workerRealtimeMutation struct {
	accountScopeID string
	userID         string
	workerID       string
	writes         map[string][]byte
	deletes        []string
	eventPayload   json.RawMessage
	outbox         *V3RealtimeOutboxRecord
}

func (m *workerRealtimeMutation) setPayload(payload WorkerRealtimePayload) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	m.eventPayload = data
	return nil
}

func (m *workerRealtimeMutation) put(key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > 2*1024*1024 {
		return fmt.Errorf("%w: worker record exceeds 2 MiB", ErrWorkerInvalid)
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
		strings.HasPrefix(key, KeyWorkerRunIdempotencyAccountPrefix+accountPart+"/") ||
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
		if (!isAllowedWorkerKey(account, key) && key != workerBudgetKey(account, m.workerID)) || !json.Valid(data) {
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
	if m == nil || strings.TrimSpace(m.accountScopeID) == "" || strings.TrimSpace(m.workerID) == "" {
		return ErrWorkerInvalid
	}

	reserved, err := s.sessionMutations.reserveOutbox(s, 1)
	if err != nil {
		return err
	}
	endpointSeq := reserved[0]
	now := time.Now().UnixMilli()

	payload := m.eventPayload
	if len(payload) == 0 {
		defaultPayload, _ := json.Marshal(WorkerRealtimePayload{
			WorkerID: m.workerID,
		})
		payload = defaultPayload
	}

	outbox := V3RealtimeOutboxRecord{
		EndpointSeq:    endpointSeq,
		EndpointCursor: V3RealtimeOutboxCursor(endpointSeq),
		SessionID:      "",
		UserID:         strings.TrimSpace(m.userID),
		AccountScopeID: m.accountScopeID,
		Event: V3SessionEvent{
			ID:        fmt.Sprintf("wkevt_%020d", endpointSeq),
			Seq:       endpointSeq,
			EventType: WorkerUpdatedEventType,
			Payload:   payload,
			TsUnixMs:  now,
		},
		CreatedAt: now,
	}

	outboxRaw, err := json.Marshal(outbox)
	if err != nil {
		s.sessionMutations.abandonOutbox(reserved)
		return fmt.Errorf("marshal worker outbox: %w", err)
	}
	outboxRef, err := marshalV3RealtimeOutboxReference(outbox)
	if err != nil {
		s.sessionMutations.abandonOutbox(reserved)
		return fmt.Errorf("marshal worker outbox ref: %w", err)
	}

	batch := s.NewBatch()
	defer batch.Close()

	if err := setWorkerRealtimeMutationInBatch(batch, m.accountScopeID, m); err != nil {
		s.sessionMutations.abandonOutbox(reserved)
		return err
	}

	if err := batch.Set([]byte(KeyV3RealtimeOutbox(endpointSeq)), outboxRaw, nil); err != nil {
		s.sessionMutations.abandonOutbox(reserved)
		return err
	}
	if err := batch.Set([]byte(KeyV3RealtimeOutboxByAuthScope(m.accountScopeID, "", endpointSeq)), outboxRef, nil); err != nil {
		s.sessionMutations.abandonOutbox(reserved)
		return err
	}
	if m.userID != "" {
		if err := batch.Set([]byte(KeyV3RealtimeOutboxByAuthScope(m.accountScopeID, m.userID, endpointSeq)), outboxRef, nil); err != nil {
			s.sessionMutations.abandonOutbox(reserved)
			return err
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		s.sessionMutations.abandonOutbox(reserved)
		return err
	}

	if err := s.sessionMutations.commitOutbox(s, reserved); err != nil {
		return err
	}

	m.outbox = &outbox
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
		defer func() {
			_ = recover() // safe against subscriber panic
		}()
		publish(*m.outbox)
	}
}
