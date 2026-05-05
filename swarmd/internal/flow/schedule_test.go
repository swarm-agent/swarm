package flow

import (
	"context"
	"testing"
	"time"
)

func TestNextFireDailyWeeklyMonthlyAndOnDemand(t *testing.T) {
	daily := testScheduleAssignment(ScheduleSpec{Cadence: "Daily", Time: "09:30", Timezone: "UTC"})
	next, ok, err := NextFire(daily, time.Date(2025, 1, 2, 9, 0, 0, 0, time.UTC))
	if err != nil || !ok {
		t.Fatalf("daily next ok=%v err=%v", ok, err)
	}
	if want := time.Date(2025, 1, 2, 9, 30, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("daily next = %s want %s", next, want)
	}
	next, ok, err = NextFire(daily, time.Date(2025, 1, 2, 9, 30, 0, 0, time.UTC))
	if err != nil || !ok {
		t.Fatalf("daily next after exact ok=%v err=%v", ok, err)
	}
	if want := time.Date(2025, 1, 3, 9, 30, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("daily exact next = %s want %s", next, want)
	}

	weekly := testScheduleAssignment(ScheduleSpec{Cadence: "Weekly", Weekday: "Tuesday", Time: "10:00", Timezone: "UTC"})
	next, ok, err = NextFire(weekly, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || !ok {
		t.Fatalf("weekly next ok=%v err=%v", ok, err)
	}
	if want := time.Date(2025, 1, 7, 10, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("weekly next = %s want %s", next, want)
	}

	monthly := testScheduleAssignment(ScheduleSpec{Cadence: "Monthly", MonthDay: 31, Time: "08:00", Timezone: "UTC"})
	next, ok, err = NextFire(monthly, time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || !ok {
		t.Fatalf("monthly next ok=%v err=%v", ok, err)
	}
	if want := time.Date(2025, 2, 28, 8, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("monthly clamped next = %s want %s", next, want)
	}

	onDemand := testScheduleAssignment(ScheduleSpec{Cadence: "On demand", Timezone: "UTC"})
	if next, ok, err = NextFire(onDemand, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil || ok || !next.IsZero() {
		t.Fatalf("on-demand next=%s ok=%v err=%v", next, ok, err)
	}

	disabled := daily
	disabled.Enabled = false
	if next, ok, err = NextFire(disabled, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil || ok || !next.IsZero() {
		t.Fatalf("disabled next=%s ok=%v err=%v", next, ok, err)
	}
}

func TestNextFireHandlesMultipleTimesPerDay(t *testing.T) {
	assignment := testScheduleAssignment(ScheduleSpec{Cadence: "Daily", Times: []string{"09:00", "17:00"}, Timezone: "UTC"})
	next, ok, err := NextFire(assignment, time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC))
	if err != nil || !ok {
		t.Fatalf("multi-time next ok=%v err=%v", ok, err)
	}
	if want := time.Date(2025, 1, 2, 17, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("multi-time same-day next = %s want %s", next, want)
	}
	next, ok, err = NextFire(assignment, time.Date(2025, 1, 2, 18, 0, 0, 0, time.UTC))
	if err != nil || !ok {
		t.Fatalf("multi-time rollover ok=%v err=%v", ok, err)
	}
	if want := time.Date(2025, 1, 3, 9, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("multi-time rollover next = %s want %s", next, want)
	}
}

func TestValidateScheduleTimes(t *testing.T) {
	if err := ValidateSchedule(ScheduleSpec{Cadence: "daily", Timezone: "UTC", Times: []string{"09:00", "17:30"}}); err != nil {
		t.Fatalf("validate multi-times: %v", err)
	}
	normalized := NormalizeScheduleSpec(ScheduleSpec{Cadence: "Daily", Time: "17:30", Times: []string{"09:00", "17:30", "09:00"}, Timezone: " UTC "})
	if normalized.Cadence != CadenceDaily {
		t.Fatalf("normalized cadence = %q", normalized.Cadence)
	}
	if normalized.Timezone != "UTC" {
		t.Fatalf("normalized timezone = %q", normalized.Timezone)
	}
	if len(normalized.Times) != 2 || normalized.Times[0] != "09:00" || normalized.Times[1] != "17:30" {
		t.Fatalf("normalized times = %+v", normalized.Times)
	}
	if normalized.Time != "09:00" {
		t.Fatalf("normalized time = %q", normalized.Time)
	}
	if err := ValidateSchedule(ScheduleSpec{Cadence: "daily", Timezone: "UTC", Times: []string{"09:00", "25:00"}}); err == nil {
		t.Fatal("expected invalid time to fail validation")
	}
	maxTimes := make([]string, maxScheduleTimes)
	for i := range maxTimes {
		maxTimes[i] = "09:00"
	}
	if err := ValidateSchedule(ScheduleSpec{Cadence: "daily", Timezone: "UTC", Times: maxTimes}); err != nil {
		t.Fatalf("expected modal max times to validate: %v", err)
	}
	tooMany := make([]string, maxScheduleTimes+1)
	for i := range tooMany {
		tooMany[i] = "09:00"
	}
	if err := ValidateSchedule(ScheduleSpec{Cadence: "daily", Timezone: "UTC", Times: tooMany}); err == nil {
		t.Fatal("expected too many times to fail validation")
	}
}

func TestNextFireHandlesMultiDayWeeklySchedule(t *testing.T) {
	assignment := testScheduleAssignment(ScheduleSpec{Cadence: "Weekly", Weekday: "Mon,Wed,Fri", Time: "10:00", Timezone: "UTC"})
	next, ok, err := NextFire(assignment, time.Date(2025, 1, 7, 12, 0, 0, 0, time.UTC))
	if err != nil || !ok {
		t.Fatalf("multi-day weekly next ok=%v err=%v", ok, err)
	}
	if want := time.Date(2025, 1, 8, 10, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("multi-day weekly next = %s want %s", next, want)
	}
}

func TestNextFireUsesCronAsSourceOfTruth(t *testing.T) {
	assignment := testScheduleAssignment(ScheduleSpec{Cadence: "Daily", Time: "09:00", Times: []string{"09:00"}, Timezone: "UTC", Cron: "*/20 9-10 * * Mon-Fri"})
	next, ok, err := NextFire(assignment, time.Date(2025, 1, 6, 9, 10, 0, 0, time.UTC))
	if err != nil || !ok {
		t.Fatalf("cron next ok=%v err=%v", ok, err)
	}
	if want := time.Date(2025, 1, 6, 9, 20, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("cron next = %s want %s", next, want)
	}
	assignment.Schedule.Cadence = "On demand"
	next, ok, err = NextFire(assignment, time.Date(2025, 1, 6, 10, 50, 0, 0, time.UTC))
	if err != nil || !ok {
		t.Fatalf("cron on-demand next ok=%v err=%v", ok, err)
	}
	if want := time.Date(2025, 1, 7, 9, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("cron on-demand next = %s want %s", next, want)
	}
}

func TestValidateScheduleCronIsAuthoritative(t *testing.T) {
	if err := ValidateSchedule(ScheduleSpec{Cadence: "Weekly", Timezone: "UTC", Cron: "0 9,13,17 * * Mon-Fri"}); err != nil {
		t.Fatalf("cron without guided weekly weekday/time should validate: %v", err)
	}
	if err := ValidateSchedule(ScheduleSpec{Cadence: "Daily", Timezone: "UTC", Cron: "61 9 * * Mon"}); err == nil {
		t.Fatal("expected invalid cron minute to fail validation")
	}
}

func TestNextFireHandlesDSTGapAndFold(t *testing.T) {
	gap := testScheduleAssignment(ScheduleSpec{Cadence: "Daily", Time: "02:30", Timezone: "America/New_York"})
	next, ok, err := NextFire(gap, time.Date(2024, 3, 10, 6, 0, 0, 0, time.UTC))
	if err != nil || !ok {
		t.Fatalf("gap next ok=%v err=%v", ok, err)
	}
	local := next.In(mustLocation(t, "America/New_York"))
	if local.Year() != 2024 || local.Month() != time.March || local.Day() != 10 || local.Hour() != 3 || local.Minute() != 0 {
		t.Fatalf("gap local next = %s, want first valid wall time after 02:30", local)
	}

	fold := testScheduleAssignment(ScheduleSpec{Cadence: "Daily", Time: "01:30", Timezone: "America/New_York"})
	next, ok, err = NextFire(fold, time.Date(2024, 11, 3, 4, 0, 0, 0, time.UTC))
	if err != nil || !ok {
		t.Fatalf("fold next ok=%v err=%v", ok, err)
	}
	if want := time.Date(2024, 11, 3, 5, 30, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("fold first next = %s want %s", next, want)
	}
}

func TestCatchUpPolicies(t *testing.T) {
	assignment := testScheduleAssignment(ScheduleSpec{Cadence: "Daily", Time: "09:00", Timezone: "UTC"})
	assignment.CatchUpPolicy = CatchUpPolicy{Mode: CatchUpOnce}
	fires, err := CatchUpFireTimes(assignment, time.Date(2025, 1, 1, 9, 0, 0, 0, time.UTC), time.Date(2025, 1, 4, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("catch-up once: %v", err)
	}
	if len(fires) != 1 || !fires[0].Equal(time.Date(2025, 1, 4, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("catch-up once fires = %v", fires)
	}

	assignment.CatchUpPolicy = CatchUpPolicy{Mode: CatchUpAll, MaxCatchUp: 2}
	fires, err = CatchUpFireTimes(assignment, time.Date(2025, 1, 1, 9, 0, 0, 0, time.UTC), time.Date(2025, 1, 4, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("catch-up all: %v", err)
	}
	if len(fires) != 2 || !fires[0].Equal(time.Date(2025, 1, 2, 9, 0, 0, 0, time.UTC)) || !fires[1].Equal(time.Date(2025, 1, 3, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("catch-up all fires = %v", fires)
	}

	assignment.CatchUpPolicy = CatchUpPolicy{Mode: CatchUpSkip}
	fires, err = CatchUpFireTimes(assignment, time.Date(2025, 1, 1, 9, 0, 0, 0, time.UTC), time.Date(2025, 1, 4, 12, 0, 0, 0, time.UTC))
	if err != nil || len(fires) != 0 {
		t.Fatalf("catch-up skip fires=%v err=%v", fires, err)
	}
}

func TestSchedulerClaimsDueRunsOnceAndSchedulesNext(t *testing.T) {
	assignment := AcceptedAssignment{Assignment: testScheduleAssignment(ScheduleSpec{Cadence: "Daily", Time: "09:00", Timezone: "UTC"})}
	assignment.FlowID = "flow-1"
	assignment.Revision = 2
	dueAt := time.Date(2025, 1, 2, 9, 0, 0, 0, time.UTC)
	store := &fakeSchedulerStore{due: []DueRun{{Assignment: assignment, ScheduledAt: dueAt}}}
	runner := &fakeFlowRunner{}
	scheduler := Scheduler{
		Store:    store,
		Runner:   runner,
		Now:      func() time.Time { return time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC) },
		NewRunID: func(DueRun) string { return "run-1" },
	}

	starts, err := scheduler.Tick(context.Background(), 10)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(starts) != 1 || starts[0].RunID != "run-1" {
		t.Fatalf("starts = %+v", starts)
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d", runner.calls)
	}
	if len(store.deleted) != 1 || store.deleted[0].FlowID != "flow-1" {
		t.Fatalf("deleted due = %+v", store.deleted)
	}
	if len(store.scheduledNext) != 1 || !store.scheduledNext[0].After(dueAt) {
		t.Fatalf("scheduled next = %+v", store.scheduledNext)
	}

	store.due = []DueRun{{Assignment: assignment, ScheduledAt: dueAt}}
	starts, err = scheduler.Tick(context.Background(), 10)
	if err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if len(starts) != 0 || runner.calls != 1 {
		t.Fatalf("duplicate starts=%+v runner calls=%d", starts, runner.calls)
	}
}

func testScheduleAssignment(schedule ScheduleSpec) Assignment {
	return Assignment{
		FlowID:   "flow-schedule",
		Revision: 1,
		Name:     "Schedule test",
		Enabled:  true,
		Agent:    AgentSelection{ProfileName: "memory", ProfileMode: "background"},
		Schedule: schedule,
		Intent:   PromptIntent{Prompt: "Run schedule test"},
	}
}

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load location %s: %v", name, err)
	}
	return loc
}

type fakeSchedulerStore struct {
	due           []DueRun
	claims        map[RunClaimKey]RunClaim
	deleted       []RunClaimKey
	scheduledNext []time.Time
}

func (s *fakeSchedulerStore) ListDue(context.Context, time.Time, int) ([]DueRun, error) {
	return append([]DueRun(nil), s.due...), nil
}

func (s *fakeSchedulerStore) ClaimRun(_ context.Context, claim RunClaim) (RunClaim, bool, error) {
	if s.claims == nil {
		s.claims = make(map[RunClaimKey]RunClaim)
	}
	key := RunClaimKey{FlowID: claim.FlowID, Revision: claim.Revision, ScheduledAt: claim.ScheduledAt.UTC()}
	if existing, ok := s.claims[key]; ok {
		return existing, false, nil
	}
	s.claims[key] = claim
	return claim, true, nil
}

func (s *fakeSchedulerStore) DeleteDue(_ context.Context, flowID string, revision int64, scheduledAt time.Time) error {
	s.deleted = append(s.deleted, RunClaimKey{FlowID: flowID, Revision: revision, ScheduledAt: scheduledAt.UTC()})
	return nil
}

func (s *fakeSchedulerStore) ScheduleNext(_ context.Context, assignment AcceptedAssignment, after time.Time) (time.Time, bool, error) {
	next, ok, err := NextFireAfter(assignment.Assignment, after)
	if err == nil && ok {
		s.scheduledNext = append(s.scheduledNext, next)
	}
	return next, ok, err
}

type fakeFlowRunner struct {
	calls int
}

func (r *fakeFlowRunner) RunAcceptedFlow(_ context.Context, assignment AcceptedAssignment, request RunRequest) (RunStart, error) {
	r.calls++
	return RunStart{
		FlowID:      assignment.FlowID,
		Revision:    assignment.Revision,
		ScheduledAt: request.ScheduledAt,
		SessionID:   "session-1",
		RunID:       "run-1",
		Status:      "running",
	}, nil
}
