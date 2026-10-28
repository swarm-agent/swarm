package pebblestore

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/pebble"
)

// FleetLinkedTask is this home's record of a task one of its sessions sent to
// a linked machine. The machine owns the task; the home keeps who sent it
// (session and project) and what it last saw of it, so the sending session
// and project can list it. Everything observed from the machine is untrusted.
type FleetLinkedTask struct {
	AccountScopeID  string `json:"account_scope_id"`
	Machine         string `json:"machine"`
	ProjectID       string `json:"project_id"` // on the machine
	TaskID          string `json:"task_id"`    // on the machine
	Title           string `json:"title,omitempty"`
	HomeSessionID   string `json:"home_session_id"`
	HomeProjectID   string `json:"home_project_id,omitempty"`
	CreatedAt       int64  `json:"created_at"`
	Status          string `json:"status,omitempty"`
	ActionNeeded    string `json:"action_needed,omitempty"`
	LastError       string `json:"last_error,omitempty"`
	Progress        string `json:"progress,omitempty"` // the active attempt's summary
	RemoteSessionID string `json:"remote_session_id,omitempty"`
	Archived        bool   `json:"archived,omitempty"`
	Missing         bool   `json:"missing,omitempty"` // the machine answered that the task does not exist
	ObservedAt      int64  `json:"observed_at,omitempty"`
}

// FleetLinkedTaskObservation is what the home read of one task through the link.
type FleetLinkedTaskObservation struct {
	TaskID          string
	Title           string
	Status          string
	ActionNeeded    string
	LastError       string
	Progress        string
	RemoteSessionID string
	Archived        bool
	Missing         bool
}

const (
	fleetLinkedTaskPrefix        = "fleet/linked_task/"
	fleetLinkedTaskSessionPrefix = "fleet/linked_task_session/"
	fleetLinkedTaskProjectPrefix = "fleet/linked_task_project/"
	fleetLinkedTaskListLimit     = 500
)

// Records are read-modify-written; one lock keeps observations from
// overwriting each other within this daemon.
var fleetLinkedTaskMu sync.Mutex

func fleetLinkedTaskID(machine, projectID, taskID string) string {
	return keyPart(machine) + "/" + keyPart(projectID) + "/" + keyPart(taskID)
}

func fleetLinkedTaskKey(account, machine, projectID, taskID string) string {
	return fleetLinkedTaskPrefix + keyPart(account) + "/" + fleetLinkedTaskID(machine, projectID, taskID)
}

