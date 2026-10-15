package pebblestore

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/cockroachdb/pebble"
)

// WorkerRunForSession resolves authority from daemon-owned receipts, never from
// editable session metadata. Scan only this account's canonical receipts so old
// receipts do not require a backfill before permission safety takes effect.
func (s *PermissionStore) WorkerRunForSession(account, session string) (WorkerRunRecord, bool, error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return WorkerRunRecord{}, false, errors.New("permission store unavailable")
	}
	if strings.TrimSpace(account) == "" || strings.TrimSpace(session) == "" {
		return WorkerRunRecord{}, false, nil
	}
	prefix := WorkerRunPrefix(account, "")
	it, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return WorkerRunRecord{}, false, err
	}
	defer it.Close()
	for ok := it.First(); ok; ok = it.Next() {
		var r WorkerRunRecord
		if err := json.Unmarshal(it.Value(), &r); err != nil {
			return WorkerRunRecord{}, false, err
		}
		if r.AccountScopeID == account && r.SessionID == session && r.ID != "" && r.WorkerID != "" && string(it.Key()) == KeyWorkerRun(account, r.WorkerID, r.ID) {
			return r, true, nil
		}
	}
	return WorkerRunRecord{}, false, it.Error()
}
