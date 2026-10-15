package pebblestore

import (
	"encoding/json"
	"github.com/cockroachdb/pebble"
	"strings"
)

// ReserveWorkerCapacity atomically reserves box-wide capacity across accounts.
// Queued receipts remain durable and are retried by worker reconciliation before
// any checkout or provider execution is created. Preparation counts as active.
func (ws *WorkerStore) ReserveWorkerCapacity(r WorkerRunRecord, limit int) (bool, error) {
	ws.store.workersMu.Lock()
	var published *workerRealtimeMutation
	defer func() {
		ws.store.workersMu.Unlock()
		if published != nil {
			ws.store.publishWorkerRealtime(published)
		}
	}()
	var current WorkerRunRecord
	found, err := ws.store.GetJSON(KeyWorkerRun(r.AccountScopeID, r.WorkerID, r.ID), &current)
	if err != nil {
		return false, err
	}
	if !found {
		return false, ErrWorkerNotFound
	}
	if current.Status == "preparing" || current.Status == "running" {
		return true, nil
	}
	if current.Status != "admitted" && current.Status != "queued" {
		return false, ErrWorkerConflict
	}
	prefix := keyWorkerReservationPrefix
	it, err := ws.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return false, err
	}
	defer it.Close()
	active := 0
	for ok := it.First(); ok && active < limit; ok = it.Next() {
		active++
	}
	if err := it.Error(); err != nil {
		return false, err
	}
	admitted := limit > 0 && active < limit
	if admitted {
		current.Status, current.QueueReason = "preparing", ""
	} else {
		current.Status, current.QueueReason = "queued", "box worker concurrency limit reached; waiting for an active run to finish"
	}
	if current.Status == r.Status && current.QueueReason == r.QueueReason {
		return admitted, nil
	}
	m := &workerRealtimeMutation{accountScopeID: r.AccountScopeID, userID: r.UserID, workerID: r.WorkerID}
	if err := m.put(KeyWorkerRun(r.AccountScopeID, r.WorkerID, r.ID), current); err != nil {
		return false, err
	}
	if err := m.setPayload(WorkerRealtimePayload{WorkerID: r.WorkerID, ChangeSummary: "run " + current.Status}); err != nil {
		return false, err
	}
	if err := ws.store.commitWorkerRealtime(m); err != nil {
		return false, err
	}
	published = m
	return admitted, nil
}

// ActiveWorkerSandboxSessions enumerates only unfinished/reserved receipts for
// startup inventory reconciliation, including queued and running follow-ups.
func (ws *WorkerStore) ActiveWorkerSandboxSessions() ([]string, error) {
	var ids []string
	err := ws.store.IteratePrefix(keyWorkerRunActivePrefix, int(^uint(0)>>1), func(key string, value []byte) error {
		var entry workerRunIndexEntry
		if err := json.Unmarshal(value, &entry); err != nil {
			return err
		}
		account, _, ok := strings.Cut(strings.TrimPrefix(key, keyWorkerRunActivePrefix), "/")
		if !ok {
			return ErrWorkerInvalid
		}
		var r WorkerRunRecord
		found, err := ws.store.GetJSON(KeyWorkerRunAccountPrefix+account+"/"+keyPart(entry.WorkerID)+"/"+keyPart(entry.RunID), &r)
		if err != nil {
			return err
		}
		if found && r.FollowupRunID != "" {
			intent, exists, err := NewSessionStore(ws.store).GetV3SessionRunIntent(r.SessionID, r.FollowupRunID)
			if err != nil {
				return err
			}
			if !exists || (intent.Status != V3RunIntentPendingExecutor && intent.Status != V3RunIntentRunning) {
				if err := ws.ReleaseWorkerFollowup(r.AccountScopeID, r.WorkerID, r.ID, r.FollowupRunID); err != nil {
					return err
				}
				r.FollowupStatus = ""
			}
		}
		if found && (!AutomationV2Terminal(r.Status) || r.FollowupStatus != "") && r.SessionID != "" {
			ids = append(ids, r.SessionID)
		}
		return nil
	})
	return ids, err
}
