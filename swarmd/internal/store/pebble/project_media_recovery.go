package pebblestore

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/cockroachdb/pebble"
)

// recoverInterruptedProjectMedia runs only during writable Open, before callers
// can launch generation. Direct media goroutines have no V3 run lifecycle; an
// inactive run on a read is therefore not sufficient evidence of interruption.
func (s *SessionStore) recoverInterruptedProjectMedia() error {
	signals := NewSignalStore(s.store)
	iter, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(KeyProjectTaskAccountPrefix), UpperBound: []byte(KeyProjectTaskAccountPrefix + "\xff")})
	if err != nil {
		return err
	}
	defer iter.Close()
	settle := func(key string, value []byte) error {
		var task ProjectTaskRecord
		if err := json.Unmarshal(value, &task); err != nil {
			return err
		}
		if task.Status != "in_progress" {
			return nil
		}
		switch task.Agent {
		case "image", "video", "sound", "audio":
		default:
			return nil
		}
		if key != KeyProjectTask(task.AccountID, task.ProjectID, task.ID) {
			return fmt.Errorf("media recovery task identity mismatch")
		}
		ready := 0
		for i := range task.Deliverables {
			d := &task.Deliverables[i]
			if d.Status == "ready" || d.Status == "accepted" {
				ready++
				continue
			}
			d.Status = "failed"
			d.Description = "daemon restarted during generation; file did not finish"
		}
		kind, severity := "media.job.failed", SignalSeverityWarning
		task.Status = "failed"
		task.LastError = "daemon restarted during generation; one or more files did not finish"
		task.ActionNeeded = "Review interrupted media generation. No generation was retried."
		task.WhatNotDone = []string{task.LastError}
		summary := "Media job failed: " + task.LastError
		if ready > 0 && ready == len(task.Deliverables) {
			task.Status = "needs_review"
			task.LastError = ""
			task.WhatNotDone = nil
			task.ActionNeeded = fmt.Sprintf("Action Needed: %d media deliverable(s) ready for review.", ready)
			kind, severity = "media.job.succeeded", SignalSeverityInfo
			summary = fmt.Sprintf("Media job made %d %s file(s)", ready, task.Agent)
		}
		attrs := map[string]string{"job_id": "media/" + task.ProjectID + "/" + task.ID, "kind": task.Agent, "files": strconv.Itoa(ready)}
		if task.Model != "" {
			attrs["model"] = task.Model
		}
		// Persist the deduplicated signal first. If task persistence fails, Open fails
		// closed and the next Open retries without duplicating the signal. Usage was
		// recorded at generation time and must never be reconstructed or charged here.
		if _, err := signals.AppendOnce(Signal{Kind: kind, Severity: severity, Source: SignalSourceSwarmd, Account: task.AccountID, Summary: summary, DedupKey: kind + ":" + task.ID, Refs: map[string]string{"project_id": task.ProjectID, "task_id": task.ID}, Attrs: attrs}); err != nil {
			return err
		}
		return s.PutProjectTask(task.AccountID, &task)
	}
	for iter.First(); iter.Valid(); iter.Next() {
		if err := settle(string(iter.Key()), iter.Value()); err != nil {
			return err
		}
	}
	return iter.Error()
}
