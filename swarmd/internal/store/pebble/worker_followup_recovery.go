package pebblestore

import "encoding/json"

// RecoverClearedWorkerFollowups repairs legacy receipts only when retained
// session lineage proves an initial successful turn followed by another turn.
// It runs before admission at startup, never inferring a follow-up from commits.
func (ws *WorkerStore) RecoverClearedWorkerFollowups() error {
	var runs []WorkerRunRecord
	if err := ws.store.IteratePrefix(KeyWorkerRunAccountPrefix, int(^uint(0)>>1), func(_ string, data []byte) error {
		var r WorkerRunRecord
		if err := json.Unmarshal(data, &r); err != nil {
			return err
		}
		if r.SessionID != "" && r.FollowupStatus == "" && r.FollowupRunID == "" {
			runs = append(runs, r)
		}
		return nil
	}); err != nil {
		return err
	}
	ss := NewSessionStore(ws.store)
	for _, r := range runs {
		snap, found, err := ss.GetSession(r.SessionID)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		if snap.Metadata["worker_id"] != r.WorkerID || snap.Metadata["worker_execution_run_id"] != r.ID {
			continue
		}
		var intents []V3SessionRunIntent
		if err := ws.store.IteratePrefix(V3SessionRunIntentPrefix(r.SessionID), int(^uint(0)>>1), func(_ string, data []byte) error {
			var intent V3SessionRunIntent
			if err := json.Unmarshal(data, &intent); err != nil {
				return err
			}
			intents = append(intents, intent)
			return nil
		}); err != nil {
			return err
		}
		if len(intents) < 2 {
			continue
		}
		var first, latest V3SessionRunIntent
		for _, i := range intents {
			if i.AccountScopeID != r.AccountScopeID {
				continue
			}
			if first.RunID == "" || i.CreatedAt < first.CreatedAt || (i.CreatedAt == first.CreatedAt && i.EventSeq < first.EventSeq) {
				first = i
			}
			if latest.RunID == "" || i.CreatedAt > latest.CreatedAt || (i.CreatedAt == latest.CreatedAt && i.EventSeq > latest.EventSeq) {
				latest = i
			}
		}
		if first.Status != V3RunIntentCompleted || first.RunID == latest.RunID || !isV3RunIntentTerminal(latest.Status) {
			continue
		}
		r.FollowupRunID, r.FollowupStatus, r.FollowupError = latest.RunID, latest.Status, latest.BlockedReason
		m := &workerRealtimeMutation{accountScopeID: r.AccountScopeID, userID: r.UserID, workerID: r.WorkerID}
		if err := m.put(KeyWorkerRun(r.AccountScopeID, r.WorkerID, r.ID), r); err != nil {
			return err
		}
		if err := m.setPayload(WorkerRealtimePayload{WorkerID: r.WorkerID, ChangeSummary: "follow-up outcome recovered"}); err != nil {
			return err
		}
		if err := ws.store.commitWorkerRealtime(m); err != nil {
			return err
		}
	}
	return nil
}
