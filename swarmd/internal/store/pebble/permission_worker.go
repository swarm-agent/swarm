package pebblestore

import (
	"errors"
	"github.com/cockroachdb/pebble"
	"strings"
)

func keyWorkerRunSession(account, session string) string {
	return "worker/runidx/session/" + keyPart(account) + "/" + keyPart(session)
}

// WorkerRunForSession performs one index read (two reads for a worker) regardless
// of history size. The index is only a locator; the canonical receipt owns the
// account and session binding. Startup backfills pre-index receipts before serving.
func (s *PermissionStore) WorkerRunForSession(account, session string) (WorkerRunRecord, bool, error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return WorkerRunRecord{}, false, errors.New("permission store unavailable")
	}
	if strings.TrimSpace(account) == "" || strings.TrimSpace(session) == "" {
		return WorkerRunRecord{}, false, nil
	}
	snap := s.store.db.NewSnapshot()
	defer snap.Close()
	return workerRunForSessionFromReader(snap, account, session)
}

func workerRunForSessionFromReader(snap pebble.Reader, account, session string) (WorkerRunRecord, bool, error) {
	var entry workerRunIndexEntry
	found, err := getJSONFromReader(snap, keyWorkerRunSession(account, session), &entry)
	if err != nil || !found {
		return WorkerRunRecord{}, false, err
	}
	var run WorkerRunRecord
	found, err = getJSONFromReader(snap, KeyWorkerRun(account, entry.WorkerID, entry.RunID), &run)
	if err != nil {
		return WorkerRunRecord{}, false, err
	}
	if !found || run.ID != entry.RunID || run.WorkerID != entry.WorkerID || run.AccountScopeID != account || run.SessionID != session {
		return WorkerRunRecord{}, false, errors.New("worker session index does not match canonical receipt")
	}
	return run, true, nil
}
