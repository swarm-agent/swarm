package pebblestore

import (
	"encoding/json"
	"strings"
)

// BoxExecutionCounts reads durable work across accounts. Queued landing and
// follow-up work also prevents shutdown: it is work the daemon must resume.
func (ws *WorkerStore) BoxExecutionCounts() (followups, reviews, landing int, err error) {
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
	if err != nil {
		return
	}
	err = ws.store.IteratePrefix("publish_queue/", int(^uint(0)>>1), func(_ string, data []byte) error {
		var entries []struct {
			State               string `json:"delivery_state"`
			NotificationPending bool   `json:"notification_pending"`
			SendBackPending     bool   `json:"send_back_pending"`
		}
		if err := json.Unmarshal(data, &entries); err != nil {
			return err
		}
		for _, e := range entries {
			if e.State == "queued" || e.State == "landing" || e.NotificationPending || e.SendBackPending {
				landing++
			}
		}
		return nil
	})
	return
}

func (r WorkerRunRecord) FollowupActive() bool {
	return r.FollowupStatus == "running" || r.FollowupStatus == "queued"
}
