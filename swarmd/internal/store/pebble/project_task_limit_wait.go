package pebblestore

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/cockroachdb/pebble"
)

// A project task whose run stopped on its provider's usage limit (a Claude
// Code session or weekly limit, a provider 429 or quota with a reset) is not
// failed: its status is ProjectTaskStatusLimitWaiting until the reset, when
// the same task session continues in its retained worktree. The wait is
// durable on the task record (LimitWaits, LimitRetryAt, LimitRunID,
// LimitReason) and indexed so a restart re-arms it at its reset time. The
// detection, reset parsing and bound are package providerlimit's, the same
// the worker and job-automation paths use.
const (
	// ProjectTaskStatusLimitWaiting: the task's run stopped on its provider's
	// limit and the task continues by itself at LimitRetryAt. Task state
	// waiting; a stop or archive cancels the wait.
	ProjectTaskStatusLimitWaiting = "limit_waiting"
)

// taskLimitWaitPrefix indexes tasks with a pending limit continuation
// (LimitRetryAt set), written in the same batch as the task record.
const taskLimitWaitPrefix = "project_task_limit_wait/"

// ProjectTaskLimitWaitRef names one task with a pending limit continuation.
type ProjectTaskLimitWaitRef struct {
	AccountScopeID string `json:"account_id"`
	ProjectID      string `json:"project_id"`
	TaskID         string `json:"task_id"`
	RetryAt        int64  `json:"retry_at"`
}

func taskLimitWaitKey(account, project, task string) string {
	return taskLimitWaitPrefix + keyPart(account) + "/" + keyPart(project) + "/" + keyPart(task)
}

// ProjectTaskLimitPending reports whether t has a limit continuation that has
// not been handed to its session yet.
func ProjectTaskLimitPending(t ProjectTaskRecord) bool {
	return t.LimitRetryAt > 0 && t.ID != "" && t.ProjectID != ""
}

// setTaskLimitWaitIndex keeps the limit wait index in the same batch as the
// task write that starts, moves or ends the wait.
func setTaskLimitWaitIndex(batch *pebble.Batch, account string, prior, next ProjectTaskRecord) error {
	if ProjectTaskLimitPending(prior) && !ProjectTaskLimitPending(next) {
		if err := batch.Delete([]byte(taskLimitWaitKey(account, prior.ProjectID, prior.ID)), nil); err != nil {
			return err
		}
	}
	if ProjectTaskLimitPending(next) {
		raw, err := json.Marshal(ProjectTaskLimitWaitRef{AccountScopeID: account, ProjectID: next.ProjectID, TaskID: next.ID, RetryAt: next.LimitRetryAt})
		if err != nil {
			return err
		}
		return batch.Set([]byte(taskLimitWaitKey(account, next.ProjectID, next.ID)), raw, nil)
	}
	return nil
}

// ListProjectTaskLimitWaits returns every task with a pending limit
// continuation, from the durable index. An index entry whose task is gone or
// no longer waits is removed.
func (s *SessionStore) ListProjectTaskLimitWaits() ([]ProjectTaskLimitWaitRef, error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	var refs []ProjectTaskLimitWaitRef
	err := s.store.IteratePrefix(taskLimitWaitPrefix, 100000, func(_ string, value []byte) error {
		var ref ProjectTaskLimitWaitRef
		if err := json.Unmarshal(value, &ref); err != nil {
			return fmt.Errorf("task limit wait index: %w", err)
		}
		refs = append(refs, ref)
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := refs[:0]
	for _, ref := range refs {
		task, found, err := s.getProjectTask(ref.AccountScopeID, ref.ProjectID, ref.TaskID, false)
		if err != nil {
			return nil, err
		}
		if !found || task.AccountID != ref.AccountScopeID || !ProjectTaskLimitPending(*task) {
			if err := s.store.db.Delete([]byte(taskLimitWaitKey(ref.AccountScopeID, ref.ProjectID, ref.TaskID)), pebble.Sync); err != nil {
				return nil, err
			}
			continue
		}
		ref.RetryAt = task.LimitRetryAt
		out = append(out, ref)
	}
	return out, nil
}

const taskLimitRerunMarker = ".limit"

// ProjectTaskLimitRunBase is the run a limit continuation continues: runID
// without its ".limit<n>" suffix. A run that is not a continuation is its
// own base.
func ProjectTaskLimitRunBase(runID string) string {
	i := strings.LastIndex(runID, taskLimitRerunMarker)
	if i <= 0 {
		return runID
	}
	n, err := strconv.Atoi(runID[i+len(taskLimitRerunMarker):])
	if err != nil || n < 1 {
		return runID
	}
	return runID[:i]
}

// ProjectTaskLimitRerunID is the run id of the n-th limit continuation of
// the run base continues.
func ProjectTaskLimitRerunID(base string, n int) string {
	return ProjectTaskLimitRunBase(base) + taskLimitRerunMarker + strconv.Itoa(n)
}
