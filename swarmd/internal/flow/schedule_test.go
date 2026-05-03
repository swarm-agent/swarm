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
