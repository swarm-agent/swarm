package automation

import (
	"context"
	"fmt"
	"sort"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Progress is a read projection, never an execution grant or fixed daily quota.
// Counts are observed lower bounds unless HistoryComplete is true. Even a complete
// scan is not a transaction snapshot: consumers repair through automation durable
// invalidations/reconnect and explicit refresh, never recurring network polling.
type Progress struct {
	AutomationID string `json:"automation_id"`
	DefinitionRevision uint64 `json:"definition_revision"`
	Schedule store.AutomationSchedulePolicy `json:"schedule"`
	IntervalAnchor *int64 `json:"interval_anchor,omitempty"`
	DisplayTimezone string `json:"display_timezone"`
	DayStart int64 `json:"day_start"`
	DayEnd int64 `json:"day_end"`
	AsOf int64 `json:"as_of"`
	Freshness string `json:"freshness"`
	HistoryComplete bool `json:"history_complete"`
	ForecastComplete bool `json:"forecast_complete"` // today's revision-window enumeration
	UpcomingComplete bool `json:"upcoming_complete"`
	ForecastHorizonEnd int64 `json:"forecast_horizon_end"`
	OutcomeAvailability string `json:"outcome_availability"`
	PlannedSlots []ProgressSlot `json:"planned_slots"`
	UpcomingSlots []ProgressSlot `json:"upcoming_slots"`
	NextEligible *ProgressSlot `json:"next_eligible,omitempty"`
	NoNextReason string `json:"no_next_reason,omitempty"`
	Counts map[string]int `json:"counts"`
	ManualCounts map[string]int `json:"manual_counts"`
	UnknownTriggerCount int `json:"unknown_trigger_count"`
	Occurrences []ProgressOccurrence `json:"occurrences"`
	TimingAvailability string `json:"timing_availability"`
	MissedAvailability string `json:"missed_availability"`
	LatestRecorded *ProgressOccurrence `json:"latest_recorded,omitempty"`
}

type ProgressSlot struct {
	ScheduledAt int64 `json:"scheduled_at"`
	DefinitionRevision uint64 `json:"definition_revision"`
	Forecast bool `json:"forecast"`
}

type ProgressOccurrence struct {
	ID string `json:"id"`
	Revision uint64 `json:"revision"`
	DefinitionRevision uint64 `json:"definition_revision"`
	ScheduledAt int64 `json:"scheduled_at"`
	State string `json:"state"`
	TriggerKind string `json:"trigger_kind"`
	RecordedAt int64 `json:"recorded_at"`
}

// scheduleSlots reuses admission's evaluator. Intervals retain millisecond anchors
// exactly as TickAt does; civil-day boundaries never reset elapsed-time intervals.
func scheduleSlots(ctx context.Context, r store.AutomationRecord, start, end int64) ([]ProgressSlot, error) {
	s, err := NormalizeSchedule(r.Definition.Schedule)
	if err != nil { return nil, err }
	out := []ProgressSlot{}
	if s.Kind != "cron" && s.Kind != "interval" { return out, nil }
	if start < r.WrittenAt { start = r.WrittenAt }
	step := int64(60000)
	at := time.UnixMilli(start).Truncate(time.Minute).UnixMilli()
	if s.Kind == "interval" {
		step = s.IntervalSeconds * 1000
		at = r.WrittenAt + (start-r.WrittenAt)/step*step
	}
	if at < start { at += step }
	for ; at < end; at += step {
		if err := ctx.Err(); err != nil { return nil, err }
		due, err := Due(s, time.UnixMilli(at), time.UnixMilli(r.WrittenAt))
		if err != nil { return nil, err }
		if due { out = append(out, ProgressSlot{ScheduledAt: at, DefinitionRevision: r.Revision, Forecast: true}) }
	}
	return out, nil
}

// ScheduleProgress bounds definition history at 50 revisions, occurrence reads
// at eight 50-result/512-key pages, and forecasts at one civil day plus 48 hours.
// A missing next slot means outside that horizon, not that a schedule never runs.
func (s *Service) ScheduleProgress(ctx context.Context, p Principal, scope store.AutomationScope, id, timezone string) (Progress, error) {
	var out Progress
	if err := s.authorize(ctx, p, scope, "read"); err != nil { return out, err }
	if timezone == "" || timezone == "Local" { return out, ErrInvalid }
	loc, err := time.LoadLocation(timezone)
	if err != nil { return out, ErrInvalid }
	now := s.now()
	local := now.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	head, found, err := s.repo.GetAutomationRecord(scope, id, "definition", id, 0)
	if err != nil { return out, err }
	if !found || head.Definition == nil { return out, ErrNotFound }
	out = Progress{AutomationID: id, DefinitionRevision: head.Revision, Schedule: head.Definition.Schedule,
		DisplayTimezone: timezone, DayStart: day.UnixMilli(), DayEnd: day.AddDate(0, 0, 1).UnixMilli(), AsOf: now.UnixMilli(),
		Freshness: "non_atomic_read", HistoryComplete: true, ForecastComplete: true,
		Counts: map[string]int{}, ManualCounts: map[string]int{}, PlannedSlots: []ProgressSlot{}, UpcomingSlots: []ProgressSlot{}, Occurrences: []ProgressOccurrence{},
		TimingAvailability: "actual_start_and_completion_unavailable; recorded_at_is_state_write", MissedAvailability: "unavailable; absence_is_not_a_recorded_skip"}
	out.Schedule, err = NormalizeSchedule(out.Schedule)
	if err != nil { return Progress{}, err }
	out.UpcomingComplete = true
	out.ForecastHorizonEnd = now.Add(48*time.Hour).UnixMilli()
	out.OutcomeAvailability = "audit_details_not_scanned; latest_recorded_is_occurrence_state"
	for _, state := range []string{"pending", "running", "completed", "failed", "blocked", "cancelled", "cancelling", "skipped"} {
		out.Counts[state] = 0
		out.ManualCounts[state] = 0
	}
	if out.Schedule.Kind == "interval" { anchor := head.WrittenAt; out.IntervalAnchor = &anchor }
	// Immutable revision validity windows preserve earlier schedules after edits.
	end := out.DayEnd
	r := head
	for n := 0; n < 50; n++ {
		if r.Definition.Enabled {
			slots, err := scheduleSlots(ctx, r, out.DayStart, end)
			if err != nil { return Progress{}, err }
			out.PlannedSlots = append(out.PlannedSlots, slots...)
		}
		if r.WrittenAt <= out.DayStart || r.Revision == 1 { break }
		end = r.WrittenAt
		if n == 49 { out.ForecastComplete = false; break }
		r, found, err = s.repo.GetAutomationRecord(scope, id, "definition", id, r.Revision-1)
		if err != nil { return Progress{}, err }
		if !found || r.Definition == nil { out.ForecastComplete = false; break }
	}
	cursor := ""
	seen := map[string]bool{}
	active := false
	for page := 0; page < 8; page++ {
		if err := ctx.Err(); err != nil { return Progress{}, err }
		rows, next, err := s.repo.SearchAutomationRecords(store.AutomationSearch{Scope: scope, AutomationID: id, Kind: "occurrence", Cursor: cursor, Limit: 50})
		if err != nil { return Progress{}, err }
		for _, row := range rows {
			o := row.Occurrence
			if o == nil || seen[row.ID] { continue }
			seen[row.ID] = true
			if o.State == "pending" || o.State == "running" || o.State == "cancelling" { active = true }
			kind := o.TriggerKind
			// Legacy scheduled identity can be verified, but a hash cannot reveal
			// manual versus event origin. Never classify those by schedule kind.
			if kind == "" && o.TriggerIdentity == executionKey("schedule", "", fmt.Sprint(o.ScheduledAt)) { kind = "schedule" }
			if kind == "" { kind = "unknown" }
			item := ProgressOccurrence{ID: row.ID, Revision: row.Revision, DefinitionRevision: o.DefinitionRevision, ScheduledAt: o.ScheduledAt, State: o.State, TriggerKind: kind, RecordedAt: row.WrittenAt}
			if out.LatestRecorded == nil || row.WrittenAt > out.LatestRecorded.RecordedAt { copy := item; out.LatestRecorded = &copy }
			if o.ScheduledAt < out.DayStart || o.ScheduledAt >= out.DayEnd { continue }
			out.Occurrences = append(out.Occurrences, item)
			switch kind {
			case "schedule": out.Counts[o.State]++
			case "manual": out.ManualCounts[o.State]++
			case "unknown": out.UnknownTriggerCount++
			}
		}
		cursor = next
		if cursor == "" { break }
	}
	out.HistoryComplete = cursor == ""
	sort.Slice(out.PlannedSlots, func(i, j int) bool { return out.PlannedSlots[i].ScheduledAt < out.PlannedSlots[j].ScheduledAt })
	sort.Slice(out.Occurrences, func(i, j int) bool { return out.Occurrences[i].ScheduledAt < out.Occurrences[j].ScheduledAt })
	slots, err := scheduleSlots(ctx, head, out.AsOf+1, now.Add(48*time.Hour).UnixMilli())
	if err != nil { return Progress{}, err }
	// Cap the wire forecast independently from evaluator work.
	if len(slots) > 100 { slots = slots[:100]; out.UpcomingComplete = false }
	out.UpcomingSlots = slots
	switch {
	case out.Schedule.Kind == "manual" || out.Schedule.Kind == "event": out.NoNextReason = "not_time_scheduled"
	case !head.Definition.Enabled: out.NoNextReason = "paused"
	case head.Definition.Authorization.Mode != "approved_policy": out.NoNextReason = "approval_required"
	case head.Definition.Authorization.ExpiresAt <= out.AsOf: out.NoNextReason = "expired"
	case out.Schedule.OverlapPolicy == "serialize" && (!out.HistoryComplete || active): out.NoNextReason = "overlap_or_history_unknown"
	default:
		_, checkErr := s.CheckRun(ctx, p, scope, id, head.Revision)
		if checkErr != nil { out.NoNextReason = "authorization_or_plan_unavailable" } else if len(slots) == 0 { out.NoNextReason = "outside_48h_horizon" } else if slots[0].ScheduledAt >= head.Definition.Authorization.ExpiresAt { out.NoNextReason = "expires_before_next_slot" } else { slot := slots[0]; out.NextEligible = &slot }
	}
	return out, nil
}
