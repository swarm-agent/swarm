package pebblestore

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/cockroachdb/pebble"
	"github.com/google/uuid"
)

// ProjectUpdatedEventType is the canonical event type for project and task invalidation updates.
const ProjectUpdatedEventType = "project.updated"

// ErrProjectInvalid indicates that a project mutation violates domain invariants or scope boundaries.
var ErrProjectInvalid = errors.New("invalid project mutation")

// projectRealtimeMutation is a private participant that attaches atomic project/task writes
// to the canonical V3 session mutation and realtime outbox.
// Lock order is projectsMu -> canonical session mutation lock.
type projectRealtimeMutation struct {
	accountScopeID string
	projectID      string
	writes         map[string][]byte
	deletes        []string
	eventPayload   json.RawMessage
	outbox         *V3RealtimeOutboxRecord
}

func (m *projectRealtimeMutation) put(key string, value any) error {
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

func (m *projectRealtimeMutation) putBytes(key string, data []byte) {
	if m.writes == nil {
		m.writes = make(map[string][]byte)
	}
	m.writes[key] = data
}

func (m *projectRealtimeMutation) delete(key string) {
	m.deletes = append(m.deletes, key)
}

func isAllowedProjectKey(accountScopeID string, key string) bool {
	accountPart := keyPart(accountScopeID)
	if accountPart == "" {
		return false
	}
	projectPrefix := KeyProjectAccountPrefix + accountPart + "/"
	taskPrefix := KeyProjectTaskAccountPrefix + accountPart + "/"
	return strings.HasPrefix(key, projectPrefix) || strings.HasPrefix(key, taskPrefix)
}

func setProjectRealtimeMutationInBatch(batch *pebble.Batch, account string, m *projectRealtimeMutation, repositories ...*SessionStore) error {
	if m == nil || m.accountScopeID == "" || m.accountScopeID != account || strings.TrimSpace(m.projectID) == "" {
		return ErrProjectInvalid
	}
	if len(m.writes) == 0 && len(m.deletes) == 0 {
		return ErrProjectInvalid
	}
	keys := make([]string, 0, len(m.writes))
	for key := range m.writes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		data := m.writes[key]
		if !isAllowedProjectKey(account, key) || !json.Valid(data) {
			return ErrProjectInvalid
		}
		if strings.HasPrefix(key, KeyProjectTaskAccountPrefix+keyPart(account)+"/") {
			var task ProjectTaskRecord
			if err := json.Unmarshal(data, &task); err != nil {
				return err
			}
			if len(repositories) > 0 {
				repository := repositories[0]
				prior, found, err := repository.GetProjectTask(account, task.ProjectID, task.ID)
				if err != nil {
					return err
				}
				if found {
					if err := repository.bindTaskUsageInBatch(batch, account, *prior); err != nil {
						return err
					}
				}
				if err := repository.bindTaskUsageInBatch(batch, account, task); err != nil {
					return err
				}
			}
		}
		if strings.HasPrefix(key, KeyProjectAccountPrefix+keyPart(account)+"/") {
			var project ProjectRecord
			if err := json.Unmarshal(data, &project); err != nil {
				return err
			}
			if err := setDesignMembership(batch, account, project.PrimarySessionID, project.ID, ""); err != nil {
				return err
			}
		} else {
			var task ProjectTaskRecord
			if err := json.Unmarshal(data, &task); err != nil {
				return err
			}
			if err := setDesignMembership(batch, account, task.SessionID, task.ProjectID, task.ID); err != nil {
				return err
			}
			for _, attempt := range task.Attempts {
				if err := setDesignMembership(batch, account, attempt.SessionID, task.ProjectID, task.ID); err != nil {
					return err
				}
			}
		}
		if err := batch.Set([]byte(key), data, nil); err != nil {
			return err
		}
	}
	for _, key := range m.deletes {
		if !isAllowedProjectKey(account, key) {
			return ErrProjectInvalid
		}
		if err := batch.Delete([]byte(key), nil); err != nil {
			return err
		}
	}
	return nil
}

// SetProjectPublisher installs an instance-owned wakeup for realtime project changes.
// Callbacks run after releasing domain and canonical mutation locks.
func (s *Store) SetProjectPublisher(publish func(V3RealtimeOutboxRecord)) {
	s.projectPublisherMu.Lock()
	defer s.projectPublisherMu.Unlock()
	s.projectPublisher = publish
}

func (s *Store) commitProjectRealtime(m *projectRealtimeMutation) error {
	requestID := uuid.NewString()
	payload := m.eventPayload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	// Session ID is lowercase to satisfy canonical session validation
	sessionID := "__project__:" + strings.ToLower(strings.TrimSpace(m.accountScopeID)) + ":" + strings.ToLower(strings.TrimSpace(m.projectID))
	result, err := NewSessionStore(s).ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:       sessionID,
		AccountScopeID:  m.accountScopeID,
		UserID:          "desktop",
		ClientRequestID: requestID,
		IdempotencyKey:  requestID,
		PayloadHash:     requestID,
		Kind:            ProjectUpdatedEventType,
		EventType:       ProjectUpdatedEventType,
		EventPayload:    payload,
		projectRealtime: m,
	})
	if err != nil {
		return err
	}
	m.outbox = result.RealtimeOutbox
	return nil
}

func (s *Store) publishProjectRealtime(m *projectRealtimeMutation) {
	if m == nil || m.outbox == nil {
		return
	}
	s.projectPublisherMu.RLock()
	publish := s.projectPublisher
	s.projectPublisherMu.RUnlock()
	if publish != nil {
		publish(*m.outbox)
	}
}
