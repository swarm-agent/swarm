package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/cockroachdb/pebble"
)

// A task's brief is the few fields the periodic readers of the whole task
// history select on: the agents-at-work finished summary (GET
// /v3/fleet/agents) and the stall check. Both used to decode every task
// record in full (results, attempts, deliverables, plans) on every poll or
// tick to read a handful of fields; the brief is what they select on, and
// they read a full record only for a task they return or act on.
//
// Briefs are a derived index written in the same batch as every canonical
// task write and delete (setProjectRealtimeMutationInBatch), built once at
// open for records written before the index existed. A project's rows are
// kept per archived partition, newest UpdatedAt first (ties in canonical
// task key order), so a reader that wants only the newest finished tasks
// stops after them. A brief is a read model, never an input to a task write.

const (
	projectTaskBriefVersion   = 1
	projectTaskBriefNamespace = "project_task_brief/v1/"
	projectTaskBriefStateKey  = projectTaskBriefNamespace + "state"
	// projectTaskBriefBackfillRows / Bytes bound one backfill batch.
	projectTaskBriefBackfillRows  = 256
	projectTaskBriefBackfillBytes = 16 << 20
)

// ErrProjectTaskBriefsNotReady means the brief index has not been built (a
// read-only open). It never means a project has no tasks.
var ErrProjectTaskBriefsNotReady = errors.New("project task briefs not ready")

// ProjectTaskBrief is a task's selection fields (see above).
type ProjectTaskBrief struct {
	ID           string `json:"id"`
	ProjectID    string `json:"project_id"`
	AccountID    string `json:"account_id"`
	SessionID    string `json:"session_id,omitempty"`
	Title        string `json:"title,omitempty"`
	Status       string `json:"status,omitempty"`
	Archived     bool   `json:"archived,omitempty"`
	AutomationID string `json:"automation_id,omitempty"`
	Agent        string `json:"agent,omitempty"`
	// Design: the task is a design job (ProjectTaskRecord.Design != nil).
	Design        bool   `json:"design,omitempty"`
	WorkerID      string `json:"worker_id,omitempty"`
	WorkerRunID   string `json:"worker_run_id,omitempty"`
	WorkspacePath string `json:"workspace_path,omitempty"`
	LimitRetryAt  int64  `json:"limit_retry_at,omitempty"`
	// Stalled: the task carries a stall marker (ProjectTaskRecord.Stall).
	Stalled   bool  `json:"stalled,omitempty"`
	CreatedAt int64 `json:"created_at,omitempty"`
	UpdatedAt int64 `json:"updated_at,omitempty"`
}

