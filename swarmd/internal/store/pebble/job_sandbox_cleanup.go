package pebblestore

import "encoding/json"

// WorkerSandboxRuns includes terminal receipts so failed removals survive restart.
func (ws *WorkerStore) WorkerSandboxRuns() ([]WorkerRunRecord, error) {
	var runs []WorkerRunRecord
	err := ws.store.IteratePrefix(KeyWorkerRunAccountPrefix, int(^uint(0)>>1), func(_ string, data []byte) error {
		var r WorkerRunRecord
		if err := json.Unmarshal(data, &r); err != nil {
			return err
		}
		if r.SessionID != "" {
			runs = append(runs, r)
		}
		return nil
	})
	return runs, err
}
func (ws *WorkerStore) SetSandboxCleanupError(r WorkerRunRecord, message string) error {
	ws.store.workersMu.Lock()
	defer ws.store.workersMu.Unlock()
	current, found, err := ws.GetWorkerRun(r.AccountScopeID, r.WorkerID, r.ID)
	if err != nil {
		return err
	}
	if !found {
		return ErrWorkerNotFound
	}
	if current.SandboxCleanupError == message {
		return nil
	}
	current.SandboxCleanupError = message
	m := &workerRealtimeMutation{accountScopeID: r.AccountScopeID, userID: r.UserID, workerID: r.WorkerID}
	if err := m.put(KeyWorkerRun(r.AccountScopeID, r.WorkerID, r.ID), current); err != nil {
		return err
	}
	if err := m.setPayload(WorkerRealtimePayload{WorkerID: r.WorkerID, ChangeSummary: "sandbox cleanup updated"}); err != nil {
		return err
	}
	return ws.store.commitWorkerRealtime(m)
}