// PutLinkedTask stores a new record with its session and project indexes in
// one batch. A record that already exists keeps its sender and creation time
// (a retried send is the same task) and takes the new observation.
func (s *FleetStore) PutLinkedTask(task FleetLinkedTask) (FleetLinkedTask, error) {
	task.AccountScopeID = strings.TrimSpace(task.AccountScopeID)
	task.Machine = strings.TrimSpace(task.Machine)
	task.ProjectID = strings.TrimSpace(task.ProjectID)
	task.TaskID = strings.TrimSpace(task.TaskID)
	task.HomeSessionID = strings.TrimSpace(task.HomeSessionID)
	task.HomeProjectID = strings.TrimSpace(task.HomeProjectID)
	if task.AccountScopeID == "" || task.Machine == "" || task.ProjectID == "" || task.TaskID == "" || task.HomeSessionID == "" {
		return FleetLinkedTask{}, errors.New("a linked task needs an account, a machine, a project, a task and the sending session")
	}
	fleetLinkedTaskMu.Lock()
	defer fleetLinkedTaskMu.Unlock()
	key := fleetLinkedTaskKey(task.AccountScopeID, task.Machine, task.ProjectID, task.TaskID)
	var existing FleetLinkedTask
	found, err := s.store.GetJSON(key, &existing)
	if err != nil {
		return FleetLinkedTask{}, err
	}
	now := time.Now().UnixMilli()
	if found {
		task.HomeSessionID, task.HomeProjectID, task.CreatedAt = existing.HomeSessionID, existing.HomeProjectID, existing.CreatedAt
		if task.Title == "" {
			task.Title = existing.Title
		}
	} else if task.CreatedAt == 0 {
		task.CreatedAt = now
	}
	if task.ObservedAt == 0 {
		task.ObservedAt = now
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return FleetLinkedTask{}, err
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(key), payload, nil); err != nil {
		return FleetLinkedTask{}, err
	}
	id := fleetLinkedTaskID(task.Machine, task.ProjectID, task.TaskID)
	if !found {
		account := keyPart(task.AccountScopeID)
		if err := batch.Set([]byte(fleetLinkedTaskSessionPrefix+account+"/"+keyPart(task.HomeSessionID)+"/"+id), []byte(key), nil); err != nil {
			return FleetLinkedTask{}, err
		}
		if task.HomeProjectID != "" {
			if err := batch.Set([]byte(fleetLinkedTaskProjectPrefix+account+"/"+keyPart(task.HomeProjectID)+"/"+id), []byte(key), nil); err != nil {
				return FleetLinkedTask{}, err
			}
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return FleetLinkedTask{}, err
	}
	return task, nil
}

// GetLinkedTask reads one record of the account.
func (s *FleetStore) GetLinkedTask(account, machine, projectID, taskID string) (FleetLinkedTask, bool, error) {
	var task FleetLinkedTask
	found, err := s.store.GetJSON(fleetLinkedTaskKey(account, machine, projectID, taskID), &task)
	return task, found, err
}

// ObserveLinkedTask updates an existing record from what the home just read
// through the link. It never creates a record: only a send does. It reports
// whether a record was updated.
func (s *FleetStore) ObserveLinkedTask(account, machine, projectID string, observed FleetLinkedTaskObservation) (bool, error) {
	if strings.TrimSpace(account) == "" || strings.TrimSpace(machine) == "" || strings.TrimSpace(projectID) == "" || strings.TrimSpace(observed.TaskID) == "" {
		return false, nil
	}
	fleetLinkedTaskMu.Lock()
	defer fleetLinkedTaskMu.Unlock()
	key := fleetLinkedTaskKey(account, machine, projectID, observed.TaskID)
	var task FleetLinkedTask
	found, err := s.store.GetJSON(key, &task)
	if err != nil || !found {
		return false, err
	}
	if observed.Missing {
		task.Missing = true
	} else {
		task.Missing = false
		if observed.Title != "" {
			task.Title = observed.Title
		}
		task.Status, task.ActionNeeded, task.LastError, task.Progress = observed.Status, observed.ActionNeeded, observed.LastError, observed.Progress
		if observed.RemoteSessionID != "" {
			task.RemoteSessionID = observed.RemoteSessionID
		}
		task.Archived = observed.Archived
	}
	task.ObservedAt = time.Now().UnixMilli()
	return true, s.store.PutJSON(key, task)
}

// ListLinkedTasksForSession lists the records sent by one home session, newest first.
func (s *FleetStore) ListLinkedTasksForSession(account, sessionID string) ([]FleetLinkedTask, error) {
	return s.listLinkedTasksByIndex(fleetLinkedTaskSessionPrefix + keyPart(account) + "/" + keyPart(sessionID) + "/")
}

// ListLinkedTasksForProject lists the records sent from one home project, newest first.
func (s *FleetStore) ListLinkedTasksForProject(account, projectID string) ([]FleetLinkedTask, error) {
	return s.listLinkedTasksByIndex(fleetLinkedTaskProjectPrefix + keyPart(account) + "/" + keyPart(projectID) + "/")
}

func (s *FleetStore) listLinkedTasksByIndex(prefix string) ([]FleetLinkedTask, error) {
	out := []FleetLinkedTask{}
	err := s.store.IteratePrefix(prefix, fleetLinkedTaskListLimit, func(_ string, value []byte) error {
		var task FleetLinkedTask
		found, err := s.store.GetJSON(string(value), &task)
		if err != nil {
			return err
		}
		if found {
			out = append(out, task)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out, nil
}
