package jobautomation

import (
	"errors"
	"fmt"
	"strings"

	pebble "swarm/packages/swarmd/internal/store/pebble"
)

func itemPending(item ItemRecord) bool {
	return item.State == "queued" || item.State == "started" || item.State == itemLimitWaiting || strings.HasPrefix(item.State, "waiting for ")
}

// Validate the entire graph before persisting any queue or dispatching any job.
func matrixDependencies(items []map[string]any, f ForEach) ([]ItemRecord, error) {
	out := make([]ItemRecord, len(items))
	if f.AfterField == "" {
		return out, nil
	}
	if f.IDField == "" {
		f.IDField = "id"
	}
	ids := map[string]int{}
	for i, item := range items {
		id := eventText(item[f.IDField])
		after := eventText(item[f.AfterField])
		if id != "" {
			if _, exists := ids[id]; exists {
				return nil, fmt.Errorf("for_each duplicate id %q", id)
			}
			ids[id] = i
		}
		out[i].ID, out[i].After = id, after
	}
	for i := range out {
		seen := map[int]bool{i: true}
		for next := out[i].After; next != ""; {
			j, ok := ids[next]
			if !ok {
				return nil, fmt.Errorf("for_each unknown dependency id %q", next)
			}
			if seen[j] {
				return nil, fmt.Errorf("for_each dependency cycle at %q", next)
			}
			seen[j] = true
			next = out[j].After
		}
	}
	return out, nil
}

func itemEndKey(account, job string) string {
	return "job_automation/item_end/" + hexKey(account) + "/" + hexKey(job)
}

// itemEndSucceeded reports whether an item job's end, its newest one, succeeded.
func itemEndSucceeded(result Result) bool {
	return result.Status == StatusSucceeded && result.Error == "" && result.JobStatus != "blocked"
}

// dependencyFailed is the error an item records when the item it waits for
// did not finish; nothing else starts with it.
func dependencyFailed(item ItemRecord) bool {
	return item.State == "failed" && item.JobID == "" && item.After != "" && strings.HasPrefix(item.Error, "waits for "+item.After+", which did not finish: ")
}

// reviveItemsLocked lets the for_each items of d follow their jobs' newest
// end. job limits it to that item job's end ("" re-evaluates every item job,
// the owner's rerun). A failed item whose job's newest end succeeded returns
// to done; an item that never started and failed only because it waited on a
// recovered item waits for it again, along its after chain, so the item queue
// dispatches it once its dependency is ready, under max_parallel and with its
// deterministic start key. For the owner's rerun, a dependent of an item that
// was not recovered here waits again only when that item is ready. Items that
// failed for their own reasons and whose job did not succeed stay failed. It
// reports whether d changed; the caller holds s.mu and persists d.
func (s *Store) reviveItemsLocked(d *Delivery, job string) bool {
	changed := false
	dependency := TriggerRunner{Store: s}
	for i := range d.Steps {
		if i >= len(d.Definition) || d.Definition[i].ForEach == nil {
			continue
		}
		items := d.Steps[i].Items
		recovered := map[string]bool{}
		for j := range items {
			item := &items[j]
			if item.JobID == "" || (job != "" && item.JobID != job) {
				continue
			}
			if item.State == "failed" {
				var result Result
				found, err := s.db.GetJSON(itemEndKey(d.Account, item.JobID), &result)
				if err != nil || !found || !itemEndSucceeded(result) {
					continue
				}
				item.State, item.Error, item.RetryAt = "done", "", 0
				changed = true
				if item.ID != "" {
					recovered[item.ID] = true
				}
			}
			// The job just ended successfully: its delivery is judged anew.
			if job != "" && item.State == "done" && item.ID != "" {
				recovered[item.ID] = true
			}
		}
		for progress := true; progress; {
			progress = false
			for j := range items {
				item := &items[j]
				if !dependencyFailed(*item) {
					continue
				}
				if !recovered[item.After] {
					if job != "" {
						continue
					}
					if ready, _ := dependency.itemDependency(d.Account, items, item.After); !ready {
						continue
					}
				}
				item.State, item.Error = "waiting for "+item.After, ""
				if item.ID != "" {
					recovered[item.ID] = true
				}
				changed, progress = true, true
			}
		}
	}
	return changed
}

// ReviveItems re-evaluates, after an item job ended, the for_each items of
// the delivery whose step started it (source is the job's request source).
// The delivery may already be finished: its items are queued again whatever
// its state, and the item queue (tickItems) dispatches them.
func (s *Store) ReviveItems(account, source, job string) error {
	if !pebble.IsWorkflowItemSource(source) || job == "" {
		return nil
	}
	parts := strings.Split(source, "/") // workflow/<automation>/delivery/<id>/step/<step>
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.GetDelivery(account, parts[3])
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if d.AutomationID != parts[1] || !s.reviveItemsLocked(&d, job) {
		return nil
	}
	return s.putDeliveryLocked(d)
}

// The result receipt is written only after consume has materialized every
// matching delivery. Absence must never be mistaken for zero workflows.
func (r *TriggerRunner) itemDependency(account string, items []ItemRecord, id string) (bool, string) {
	for _, item := range items {
		if item.ID != id {
			continue
		}
		if item.State == "failed" {
			return false, item.Error
		}
		if item.State != "done" {
			return false, ""
		}
		var result Result
		found, err := r.Store.db.GetJSON(itemEndKey(account, item.JobID), &result)
		if err != nil || !found {
			return false, ""
		}
		if !itemEndSucceeded(result) {
			reason := result.Error
			if reason == "" {
				reason = "job " + result.Status
			}
			return false, reason
		}
		deliveries, err := r.Store.JobDeliveries(account, item.JobID)
		if err != nil {
			return false, ""
		}
		ready := true
		// JobDeliveries is newest first. Earlier review rounds are history,
		// not failures of the job's current delivery.
		seen := map[string]bool{}
		for _, d := range deliveries {
			if seen[d.AutomationID] {
				continue
			}
			seen[d.AutomationID] = true
			if !Terminal(d.State) {
				ready = false
				continue
			}
			// A sent_back delivery handed nothing over: like a succeeded
			// one with a reopen, its follow-up's delivery is outstanding.
			if d.State != StateSucceeded && d.State != StateSentBack {
				reason := d.BlockedReason
				if reason == "" {
					reason = "workflow " + d.AutomationName + " " + d.State
				}
				for _, step := range d.Steps {
					if d.BlockedReason == "" && step.State == StateFailed && step.Reason != "" {
						reason = step.Reason
						break
					}
				}
				return false, reason
			}
			// A successful reopen only acknowledges sending the follow-up;
			// its delivery is still outstanding, even though this round ended.
			reopened := false
			for _, step := range d.Steps {
				if followup, _ := step.Outcome["followup"].(string); step.Type == StepReopen && step.State == StateSucceeded && followup != "" && result.Followup != followup {
					reopened = true
				}
			}
			if reopened {
				ready = false
				continue
			}
			for _, step := range d.Steps {
				if step.Type == StepDeliver && step.State != StateSucceeded {
					return false, "workflow " + d.AutomationName + " deliver step " + step.ID + " " + step.State
				}
			}
		}
		return ready, ""
	}
	return false, "unknown dependency"
}
