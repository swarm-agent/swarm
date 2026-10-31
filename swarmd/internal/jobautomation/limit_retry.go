package jobautomation

import "errors"

// LimitRetry is one agent step or for-each item, waiting for a provider's
// limit to reset, that a retry-now reached.
type LimitRetry struct {
	DeliveryID string `json:"delivery_id"`
	Automation string `json:"workflow"`
	StepID     string `json:"step"`
	// Item is the for-each item's index; nil for the step itself.
	Item *int `json:"item,omitempty"`
	// JobID is the job that hit the limit ("" when none is known).
	JobID string `json:"job_id,omitempty"`
}

// limitStepJob is the job a limit-waiting step waits on or that hit the
// limit: the same job when it waits for its own rerun (job_id), else the
// limited job (limited_job_id).
func limitStepJob(step StepRecord) string {
	if job, _ := step.Outcome["job_id"].(string); job != "" {
		return job
	}
	job, _ := step.Outcome["limited_job_id"].(string)
	return job
}

// RetryLimitWaitsNow makes the account's agent steps and for-each items that
// wait for a provider's limit due now instead of at the reset the provider
// stated, for when the person fixed the provider. match (nil: every one)
// picks them by the job that hit the limit. Nothing else changes: the runner
// then takes its usual path for a reset that is due (a new job under the
// next .limit<n> key, or the same job read again), so the bound on waits and
// the jobs' idempotency stay as they are, and a job that hits the limit again
// waits again. A step whose delivery is running right now is left to that
// run. A second call finds nothing waiting in the future and changes nothing.
func (r *Runner) RetryLimitWaitsNow(account string, match func(jobID string) bool) ([]LimitRetry, error) {
	out := []LimitRetry{}
	if r == nil || r.store == nil || account == "" {
		return out, nil
	}
	list, err := r.store.Unfinished()
	if err != nil {
		return out, err
	}
	now := r.now().UnixMilli()
	picked := func(job string) bool { return match == nil || match(job) }
	for _, listed := range list {
		if listed.Account != account {
			continue
		}
		// Claim the delivery so dispatch does not start it while its steps
		// change; a delivery already running keeps its steps.
		r.mu.Lock()
		claimed := !r.active[listed.ID]
		if claimed {
			r.active[listed.ID] = true
		}
		r.mu.Unlock()
		retried, err := r.store.retryLimitWaitsNow(account, listed.ID, now, claimed, picked)
		if claimed {
			r.mu.Lock()
			delete(r.active, listed.ID)
			r.mu.Unlock()
		}
		if err != nil {
			return out, err
		}
		out = append(out, retried...)
	}
	if len(out) > 0 {
		r.Wake()
	}
	return out, nil
}

// retryLimitWaitsNow moves one delivery's limit waits that match to now,
// under the store's lock (the item queue's own lock). steps is false when
// the delivery runner holds the delivery: only its items change then.
func (s *Store) retryLimitWaitsNow(account, id string, now int64, steps bool, picked func(string) bool) ([]LimitRetry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.GetDelivery(account, id)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if Terminal(d.State) {
		return nil, nil
	}
	var out []LimitRetry
	for i := range d.Steps {
		rec := &d.Steps[i]
		if steps && LimitWaiting(*rec) && rec.NextAttemptAt > now && picked(limitStepJob(*rec)) {
			rec.NextAttemptAt = now
			out = append(out, LimitRetry{DeliveryID: d.ID, Automation: d.AutomationName, StepID: rec.ID, JobID: limitStepJob(*rec)})
		}
		for j := range rec.Items {
			item := &rec.Items[j]
			if item.State == itemLimitWaiting && item.RetryAt > now && picked(item.JobID) {
				item.RetryAt = now
				index := item.Index
				out = append(out, LimitRetry{DeliveryID: d.ID, Automation: d.AutomationName, StepID: rec.ID, Item: &index, JobID: item.JobID})
			}
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, s.putDeliveryLocked(d)
}
