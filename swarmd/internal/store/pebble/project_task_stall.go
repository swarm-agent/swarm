package pebblestore

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/cockroachdb/pebble"
)

// Stalled tasks (docs/task-progress.md, "Stalled tasks"). The stall detector
// in the API server decides; this file keeps what it decided durably: the
// marker every task read shows (ProjectTaskRecord.Stall), the per-task
// episode record that makes its alerts exactly-once across restarts, and the
// receipts that make each automatic recovery happen once.

// Stall kinds: the precise state a stalled task is in.
const (
	ProjectTaskStallApproval         = "approval"          // a tool approval or Start tasks approval waits
	ProjectTaskStallHeld             = "held"              // a queued run held by an update drain or admission
	ProjectTaskStallProviderLimit    = "provider_limit"    // a workflow step's provider-limit wait passed its reset
	ProjectTaskStallReviewExhausted  = "review_exhausted"  // the review loop ran out of rounds and still blocks
	ProjectTaskStallEndedUncommitted = "ended_uncommitted" // the run ended with uncommitted changes, nothing committed
	ProjectTaskStallNoNextStep       = "no_next_step"      // no run and no next step
	ProjectTaskStallRunSilent        = "run_silent"        // an active run with no new event
)

// ProjectTaskStall is the marker on a stalled task.
type ProjectTaskStall struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
	Since  int64  `json:"since"`
	// TaskUpdatedAt is the task's UpdatedAt the stall was decided on: any
	// later task write is movement, so the marker no longer applies.
	TaskUpdatedAt int64 `json:"task_updated_at"`
}

// Applies reports whether the marker describes task as it is now.
func (s *ProjectTaskStall) Applies(task ProjectTaskRecord) bool {
	return s != nil && strings.TrimSpace(s.Reason) != "" && s.TaskUpdatedAt == task.UpdatedAt
}

// SetProjectTaskStall records stall (nil clears it) on the task without
// changing anything else, UpdatedAt included: a marker is not movement. It
// writes nothing and reports false when the task changed since the stall was
// decided (its UpdatedAt is no longer stall.TaskUpdatedAt) or is gone.
// Clients read the task again through the usual project.updated event.
func (s *SessionStore) SetProjectTaskStall(accountScopeID, projectID, taskID string, updatedAt int64, stall *ProjectTaskStall) (bool, error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return false, errors.New("database not available")
	}
	accountScopeID, projectID, taskID = strings.TrimSpace(accountScopeID), strings.TrimSpace(projectID), strings.TrimSpace(taskID)
	if accountScopeID == "" || projectID == "" || taskID == "" {
		return false, errors.New("account, project and task are required")
	}
	s.store.projectsMu.Lock()
	key := KeyProjectTask(accountScopeID, projectID, taskID)
	raw, closer, err := s.store.db.Get([]byte(key))
	if errors.Is(err, pebble.ErrNotFound) {
		s.store.projectsMu.Unlock()
		return false, nil
	}
	if err != nil {
		s.store.projectsMu.Unlock()
		return false, err
	}
	var task ProjectTaskRecord
	err = json.Unmarshal(raw, &task)
	closer.Close()
	if err != nil {
		s.store.projectsMu.Unlock()
		return false, err
	}
	if task.UpdatedAt != updatedAt || task.AccountID != accountScopeID {
		s.store.projectsMu.Unlock()
		return false, nil
	}
	if stall == nil && task.Stall == nil {
		s.store.projectsMu.Unlock()
		return true, nil
	}
	task.Stall = stall
	out, err := json.Marshal(&task)
	if err != nil {
		s.store.projectsMu.Unlock()
		return false, err
	}
	payload, _ := json.Marshal(map[string]any{
		"project_id": projectID, "account_id": accountScopeID, "task_id": taskID,
		"action": "task_stall", "revision": task.Revision, "archived": task.Archived,
	})
	mutation := &projectRealtimeMutation{accountScopeID: accountScopeID, projectID: projectID, eventPayload: payload}
	mutation.putBytes(key, out)
	err = s.store.commitProjectRealtime(mutation)
	s.store.projectsMu.Unlock()
	if err != nil {
		return false, err
	}
	s.store.publishProjectRealtime(mutation)
	return true, nil
}

// TaskStallRecord is the detector's durable view of one task's current
// stall episode. Episode names the task's progress as it was when it stopped
// moving; it is the same after a restart, so the episode's signal and wake
// (keyed by it) are never sent twice.
type TaskStallRecord struct {
	ProjectID string `json:"project_id"`
	TaskID    string `json:"task_id"`
	Episode   string `json:"episode"`
	Kind      string `json:"kind"`
	Reason    string `json:"reason"`
	Since     int64  `json:"since"`
	Alerted   bool   `json:"alerted,omitempty"`
	AlertedAt int64  `json:"alerted_at,omitempty"`
}

func taskStallKey(account, taskID string) string {
	return "task_stall/episode/" + keyPart(account) + "/" + keyPart(taskID)
}

func taskStallRecoveryKey(account, taskID, kind, id string) string {
	return "task_stall/recovery/" + keyPart(account) + "/" + keyPart(taskID) + "/" + keyPart(kind) + "/" + keyPart(id)
}

// GetTaskStall reads a task's current stall episode.
func (s *SessionStore) GetTaskStall(account, taskID string) (TaskStallRecord, bool, error) {
	var rec TaskStallRecord
	found, err := s.store.GetJSON(taskStallKey(account, taskID), &rec)
	return rec, found, err
}

// PutTaskStall records a task's current stall episode.
func (s *SessionStore) PutTaskStall(account string, rec TaskStallRecord) error {
	if strings.TrimSpace(rec.TaskID) == "" || strings.TrimSpace(rec.Episode) == "" {
		return errors.New("a stall record needs its task and episode")
	}
	return s.store.PutJSON(taskStallKey(account, rec.TaskID), rec)
}

// DeleteTaskStall ends a task's stall episode.
func (s *SessionStore) DeleteTaskStall(account, taskID string) error {
	return s.store.Delete(taskStallKey(account, taskID))
}

// TaskStallRecovery is the receipt of one automatic recovery.
type TaskStallRecovery struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	RunID    string `json:"run_id,omitempty"`
	Followup string `json:"followup,omitempty"`
	At       int64  `json:"at"`
}

// GetTaskStallRecovery reads the receipt of the recovery kind/id of a task.
func (s *SessionStore) GetTaskStallRecovery(account, taskID, kind, id string) (TaskStallRecovery, bool, error) {
	var rec TaskStallRecovery
	found, err := s.store.GetJSON(taskStallRecoveryKey(account, taskID, kind, id), &rec)
	return rec, found, err
}

// PutTaskStallRecovery records that the recovery kind/id of a task was sent.
func (s *SessionStore) PutTaskStallRecovery(account, taskID string, rec TaskStallRecovery) error {
	if strings.TrimSpace(rec.Kind) == "" || strings.TrimSpace(rec.ID) == "" {
		return errors.New("a recovery receipt needs its kind and id")
	}
	return s.store.PutJSON(taskStallRecoveryKey(account, taskID, rec.Kind, rec.ID), rec)
}

// HasTaskStallRecovery reports whether any recovery of kind was recorded for
// a task (whatever its id).
func (s *SessionStore) HasTaskStallRecovery(account, taskID, kind string) (bool, error) {
	found := false
	err := s.store.IteratePrefix("task_stall/recovery/"+keyPart(account)+"/"+keyPart(taskID)+"/"+keyPart(kind)+"/", 1, func(string, []byte) error {
		found = true
		return nil
	})
	return found, err
}
