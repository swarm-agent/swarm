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
	// Upgrade reservations written before the full dispatch payload was stored.
	if state.Pending == nil && state.ActiveJob == "schedule-dispatch-pending" && len(state.Events) > 0 {
		receipt := state.Events[len(state.Events)-1]
		slot, err := time.Parse(time.RFC3339, receipt.ID)
		if err != nil {
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
		state.Pending = &scheduleDispatch{Automation: a, Brief: brief, Key: hex.EncodeToString(digest[:]), ReceiptID: receipt.ID}
		if s.db.PutJSON(triggerKey(a), state) != nil {
			return
		}
	}
	// Recover an already reserved dispatch, not a missed schedule slot.
	if state.Pending != nil {
		if !r.finishScheduleDispatch(ctx, a, &state, true) {
			return
		}
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
	if !eligible {
		_ = s.db.PutJSON(key, state)
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
	state.Pending = &scheduleDispatch{Automation: a, Brief: brief, Key: hex.EncodeToString(digest[:]), ReceiptID: receipt.ID}
	state.ActiveJob = "schedule-dispatch-pending"
	if s.db.PutJSON(key, state) != nil {
		return
	}
	r.finishScheduleDispatch(ctx, a, &state, false)
}

func (r *TriggerRunner) finishScheduleDispatch(ctx context.Context, a Automation, state *triggerState, recovering bool) bool {
	pending := state.Pending
	status, job, failure := r.advanceQueued(ctx, pending.Automation, pending.Brief, pending.Key)
	// A recovery error does not prove the original dispatch failed. Retain the
	// reservation until the idempotent start boundary yields its job ID.
	if recovering && status != "started" {
		changed := false
		for i := range state.Events {
			if state.Events[i].ID == pending.ReceiptID {
				changed = state.Events[i].State != "failed"
				state.Events[i].State = "failed"
				state.Events[i].Error = "schedule dispatch recovery blocked; reservation retained: " + failure
			}
		}
		if r.Store.db.PutJSON(triggerKey(a), state) == nil && changed && r.Signal != nil {
			r.Signal(a, "workflow.trigger.failed", pending.ReceiptID)
		}
		return false
	}
	for i := range state.Events {
		if state.Events[i].ID == pending.ReceiptID {
			state.Events[i].State, state.Events[i].JobID, state.Events[i].Error = status, job, failure
		}
	}
	state.Pending = nil
	state.ActiveJob = ""
	if status == "started" {
		state.ActiveJob = job
	}
	if r.Store.db.PutJSON(triggerKey(a), state) != nil {
		return false
	}
	if r.Signal != nil {
		kind := "workflow.trigger.fired"
		if status == "failed" {
			kind = "workflow.trigger.failed"
		}
		r.Signal(a, kind, pending.ReceiptID)
	}
	return true
}
