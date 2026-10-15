package pebblestore

import (
	"encoding/json"
	"fmt"
	"github.com/cockroachdb/pebble"
)

// ReviewAdmissionRecoveryRuns reads durable review receipts, including failed
// admissions that never created a session. Bound the scan and fail closed.
func (ws *WorkerStore) ReviewAdmissionRecoveryRuns() ([]WorkerRunRecord, error) {
	prefix := KeyWorkerRunAccountPrefix
	it, err := ws.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return nil, err
	}
	defer it.Close()
	var rows []WorkerRunRecord
	count, size := 0, 0
	for ok := it.First(); ok; ok = it.Next() {
		count++
		size += len(it.Value())
		if count > 100000 || size > 64*1024*1024 {
			return nil, fmt.Errorf("review recovery scan budget exceeded")
		}
		var r WorkerRunRecord
		if err := json.Unmarshal(it.Value(), &r); err != nil {
			return nil, err
		}
		if string(it.Key()) != KeyWorkerRun(r.AccountScopeID, r.WorkerID, r.ID) {
			return nil, fmt.Errorf("review recovery receipt identity mismatch")
		}
		if r.Role == WorkerRoleReviewer {
			rows = append(rows, r)
		}
	}
	return rows, it.Error()
}
