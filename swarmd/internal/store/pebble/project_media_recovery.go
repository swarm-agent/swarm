package pebblestore

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/cockroachdb/pebble"
)

// recoverInterruptedProjectMedia runs once after startup installs the status
// observer, before callers can launch generation. Direct media goroutines have no V3 run lifecycle; an
// inactive run on a read is therefore not sufficient evidence of interruption.
func (s *SessionStore) recoverInterruptedProjectMedia() error {
	signals := NewSignalStore(s.store)
	iter, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte("project_task_summary/v3/"), UpperBound: []byte("project_task_summary/v3/\xff")})
	if err != nil {
		return err
	}
	defer iter.Close()
	settle := func(key string, value []byte) error {
		if !strings.Contains(key, "/rows/") {
			return nil
		}
		var row taskSummaryRow
		if err := json.Unmarshal(value, &row); err != nil {
			return err
		}
		task := row.Task
		if task.Status != "in_progress" {
			return nil
		}
		switch task.Agent {
		case "image", "video", "sound", "audio":
		default:
			return nil
		}
		if key != taskSummaryRowKey(task) || row.Version != taskSummaryVersion {
			return fmt.Errorf("media recovery summary identity mismatch")
		}
		full, found, err := s.GetProjectTask(task.AccountID, task.ProjectID, task.ID)
		if err != nil {
			return err
		}
		if !found || full.ID != task.ID || full.AccountID != task.AccountID || full.ProjectID != task.ProjectID || full.Status != "in_progress" || full.Agent != task.Agent {
			return fmt.Errorf("media recovery task mismatch")
		}
		task = *full
		ready := 0
		for i := range task.Deliverables {
			d := &task.Deliverables[i]
			if d.Status == "ready" || d.Status == "accepted" {
				ready++
				continue
			}
			// Preserve terminal failures and their original provider diagnostics.
			if d.Status == "generating" || d.Status == "pending" {
				d.Status = "failed"
				d.Description = "daemon restarted during generation; file did not finish"
			}
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
		// Persist the deduplicated signal first. If task persistence fails, the
		// next startup retries without duplicating the signal. Usage was
		// recorded at generation time and must never be reconstructed or charged here.
		if _, err := signals.AppendOnce(Signal{Kind: kind, Severity: severity, Source: SignalSourceSwarmd, Account: task.AccountID, Summary: summary, DedupKey: kind + ":" + task.ID, Refs: map[string]string{"project_id": task.ProjectID, "task_id": task.ID}, Attrs: attrs}); err != nil {
			return err
		}
		return s.PutProjectTask(task.AccountID, &task)
	}
	for iter.First(); iter.Valid(); iter.Next() {
		if err := settle(string(iter.Key()), iter.Value()); err != nil {
			// Isolate corrupt tasks; leave their durable state for repair and retry
			// at next startup. Do not log payloads or decoder errors (media/private text).
			log.Printf("warning: interrupted media task recovery failed; deferred until next restart")
		}
	}
	return iter.Error()
}
