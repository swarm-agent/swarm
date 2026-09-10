package pebblestore

import (
	"encoding/json"
	"strings"
	"sort"

	"github.com/cockroachdb/pebble"
	"github.com/google/uuid"
)

const AutomationChangedEventType = "automation.updated"

// Private participant: only automation authorities prepare these writes while
// holding automationsMu. Lock order is automation -> canonical session mutation;
// the canonical participant never acquires automationsMu or calls domain code.
type automationRealtimeMutation struct {
	scope AutomationScope
	writes map[string]json.RawMessage
	outbox *V3RealtimeOutboxRecord
}

func (m *automationRealtimeMutation) put(key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil { return err }
	m.writes[key] = data
	return nil
}

func setAutomationRealtimeMutationInBatch(batch *pebble.Batch, account string, m *automationRealtimeMutation) error {
	prefix, err := automationPrefix(m.scope)
	if err != nil || m.scope.AccountID != account || len(m.writes) == 0 { return ErrAutomationInvalid }
	keys := make([]string, 0, len(m.writes))
	for key := range m.writes { keys = append(keys, key) }
	sort.Strings(keys)
	for _, key := range keys {
		data := m.writes[key]
		if !strings.HasPrefix(key, prefix) || !json.Valid(data) { return ErrAutomationInvalid }
		if err := batch.Set([]byte(key), data, nil); err != nil { return err }
	}
	return nil
}

// SetAutomationPublisher installs an instance-owned wakeup, not a persistence
// authority. Callbacks run after releasing domain and canonical mutation locks.
func (s *Store) SetAutomationPublisher(publish func(V3RealtimeOutboxRecord)) {
	s.automationPublisherMu.Lock()
	defer s.automationPublisherMu.Unlock()
	s.automationPublisher = publish
}

func (s *Store) commitAutomationRealtime(m *automationRealtimeMutation) error {
	requestID := uuid.NewString()
	result, err := NewSessionStore(s).ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID: "__automation__:" + m.scope.AccountID, AccountScopeID: m.scope.AccountID,
		UserID: "desktop", ClientRequestID: requestID, IdempotencyKey: requestID,
		PayloadHash: requestID, Kind: AutomationChangedEventType, EventType: AutomationChangedEventType,
		EventPayload: json.RawMessage(`{}`), automationRealtime: m,
	})
	if err != nil { return err }
	m.outbox = result.RealtimeOutbox
	return nil
}

// publishAutomationRealtime is called only after releasing automationsMu.
func (s *Store) publishAutomationRealtime(m *automationRealtimeMutation) {
	if m == nil || m.outbox == nil { return }
	s.automationPublisherMu.RLock()
	publish := s.automationPublisher
	s.automationPublisherMu.RUnlock()
	if publish != nil { publish(*m.outbox) }
}
