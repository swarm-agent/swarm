package jobautomation

import (
	"fmt"
	"strings"
)

func itemPending(item ItemRecord) bool {
	return item.State == "queued" || item.State == "started" || strings.HasPrefix(item.State, "waiting for ")
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
		if result.Status != StatusSucceeded || result.Error != "" || result.JobStatus == "blocked" {
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
		for _, d := range deliveries {
			if !Terminal(d.State) {
				ready = false
				continue
			}
			if d.State != StateSucceeded {
				reason := d.BlockedReason
				if reason == "" {
					reason = "workflow " + d.AutomationName + " " + d.State
				}
				for _, step := range d.Steps {
					if step.State == StateFailed && step.Reason != "" {
						reason = step.Reason
						break
					}
				}
				return false, reason
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
