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

// itemEndSucceeded reports whether an item job's end, its newest one,
// succeeded. The error of a run that succeeded is an earlier run's: worker
// results built before a successful run dropped it still carry the old
// blocker, and like Result.EndedBlocked that text alone never makes a
// successful end a failure.
func itemEndSucceeded(result Result) bool {
	return result.Status == StatusSucceeded && result.JobStatus != "blocked"
}

// RevivedItem is a for_each item a revive took out of failed: back to done
// (its job's newest end succeeded and was delivered) or to waiting for the
// item it depends on.
type RevivedItem struct {
	Step  string `json:"step"`
	Index int    `json:"index"`
	ID    string `json:"id,omitempty"`
	JobID string `json:"job_id,omitempty"`
	State string `json:"state"`
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
// deterministic start key. For the owner's rerun, a failed item returns to
// done only when its job is ready as a dependency (itemJobReady: its newest
// end succeeded and its workflows delivered), and a dependent of an item that
// was not recovered here waits again only when that item is ready. Items that
// failed for their own reasons and whose job did not succeed stay failed. It
// returns the items it revived; the caller holds s.mu and persists d.
func (s *Store) reviveItemsLocked(d *Delivery, job string) []RevivedItem {
	revived := []RevivedItem{}
	dependency := TriggerRunner{Store: s}
	for i := range d.Steps {
		if i >= len(d.Definition) || d.Definition[i].ForEach == nil {
			continue
		}
		items := d.Steps[i].Items
		note := func(item ItemRecord) {
			revived = append(revived, RevivedItem{Step: d.Steps[i].ID, Index: item.Index, ID: item.ID, JobID: item.JobID, State: item.State})
		}
		recovered := map[string]bool{}
		for j := range items {
			item := &items[j]
			if item.JobID == "" || (job != "" && item.JobID != job) {
				continue
			}
			if item.State == "failed" {
				if job == "" {
					if ready, _ := dependency.itemJobReady(d.Account, item.JobID); !ready {
						continue
					}
				} else {
					var result Result
					found, err := s.db.GetJSON(itemEndKey(d.Account, item.JobID), &result)
					if err != nil || !found || !itemEndSucceeded(result) {
						continue
					}
				}
				item.State, item.Error, item.RetryAt = "done", "", 0
				note(*item)
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
				note(*item)
				if item.ID != "" {
					recovered[item.ID] = true
				}
				progress = true
			}
		}
	}
	return revived
}

// ReviveItems re-evaluates, after an item job ended, the for_each items of
// the delivery whose step started it (source is the job's request source).
// The delivery may already be finished: it then runs again until the revived
// items settle, and the item queue (tickItems) dispatches them.
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
	if d.AutomationID != parts[1] || len(s.reviveItemsLocked(&d, job)) == 0 {
		return nil
	}
	reopenForItems(&d)
	return s.putDeliveryLocked(d)
}

// itemsPending reports whether any for_each item of d is still to run.
func itemsPending(d Delivery) bool {
	for _, step := range d.Steps {
		for _, item := range step.Items {
			if itemPending(item) {
				return true
			}
		}
	}
	return false
}

// reopenForItems makes a finished delivery whose items were revived running
// again until they settle (putDeliveryLocked finishes it again with the state
// it had). Its steps keep their records: the runner does not run it
// (Runner.dispatch skips it); only the item queue advances it.
func reopenForItems(d *Delivery) {
	if !Terminal(d.State) || !itemsPending(*d) {
		return
	}
	d.ItemsReopened, d.State, d.FinishedAt = d.State, StateRunning, 0
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
		return r.itemJobReady(account, item.JobID)
	}
	return false, "unknown dependency"
}

// itemJobReady reports whether an item job is ready for the items waiting on
// it: its newest end (the item-end receipt) succeeded and the newest delivery
// of each of its workflows finished delivering. A reason means it will not
// become ready without another end; none with false means not yet.
func (r *TriggerRunner) itemJobReady(account, job string) (bool, string) {
	var result Result
	found, err := r.Store.db.GetJSON(itemEndKey(account, job), &result)
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
	deliveries, err := r.Store.JobDeliveries(account, job)
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
