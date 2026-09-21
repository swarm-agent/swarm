package pebblestore

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/cockroachdb/pebble"
	"github.com/google/uuid"
)

// EnvironmentChangedEventType is the canonical event type for metadata-only workspace environment updates.
const EnvironmentChangedEventType = "environment.updated"

// ErrEnvironmentInvalid indicates that an environment mutation violates domain invariants or scope boundaries.
var ErrEnvironmentInvalid = errors.New("invalid environment mutation")

// environmentRealtimeMutation is a private participant that attaches atomic environment writes
// to the canonical V3 session mutation and realtime outbox.
// Lock order is environmentsMu -> canonical session mutation lock.
type environmentRealtimeMutation struct {
	accountScopeID string
	workspaceID    string
	writes         map[string][]byte
	deletes        []string
	eventPayload   json.RawMessage
	outbox         *V3RealtimeOutboxRecord
}

func (m *environmentRealtimeMutation) put(key string, value any) error {
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

func (m *environmentRealtimeMutation) putBytes(key string, data []byte) {
	if m.writes == nil {
		m.writes = make(map[string][]byte)
	}
	m.writes[key] = data
}

func (m *environmentRealtimeMutation) delete(key string) {
	m.deletes = append(m.deletes, key)
}

func isAllowedEnvironmentKey(accountScopeID string, workspaceID string, key string) bool {
	accountPart := keyPart(accountScopeID)
	workspacePart := keyPart(workspaceID)
	if accountPart == "" || workspacePart == "" {
		return false
	}
	summaryExact := KeyEnvironmentSummaryForAccount(accountScopeID, workspaceID)
	if key == summaryExact {
		return true
	}
	prefixes := []string{
		KeyEnvironmentAccountPrefix + accountPart + "/" + workspacePart + "/",
		KeyDeploymentAccountPrefix + accountPart + "/" + workspacePart + "/",
		KeyDeploymentLeaseAccountPrefix + accountPart + "/" + workspacePart + "/",
		KeyDeploymentActiveLeasePrefix + accountPart + "/" + workspacePart + "/",
		KeyEnvironmentOperationAccountPrefix + accountPart + "/" + workspacePart + "/",
		KeyEnvironmentActiveOpAccountPrefix + accountPart + "/" + workspacePart + "/",
		KeyEnvironmentOpIdempotencyPrefix + accountPart + "/" + workspacePart + "/",
		KeyEnvironmentSummaryAccountPrefix + accountPart + "/" + workspacePart + "/",
		KeyEnvironmentOpHistoryAccountPrefix + accountPart + "/" + workspacePart + "/",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

func setEnvironmentRealtimeMutationInBatch(batch *pebble.Batch, account string, m *environmentRealtimeMutation) error {
	if m == nil || m.accountScopeID == "" || m.accountScopeID != account || strings.TrimSpace(m.workspaceID) == "" {
		return ErrEnvironmentInvalid
	}
	keys := make([]string, 0, len(m.writes))
	for key := range m.writes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		data := m.writes[key]
		if !isAllowedEnvironmentKey(account, m.workspaceID, key) {
			return ErrEnvironmentInvalid
		}
		if err := batch.Set([]byte(key), data, nil); err != nil {
			return err
		}
	}
	for _, key := range m.deletes {
		if !isAllowedEnvironmentKey(account, m.workspaceID, key) {
			return ErrEnvironmentInvalid
		}
		if err := batch.Delete([]byte(key), nil); err != nil {
			return err
		}
	}
	return nil
}

// SetEnvironmentPublisher installs an instance-owned wakeup for realtime environment changes.
// Callbacks run after releasing domain and canonical mutation locks.
func (s *Store) SetEnvironmentPublisher(publish func(V3RealtimeOutboxRecord)) {
	s.environmentPublisherMu.Lock()
	defer s.environmentPublisherMu.Unlock()
	s.environmentPublisher = publish
}

func (s *Store) commitEnvironmentRealtime(m *environmentRealtimeMutation) error {
	requestID := uuid.NewString()
	payload := m.eventPayload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	// Session ID is lowercase to satisfy canonical session validation
	sessionID := "__environment__:" + strings.ToLower(m.accountScopeID) + ":" + strings.ToLower(m.workspaceID)
	result, err := NewSessionStore(s).ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:           sessionID,
		AccountScopeID:      m.accountScopeID,
		UserID:              "desktop",
		ClientRequestID:     requestID,
		IdempotencyKey:      requestID,
		PayloadHash:         requestID,
		Kind:                EnvironmentChangedEventType,
		EventType:           EnvironmentChangedEventType,
		EventPayload:        payload,
		environmentRealtime: m,
	})
	if err != nil {
		return err
	}
	m.outbox = result.RealtimeOutbox
	return nil
}

func (s *Store) publishEnvironmentRealtime(m *environmentRealtimeMutation) {
	if m == nil || m.outbox == nil {
		return
	}
	s.environmentPublisherMu.RLock()
	publish := s.environmentPublisher
	s.environmentPublisherMu.RUnlock()
	if publish != nil {
		publish(*m.outbox)
	}
}
