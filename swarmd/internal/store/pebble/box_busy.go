package pebblestore

import (
	"encoding/json"
	"strings"
)

// BoxExecutionCounts reads durable work across accounts. Follow-up work also
// prevents shutdown: it is work the daemon must resume.
func (ws *WorkerStore) BoxExecutionCounts() (followups, reviews int, err error) {
	err = ws.store.IteratePrefix(keyWorkerRunActivePrefix, int(^uint(0)>>1), func(key string, data []byte) error {
		var entry workerRunIndexEntry
		if err := json.Unmarshal(data, &entry); err != nil {
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
		if !found {
			return nil
		}
		if r.FollowupActive() {
			followups++
		}
		if r.Role == WorkerRoleReviewer && !AutomationV2Terminal(r.Status) {
			reviews++
		}
		return nil
	})
	return
}

func (r WorkerRunRecord) FollowupActive() bool {
	return r.FollowupStatus == "running" || r.FollowupStatus == "queued"
}
