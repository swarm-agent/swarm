package pebblestore

import (
	"errors"
	"github.com/cockroachdb/pebble"
)

// Project mutations own the association. Freeze it before any provider charge,
// in the same batch as the task, so replacements retain late historic receipts.
// Existing bindings are immutable; metadata is never an association authority.
func (s *SessionStore) bindTaskUsageInBatch(batch *pebble.Batch, account string, task ProjectTaskRecord) error {
	if task.AccountID != account {
		return errors.New("usage task account mismatch")
	}
	ids := []string{task.SessionID}
	for _, attempt := range task.Attempts {
		ids = append(ids, attempt.SessionID)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		session, found, err := s.GetSession(id)
		if err != nil {
			return err
		}
		if !found {
			continue
		} // A queued task may precede session creation.
		if session.AccountScopeID != account {
			return errors.New("usage task session account mismatch")
		}
		var bound []UsageScopeTotal
		exists, err := s.store.GetJSON(usageBindingKey(account, id), &bound)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		scopes := []UsageScopeTotal{{Kind: "task", ProjectID: task.ProjectID, ID: task.ID}}
		if task.WorkerID != "" && task.WorkerRunID != "" {
			run, ok, err := s.WorkerStore().GetWorkerRun(account, task.WorkerID, task.WorkerRunID)
			if err != nil {
				return err
			}
			if !ok || run.AccountScopeID != account || run.WorkerID != task.WorkerID || run.ID != task.WorkerRunID {
				return errors.New("usage task worker linkage mismatch")
			}
			scopes = append(scopes, UsageScopeTotal{Kind: "worker", ID: task.WorkerID}, UsageScopeTotal{Kind: "worker_run", ProjectID: task.WorkerID, ID: task.WorkerRunID})
		}
		if err := setUsageBinding(batch, account, id, scopes); err != nil {
			return err
		}
	}
	return nil
}