// ProjectTaskBriefOf is t's brief.
func ProjectTaskBriefOf(t ProjectTaskRecord) ProjectTaskBrief {
	return ProjectTaskBrief{
		ID: t.ID, ProjectID: t.ProjectID, AccountID: t.AccountID, SessionID: t.SessionID, Title: t.Title, Status: t.Status,
		Archived: t.Archived, AutomationID: t.AutomationID, Agent: t.Agent, Design: t.Design != nil,
		WorkerID: t.WorkerID, WorkerRunID: t.WorkerRunID, WorkspacePath: t.WorkspacePath, LimitRetryAt: t.LimitRetryAt,
		Stalled: t.Stall != nil, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}

// WorkflowSpawned is ProjectTaskWorkflowSpawned for the brief's task.
func (b ProjectTaskBrief) WorkflowSpawned() bool {
	return projectTaskWorkflowSpawned(b.AutomationID, b.ID)
}

type projectTaskBriefState struct {
	Version int    `json:"version"`
	Cursor  string `json:"cursor,omitempty"`
	Ready   bool   `json:"ready"`
}

func projectTaskBriefRowsPrefix(account, project string, archived bool) string {
	partition := "0"
	if archived {
		partition = "1"
	}
	return projectTaskBriefNamespace + "rows/" + keyPart(account) + "/" + keyPart(project) + "/" + partition + "/"
}

func projectTaskBriefRowKey(b ProjectTaskBrief) string {
	updated := max(b.UpdatedAt, 0)
	return fmt.Sprintf("%s%016x/%s", projectTaskBriefRowsPrefix(b.AccountID, b.ProjectID, b.Archived), uint64(math.MaxInt64-updated), keyPart(b.ID))
}

// projectTaskBriefLocatorKey names the row a canonical task key's brief is
// stored at, so a write replaces it whatever the prior record said.
func projectTaskBriefLocatorKey(canonical string) string {
	return projectTaskBriefNamespace + "ids/" + canonical
}

// setProjectTaskBriefInBatch keeps the brief of the task stored at the
// canonical key in the batch that writes it; next nil is a delete. A record
// not stored under its own account, project and id key has no brief (the
// whole-project reads it replaces skipped such records too).
func setProjectTaskBriefInBatch(batch *pebble.Batch, reader pebble.Reader, account, key string, next *ProjectTaskRecord) error {
	locator := projectTaskBriefLocatorKey(key)
	var prior string
	found, err := getJSONFromReader(reader, locator, &prior)
	if err != nil {
		return err
	}
	if found && strings.HasPrefix(prior, projectTaskBriefNamespace+"rows/") {
		if err := batch.Delete([]byte(prior), nil); err != nil {
			return err
		}
	}
	if next == nil || account == "" || next.AccountID != account || next.ID == "" || KeyProjectTask(account, next.ProjectID, next.ID) != key {
		if found {
			return batch.Delete([]byte(locator), nil)
		}
		return nil
	}
	brief := ProjectTaskBriefOf(*next)
	raw, err := json.Marshal(brief)
	if err != nil {
		return err
	}
	row := projectTaskBriefRowKey(brief)
	if err := batch.Set([]byte(row), raw, nil); err != nil {
		return err
	}
	link, err := json.Marshal(row)
	if err != nil {
		return err
	}
	return batch.Set([]byte(locator), link, nil)
}

// prepareProjectTaskBriefs builds the brief of every task written before the
// index existed, in bounded batches that commit their cursor with their rows,
// so an interrupted open resumes. It runs once: later writes keep the index.
func (s *SessionStore) prepareProjectTaskBriefs() error {
	s.store.projectsMu.Lock()
	defer s.store.projectsMu.Unlock()
	return s.backfillProjectTaskBriefsLocked()
}

// RebuildProjectTaskBriefs drops the brief index and builds it again from
// the canonical task records: a repair for records written without it (an
// import, or a store written by a build that did not keep it).
func (s *SessionStore) RebuildProjectTaskBriefs() error {
	if s == nil || s.store == nil || s.store.db == nil {
		return errors.New("database not available")
	}
	s.store.projectsMu.Lock()
	defer s.store.projectsMu.Unlock()
	reset := s.store.db.NewBatch()
	defer reset.Close()
	if err := reset.DeleteRange([]byte(projectTaskBriefNamespace), []byte(projectTaskBriefNamespace+"\xff"), nil); err != nil {
		return err
	}
	if err := reset.Commit(pebble.Sync); err != nil {
		return err
	}
	return s.backfillProjectTaskBriefsLocked()
}

func (s *SessionStore) backfillProjectTaskBriefsLocked() error {
	var state projectTaskBriefState
	found, err := getJSONFromReader(s.store.db, projectTaskBriefStateKey, &state)
	if err != nil {
		return err
	}
	if found && state.Version != projectTaskBriefVersion {
		return fmt.Errorf("project task briefs: unsupported version %d (expected %d)", state.Version, projectTaskBriefVersion)
	}
	if state.Cursor != "" && !strings.HasPrefix(state.Cursor, KeyProjectTaskAccountPrefix) {
		return errors.New("project task briefs: invalid backfill cursor")
	}
	for !state.Ready {
		batch := s.store.db.NewBatch()
		rows, bytes, limited := 0, 0, false
		err := scanRangeFromReader(s.store.db, scanRangeOptions{Prefix: KeyProjectTaskAccountPrefix, StartKey: state.Cursor, Limit: projectTaskBriefBackfillRows}, func(key string, raw []byte) (bool, error) {
			if rows > 0 && bytes+len(raw) > projectTaskBriefBackfillBytes {
				limited = true
				return false, nil
			}
			rows++
			bytes += len(raw)
			state.Cursor = key + "\x00"
			var task ProjectTaskRecord
			if json.Unmarshal(raw, &task) != nil {
				return true, nil // an unreadable record has no brief
			}
			return true, setProjectTaskBriefInBatch(batch, s.store.db, task.AccountID, key, &task)
		})
		if err == nil {
			state.Version = projectTaskBriefVersion
			state.Ready = rows < projectTaskBriefBackfillRows && !limited
			var raw []byte
			if raw, err = json.Marshal(state); err == nil {
				if err = batch.Set([]byte(projectTaskBriefStateKey), raw, nil); err == nil {
					err = batch.Commit(pebble.Sync)
				}
			}
		}
		batch.Close()
		if err != nil {
			return fmt.Errorf("project task brief preparation failed; records retained: %w", err)
		}
	}
	return nil
}

// ListProjectTaskBriefs visits the briefs of a project's tasks in the
// archived (or unarchived) partition, newest UpdatedAt first and, among
// equal times, in canonical task key order, until visit returns false. No
// full task record is read.
func (s *SessionStore) ListProjectTaskBriefs(accountScopeID, projectID string, archived bool, visit func(ProjectTaskBrief) bool) error {
	if s == nil || s.store == nil || s.store.db == nil {
		return errors.New("database not available")
	}
	accountScopeID, projectID = strings.TrimSpace(accountScopeID), strings.TrimSpace(projectID)
	if accountScopeID == "" || projectID == "" {
		return errors.New("account scope id and project id are required")
	}
	var state projectTaskBriefState
	if found, err := getJSONFromReader(s.store.db, projectTaskBriefStateKey, &state); err != nil {
		return err
	} else if !found || !state.Ready || state.Version != projectTaskBriefVersion {
		return ErrProjectTaskBriefsNotReady
	}
	return scanRangeFromReader(s.store.db, scanRangeOptions{Prefix: projectTaskBriefRowsPrefix(accountScopeID, projectID, archived), Limit: math.MaxInt}, func(_ string, raw []byte) (bool, error) {
		var brief ProjectTaskBrief
		if err := json.Unmarshal(raw, &brief); err != nil {
			return false, fmt.Errorf("project task brief: %w", err)
		}
		if brief.AccountID != accountScopeID || brief.ProjectID != projectID || brief.Archived != archived {
			return true, nil
		}
		return visit(brief), nil
	})
}

// GetProjectTaskRecord reads a task's record as stored, without hydrating
// its attempts or plan (GetProjectTask hydrates).
func (s *SessionStore) GetProjectTaskRecord(accountScopeID, projectID, taskID string) (*ProjectTaskRecord, bool, error) {
	return s.getProjectTask(accountScopeID, projectID, taskID, false)
}

// ProjectTaskRecordDecodes is how many full task records this store has
// decoded for reads (gets and whole-project lists) since it was opened.
func (s *SessionStore) ProjectTaskRecordDecodes() int64 {
	if s == nil || s.store == nil {
		return 0
	}
	return s.store.projectTaskRecordDecodes.Load()
}
