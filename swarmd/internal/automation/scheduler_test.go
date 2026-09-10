package automation

import (
	"context"
	"errors"
	"testing"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: Tick must anchor interval receipts to the persisted definition, skip
// old missed ticks when requested, coalesce only one, and never admit future work
// on clock rollback. Fake clock plus real cursor storage is the narrowest proof.
func TestSchedulerClockPolicies(t *testing.T) {
	for _, policy := range []string{"skip", "coalesce"} {
		t.Run(policy, func(t *testing.T) {
			s, _, _, _, p, scope, d := fixture(t)
			db, err := store.Open(t.TempDir())
			if err != nil { t.Fatal(err) }
			defer db.Close()
			s.repo = db
			d.Enabled = true
			d.Schedule = store.AutomationSchedulePolicy{Kind: "interval", IntervalSeconds: 120, MissedPolicy: policy, OverlapPolicy: "independent"}
			d.Authorization = store.AutomationAuthorizationPolicy{Mode: "approved_policy", ApprovalReference: "approval", ExpiresAt: 10000000}
			s.now = func() time.Time { return time.UnixMilli(100000) }
			if _, _, err := s.SaveDefinition(context.Background(), p, scope, "automation", "save", 0, d); err != nil { t.Fatal(err) }
			e, err := NewExecutionService(s, &executionRuntimeFake{}, triggerAuthorityFake{})
			if err != nil { t.Fatal(err) }
			p.Role = "system"
			s.now = func() time.Time { return time.UnixMilli(99000) }
			if err := e.Tick(context.Background(), p, scope, "automation", 1); err != nil { t.Fatal(err) }
			cursor, err := db.GetAutomationCursor(scope, "automation", 1)
			if err != nil || cursor != 0 { t.Fatal("rollback advanced cursor", cursor, err) }
			s.now = func() time.Time { return time.UnixMilli(290000) }
			if err := e.Tick(context.Background(), p, scope, "automation", 1); err != nil { t.Fatal(err) }
			if err := e.Tick(context.Background(), p, scope, "automation", 1); err != nil { t.Fatal(err) }
			rows, _, err := db.SearchAutomationRecords(store.AutomationSearch{Scope: scope, AutomationID: "automation", Kind: "occurrence", Limit: 50})
			if err != nil { t.Fatal(err) }
			want := 0
			if policy == "coalesce" { want = 1 }
			if len(rows) != want { t.Fatalf("occurrences=%d want=%d", len(rows), want) }
			if want == 1 && rows[0].Occurrence.ScheduledAt != 220000 { t.Fatal("interval anchor drift", rows[0]) }
		})
	}
}

// Purpose: RecoverPage must finish a persisted cancelling fence despite disabled
// scheduling and retain cancelling on runtime failure. Real store plus fake stop
// proves terminal state is not published before the external fence succeeds.
func TestSchedulerCancellationRecovery(t *testing.T) {
	s, _, _, _, p, scope, d := fixture(t)
	db, err := store.Open(t.TempDir())
	if err != nil { t.Fatal(err) }
	defer db.Close()
	s.repo = db
	d.Enabled = true
	d.Authorization = store.AutomationAuthorizationPolicy{Mode: "approved_policy", ApprovalReference: "approval", ExpiresAt: 200000}
	ctx := context.Background()
	if _, _, err := s.SaveDefinition(ctx, p, scope, "automation", "save", 0, d); err != nil { t.Fatal(err) }
	stopErr := errors.New("stop interrupted")
	runtime := &cancellationRuntimeFake{stop: func(store.AutomationRecord) error { return stopErr }}
	e, err := NewExecutionService(s, runtime, triggerAuthorityFake{})
	if err != nil { t.Fatal(err) }
	r, err := e.Admit(ctx, p, scope, "automation", 1, Trigger{Kind: "manual", Identity: "click", ScheduledAt: 100000})
	if err != nil { t.Fatal(err) }
	if _, err := e.Cancel(ctx, p, scope, "automation", r.ID, r.Revision, "cancel"); !errors.Is(err, stopErr) { t.Fatal(err) }
	d.Enabled = false
	if _, _, err := s.SaveDefinition(ctx, p, scope, "automation", "disable", 1, d); err != nil { t.Fatal(err) }
	if _, err := e.RecoverPage(ctx, p, scope, "automation", ""); !errors.Is(err, stopErr) { t.Fatal(err) }
	head, _, err := db.GetAutomationRecord(scope, "automation", "occurrence", r.ID, 0)
	if err != nil || head.Occurrence.State != "cancelling" { t.Fatal("false cancellation success", head, err) }
	stopErr = nil
	if _, err := e.RecoverPage(ctx, p, scope, "automation", ""); err != nil { t.Fatal(err) }
	head, _, err = db.GetAutomationRecord(scope, "automation", "occurrence", r.ID, 0)
	if err != nil || head.Occurrence.State != "cancelled" || runtime.stops != 3 { t.Fatal(head, err, runtime.stops) }
}
