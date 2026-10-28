package pebblestore

import (
	"encoding/json"
	"errors"
	"log"
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
	taskTransition *ProjectTaskStatusTransition
	// notify publishes project.updated with no project or task write: a
	// task's view changed through a record kept elsewhere (a workflow
	// delivery of its job, or the evaluation of its job end).
	notify bool
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
	return key == projectOrderKey(accountScopeID) || strings.HasPrefix(key, projectPrefix) || strings.HasPrefix(key, taskPrefix)
}

func setProjectRealtimeMutationInBatch(batch *pebble.Batch, account string, m *projectRealtimeMutation, repositories ...*SessionStore) error {
	if m == nil || m.accountScopeID == "" || m.accountScopeID != account || strings.TrimSpace(m.projectID) == "" {
		return ErrProjectInvalid
	}
	if len(m.writes) == 0 && len(m.deletes) == 0 {
		if m.notify {
			return nil
		}
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
		if key == projectOrderKey(account) {
			if err := batch.Set([]byte(key), data, nil); err != nil {
				return err
			}
			continue
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
				previous := ProjectTaskRecord{}
				if found {
					previous = *prior
				}
				if err := setTaskEnvironmentIndex(batch, previous, task); err != nil {
					return err
				}
				if err := setDesignJobActiveIndex(batch, account, previous, task); err != nil {
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
		if strings.HasPrefix(key, KeyProjectTaskAccountPrefix) && len(repositories) > 0 {
			var prior ProjectTaskRecord
			found, err := repositories[0].store.GetJSON(key, &prior)
			if err != nil {
				return err
			}
			if found {
				if err := setTaskEnvironmentIndex(batch, prior, ProjectTaskRecord{}); err != nil {
					return err
				}
				if err := setDesignJobActiveIndex(batch, account, prior, ProjectTaskRecord{}); err != nil {
					return err
				}
			}
		}
		if err := batch.Delete([]byte(key), nil); err != nil {
			return err
		}
	}
	if len(repositories) > 0 && (len(m.writes) > 0 || len(m.deletes) > 0) {
		if err := setTaskSummariesInBatch(batch, repositories[0].store.db, m); err != nil {
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

// ProjectRealtimeSessionID is the internal session a project's project.updated
// events are committed under. It is lowercase to satisfy canonical session
// validation.
func ProjectRealtimeSessionID(accountScopeID, projectID string) string {
	return "__project__:" + strings.ToLower(strings.TrimSpace(accountScopeID)) + ":" + strings.ToLower(strings.TrimSpace(projectID))
}

func (s *Store) commitProjectRealtime(m *projectRealtimeMutation) error {
	requestID := uuid.NewString()
	payload := m.eventPayload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	sessionID := ProjectRealtimeSessionID(m.accountScopeID, m.projectID)
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
	if m == nil {
		return
	}
	s.projectPublisherMu.RLock()
	publish := s.projectPublisher
	observe := s.projectTaskStatusObserver
	s.projectPublisherMu.RUnlock()
	if publish != nil && m.outbox != nil {
		publish(*m.outbox)
	}
	if observe != nil && m.taskTransition != nil {
		observe(*m.taskTransition)
	}
}

// NotifyProjectTaskChanged publishes project.updated for a task whose view
// changed without a write to its record: a workflow delivery of its job
// (created, a step started or finished, finished) or the evaluation of its
// job end. Nothing but the event is written; clients read the task again.
func (s *SessionStore) NotifyProjectTaskChanged(accountScopeID, projectID, taskID string) error {
	if s == nil || s.store == nil || s.store.db == nil {
		return errors.New("database not available")
	}
	accountScopeID, projectID, taskID = strings.TrimSpace(accountScopeID), strings.TrimSpace(projectID), strings.TrimSpace(taskID)
	if accountScopeID == "" || projectID == "" {
		return ErrProjectInvalid
	}
	payload, _ := json.Marshal(map[string]any{
		"project_id": projectID,
		"account_id": accountScopeID,
		"task_id":    taskID,
		"action":     "task_progress",
	})
	mutation := &projectRealtimeMutation{accountScopeID: accountScopeID, projectID: projectID, eventPayload: payload, notify: true}
	if err := s.store.commitProjectRealtime(mutation); err != nil {
		return err
	}
	s.store.publishProjectRealtime(mutation)
	return nil
}

// SetProjectTaskProgress installs fn, which computes a task's progress (its
// state, summary, workflow and integration) for the outcomes wait_tasks
// resumes the Orchestrator with. nil leaves progress out.
func (s *Store) SetProjectTaskProgress(fn func(account string, task ProjectTaskRecord) any) {
	s.projectPublisherMu.Lock()
	s.projectTaskProgress = fn
	if fn != nil {
		s.projectTaskProgressAwaited = false
	}
	s.projectPublisherMu.Unlock()
}

// AwaitProjectTaskProgress declares that a progress hook will be installed
// (SetProjectTaskProgress) later in startup. Until it is, an until=settled
// wait does not decide a finished (needs_review/completed) task: without
// progress it cannot tell a running workflow from none, so the startup
// reconciliation must not wake it. Whoever installs the hook reconciles the
// waits afterwards.
func (s *Store) AwaitProjectTaskProgress() {
	s.projectPublisherMu.Lock()
	if s.projectTaskProgress == nil {
		s.projectTaskProgressAwaited = true
	}
	s.projectPublisherMu.Unlock()
}

func (s *Store) projectTaskProgressPending() bool {
	s.projectPublisherMu.RLock()
	defer s.projectPublisherMu.RUnlock()
	return s.projectTaskProgressAwaited && s.projectTaskProgress == nil
}

func (s *Store) taskProgress(account string, task ProjectTaskRecord) any {
	s.projectPublisherMu.RLock()
	fn := s.projectTaskProgress
	s.projectPublisherMu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn(account, task)
}

// ProjectTaskProgress is a task's progress as the installed progress hook
// computes it (the same progress wait_tasks resumes with); nil when no hook
// is installed. The Orchestrator's get_task reads it.
func (s *SessionStore) ProjectTaskProgress(account string, task ProjectTaskRecord) any {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.taskProgress(account, task)
}

// ProjectTaskStatusTransition is one committed change of a project task's status.
type ProjectTaskStatusTransition struct {
	AccountScopeID string
	PriorStatus    string
	Task           ProjectTaskRecord
}

// SetProjectTaskStatusObserver installs fn, called once for each committed
// project task write that moves the task into a new status. It runs after the
// write commits and outside the project and session mutation locks; a write that
// repeats the current status (a retry or replay) is not a transition.
func (s *Store) SetProjectTaskStatusObserver(fn func(ProjectTaskStatusTransition)) {
	s.projectPublisherMu.Lock()
	s.projectTaskStatusObserver = fn
	recoverMedia := fn != nil && s.mediaRecoveryPending
	if recoverMedia {
		s.mediaRecoveryPending = false
	}
	s.projectPublisherMu.Unlock()
	// Startup installs the notification observer before serving. Recover only
	// once per writable Open, with the real observer installed and no lock held.
	if recoverMedia {
		if err := NewSessionStore(s).recoverInterruptedProjectMedia(); err != nil {
			log.Printf("warning: interrupted media recovery scan failed; deferred until next restart")
		}
	}
}
