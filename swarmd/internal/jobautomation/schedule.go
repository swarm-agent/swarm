package jobautomation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// NextScheduleRun returns the next civil minute, bounded to cover leap years.
// Zero means no occurrence within eight years (for example February 30).
func (a Automation) NextScheduleRun(after time.Time) time.Time {
	if a.On == nil || a.On.Schedule == nil {
		return time.Time{}
	}
	spec := a.On.Schedule
	cron, err := pebblestore.ParseCron(spec.Cron)
	if err != nil {
		return time.Time{}
	}
	zone := spec.Timezone
	if zone == "" {
		zone = "UTC"
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return time.Time{}
	}
	at := after.Truncate(time.Minute).Add(time.Minute)
	end := at.AddDate(8, 0, 0)
	for at.Before(end) {
		local := at.In(loc)
		if cron.Matches(local) {
			return at
		}
		at = at.Add(time.Minute)
	}
	return time.Time{}
}

func (r *TriggerRunner) tickSchedule(ctx context.Context, a Automation) {
	s := r.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.Get(a.Account, a.ID)
	if err != nil || current.Revision != a.Revision || !current.Enabled || current.Proposed {
		return
	}
	state, err := s.triggerState(a)
	if err != nil {
		return
	}
	now := s.now()
	key := triggerKey(a)
	if r.scheduleSeen == nil {
		r.scheduleSeen = map[string]string{}
	}
	first := r.scheduleSeen[key] != state.Fingerprint
	if first {
		// Re-establish the clock on process start or definition change. Never replay
		// a persisted due slot after downtime, even within its original minute.
		r.scheduleSeen[key] = state.Fingerprint
	}
	if !first && (state.NextPoll == 0 || state.NextPoll > now.UnixMilli()) {
		return
	}
	due := state.NextPoll != 0 && state.NextPoll <= now.UnixMilli()
	receipt := TriggerReceipt{}
	if due {
		slot := time.UnixMilli(state.NextPoll)
		receipt = TriggerReceipt{ID: slot.UTC().Format(time.RFC3339), ReceivedAt: now.UnixMilli(), State: "skipped"}
	}
	next := a.NextScheduleRun(now)
	state.NextPoll = 0
	if !next.IsZero() {
		state.NextPoll = next.UnixMilli()
	}
	if !due {
		_ = s.db.PutJSON(key, state)
		return
	}
	// Only the current minute is eligible. Older slots are deliberately skipped.
	slot, _ := time.Parse(time.RFC3339, receipt.ID)
	eligible := !first && now.Sub(slot) < time.Minute
	if eligible && state.ActiveJob == "schedule-dispatch-pending" {
		receipt.State, receipt.Error = "failed", "previous schedule dispatch outcome unknown; operator reconciliation required"
		eligible = false
	}
	if eligible && state.ActiveJob != "" {
		running, err := r.Running(ctx, a, state.ActiveJob)
		if err != nil {
			receipt.State, receipt.Error = "failed", "running job status unavailable"
			eligible = false
		} else if running {
			eligible = false
		} else {
			state.ActiveJob = ""
		}
	}
	state.Events = append(state.Events, receipt)
	if len(state.Events) > maxTriggerItems {
		state.Events = state.Events[len(state.Events)-maxTriggerItems:]
	}
	// Reserve before dispatch. An interrupted dispatch with an unknown job ID
	// must fail closed rather than overlap a possibly running job after restart.
	if eligible {
		state.ActiveJob = "schedule-dispatch-pending"
	}
	if s.db.PutJSON(key, state) != nil {
		return
	}
	if !eligible {
		return
	}
	zone := a.On.Schedule.Timezone
	if zone == "" {
		zone = "UTC"
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return
	}
	local := slot.In(loc)
	brief := strings.NewReplacer("{{schedule.date}}", local.Format("2006-01-02"), "{{schedule.time}}", local.Format("15:04")).Replace(a.On.Start.Brief)
	digest := sha256.Sum256([]byte(a.ID + "/" + state.Fingerprint + "/" + receipt.ID))
	status, job, failure := r.advanceQueued(ctx, a, brief, hex.EncodeToString(digest[:]))
	e := &state.Events[len(state.Events)-1]
	e.State, e.JobID, e.Error = status, job, failure
	state.ActiveJob = ""
	if status == "started" {
		state.ActiveJob = job
	}
	if s.db.PutJSON(key, state) != nil {
		return
	}
	if r.Signal != nil {
		kind := "workflow.trigger.fired"
		if status == "failed" {
			kind = "workflow.trigger.failed"
		}
		r.Signal(a, kind, e.ID)
	}
}
