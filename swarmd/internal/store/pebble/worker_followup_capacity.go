package pebblestore

import "github.com/cockroachdb/pebble"

const keyWorkerReservationPrefix = "worker/reservation/"

func keyWorkerReservation(account, worker, run string) string {
	return keyWorkerReservationPrefix + keyPart(account) + "/" + keyPart(worker) + "/" + keyPart(run)
}

// ReserveWorkerFollowup serializes follow-up admission with ordinary jobs. An
// empty run ID registers the message preparation as queued without claiming a
// slot: the V3 append may still fail. Executor enqueue reserves using the actual
// durable intent ID before launching any goroutine. Original outcomes stay intact.
func (ws *WorkerStore) ReserveWorkerFollowup(account, worker, run, intent string, limit int) (bool, error) {
	ws.store.workersMu.Lock()
	var published *workerRealtimeMutation
	defer func() {
		ws.store.workersMu.Unlock()
		if published != nil {
			ws.store.publishWorkerRealtime(published)
		}
	}()
	r, found, err := ws.GetWorkerRun(account, worker, run)
	if err != nil {
		return false, err
	}
	if !found || (r.Status != "succeeded" && r.Status != "failed") || r.CancelRequested {
		return false, ErrWorkerConflict
	}
	if r.FollowupStatus == "running" {
		return r.FollowupRunID == intent, nil
	}
	active := 0
	it, err := ws.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(keyWorkerReservationPrefix), UpperBound: []byte(keyWorkerReservationPrefix + "\xff")})
	if err != nil {
		return false, err
	}
	for ok := it.First(); ok && active < limit; ok = it.Next() {
		active++
	}
	err = it.Error()
	it.Close()
	if err != nil {
		return false, err
	}
	admitted := intent != "" && limit > 0 && active < limit
	r.FollowupStatus, r.FollowupRunID = "queued", intent
	r.QueueReason = "follow-up waiting for box worker capacity"
	if admitted {
		r.FollowupStatus, r.QueueReason = "running", ""
	}
	m := &workerRealtimeMutation{accountScopeID: account, userID: r.UserID, workerID: worker}
	if err = m.put(KeyWorkerRun(account, worker, run), r); err != nil {
		return false, err
	}
	if err = m.setPayload(WorkerRealtimePayload{WorkerID: worker, ChangeSummary: "follow-up " + r.FollowupStatus}); err != nil {
		return false, err
	}
	if err = ws.store.commitWorkerRealtime(m); err != nil {
		return false, err
	}
	published = m
	return admitted, nil
}

// ReleaseWorkerFollowup fences delayed completion against a newer intent.
func (ws *WorkerStore) ReleaseWorkerFollowup(account, worker, run, intent string) error {
	ws.store.workersMu.Lock()
	var published *workerRealtimeMutation
	defer func() {
		ws.store.workersMu.Unlock()
		if published != nil {
			ws.store.publishWorkerRealtime(published)
		}
	}()
	r, found, err := ws.GetWorkerRun(account, worker, run)
	if err != nil {
		return err
	}
	if !found {
		return ErrWorkerNotFound
	}
	if r.FollowupRunID != intent || r.FollowupStatus == "" {
		return nil
	}
	r.FollowupStatus, r.FollowupRunID, r.QueueReason = "", "", ""
	m := &workerRealtimeMutation{accountScopeID: account, userID: r.UserID, workerID: worker}
	if err = m.put(KeyWorkerRun(account, worker, run), r); err != nil {
		return err
	}
	if err = m.setPayload(WorkerRealtimePayload{WorkerID: worker, ChangeSummary: "follow-up ended"}); err != nil {
		return err
	}
	if err = ws.store.commitWorkerRealtime(m); err != nil {
		return err
	}
	published = m
	return nil
}
