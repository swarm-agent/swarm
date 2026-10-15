package pebblestore

import (
	"encoding/json"
	"errors"
	"github.com/cockroachdb/pebble"
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
	prefix := KeyWorkerRunAccountPrefix
	it, err := ws.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return false, err
	}
	defer it.Close()
	active, rows, bytes := 0, 0, 0
	for ok := it.First(); ok; ok = it.Next() {
		rows++
		bytes += len(it.Value())
		if rows > 10000 || bytes > 32<<20 {
			return false, errors.New("worker capacity scan budget exceeded")
		}
		var other WorkerRunRecord
		if err := json.Unmarshal(it.Value(), &other); err != nil {
			return false, err
		}
		if other.Status == "running" || other.Status == "preparing" {
			active++
		}
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
