package automation

import (
	"context"
	"errors"
	"testing"

	store "swarm/packages/swarmd/internal/store/pebble"
)

type executionRuntimeFake struct { calls int; err error }
func (f *executionRuntimeFake) Ensure(context.Context, Principal, store.AutomationRecord, store.AutomationRecord) (string, error) {
	f.calls++
	return "execution-session", f.err
}
type triggerAuthorityFake struct { err error }
func (f triggerAuthorityFake) Verify(context.Context, Principal, store.AutomationScope, Trigger) error { return f.err }

// Purpose: authenticated admission and dispatch reauthorization must reject
// forged sources and revoked access without creating an occurrence or session.
// ExecutionService is the narrowest layer owning these dependency calls; this
// does not prove the eventual V3/worktree adapter's idempotency or isolation.
func TestExecutionAdmissionAndRevocation(t *testing.T) {
	s, repo, access, _, p, scope, d := fixture(t)
	d.Enabled = true
	d.Authorization = store.AutomationAuthorizationPolicy{Mode: "approved_policy", ApprovalReference: "approval", ExpiresAt: 200000}
	if _, _, err := s.SaveDefinition(context.Background(), p, scope, "automation", "save", 0, d); err != nil { t.Fatal(err) }
	runtime := &executionRuntimeFake{}
	e, err := NewExecutionService(s, runtime, triggerAuthorityFake{err: ErrDenied})
	if err != nil { t.Fatal(err) }
	trigger := Trigger{Kind: "manual", Identity: "click", ScheduledAt: 100000}
	writes := repo.writes
	if _, err := e.Admit(context.Background(), p, scope, "automation", 1, trigger); !errors.Is(err, ErrDenied) { t.Fatalf("admission: %v", err) }
	if repo.writes != writes || runtime.calls != 0 { t.Fatal("denied trigger caused side effects") }
	e.triggers = triggerAuthorityFake{}
	r, err := e.Admit(context.Background(), p, scope, "automation", 1, trigger)
	if err != nil { t.Fatal(err) }
	access.denied = true
	writes = repo.writes
	if _, err := e.Dispatch(context.Background(), p, scope, "automation", r.ID); !errors.Is(err, ErrDenied) { t.Fatalf("revoked dispatch: %v", err) }
	if runtime.calls != 0 || repo.writes != writes { t.Fatal("revoked dispatch caused side effects") }
}

// Purpose: an ambiguous adapter failure must preserve pending durable work for
// recovery, never publish running success or generate a different occurrence.
// The fake runtime isolates the domain's partial-failure behavior; adapter crash
// safety and real store concurrency require separate integration tests.
func TestExecutionPartialFailurePreservesPending(t *testing.T) {
	s, repo, _, _, p, scope, d := fixture(t)
	d.Enabled = true
	d.Authorization = store.AutomationAuthorizationPolicy{Mode: "approved_policy", ApprovalReference: "approval", ExpiresAt: 200000}
	if _, _, err := s.SaveDefinition(context.Background(), p, scope, "automation", "save", 0, d); err != nil { t.Fatal(err) }
	runtime := &executionRuntimeFake{err: errors.New("interrupted")}
	e, err := NewExecutionService(s, runtime, triggerAuthorityFake{})
	if err != nil { t.Fatal(err) }
	r, err := e.Admit(context.Background(), p, scope, "automation", 1, Trigger{Kind: "manual", Identity: "click", ScheduledAt: 100000})
	if err != nil { t.Fatal(err) }
	writes := repo.writes
	if _, err := e.Dispatch(context.Background(), p, scope, "automation", r.ID); err == nil { t.Fatal("failure reported success") }
	if repo.writes != writes { t.Fatal("partial failure changed durable state") }
	persisted := repo.rows["occurrence"+r.ID]
	if persisted.Occurrence.State != "pending" || persisted.Occurrence.SessionID != "" { t.Fatal("pending recovery record lost") }
	// A new service models restart without relying on process-local state.
	runtime.err = nil
	restarted, err := NewExecutionService(s, runtime, triggerAuthorityFake{})
	if err != nil { t.Fatal(err) }
	out, err := restarted.Dispatch(context.Background(), p, scope, "automation", r.ID)
	if err != nil { t.Fatal(err) }
	if out.ID != r.ID || out.Occurrence.State != "running" || out.Occurrence.SessionID != "execution-session" { t.Fatalf("recovery: %+v", out) }
}

type cancellationRuntimeFake struct {
	executionRuntimeFake
	stop func(store.AutomationRecord) error
	stops int
}
func (f *cancellationRuntimeFake) Cancel(_ context.Context, _ Principal, r store.AutomationRecord) error {
	f.stops++
	return f.stop(r)
}

// Purpose: ExecutionService.Cancel must CAS the exact occurrence before stop,
// exclude competing cancellation/dispatch/writers, retain reservations on stop
// failure, and recover the same request after Pebble reopen. Real persistence plus
// a fake stop is the narrowest layer proving both durable and external effects.
func TestExecutionCancellationFenceRecovery(t *testing.T) {
	ctx := context.Background()
	s, _, _, _, p, scope, d := fixture(t)
	path := t.TempDir()
	db, err := store.Open(path)
	if err != nil { t.Fatal(err) }
	defer func() { db.Close() }()
	s.repo = db
	d.Enabled = true
	d.Schedule.OverlapPolicy = "serialize"
	d.Authorization = store.AutomationAuthorizationPolicy{Mode: "approved_policy", ApprovalReference: "approval", ExpiresAt: 200000, TargetIDs: []string{"target"}}
	if _, _, err := s.SaveDefinition(ctx, p, scope, "automation", "save", 0, d); err != nil { t.Fatal(err) }
	runtime := &cancellationRuntimeFake{}
	e, err := NewExecutionService(s, runtime, triggerAuthorityFake{})
	if err != nil { t.Fatal(err) }
	r, err := e.Admit(ctx, p, scope, "automation", 1, Trigger{Kind: "manual", Identity: "first", ScheduledAt: 100000})
	if err != nil { t.Fatal(err) }
	next, err := e.Admit(ctx, p, scope, "automation", 1, Trigger{Kind: "manual", Identity: "second", ScheduledAt: 100000})
	if err != nil { t.Fatal(err) }
	if err := db.ClaimAutomationDispatch(scope, "automation", r.ID); err != nil { t.Fatal(err) }
	// An exact but nonexistent/stale revision cannot reach the runtime.
	if _, err := e.Cancel(ctx, p, scope, "automation", r.ID, r.Revision+1, "stale"); !errors.Is(err, store.ErrAutomationConflict) { t.Fatalf("stale: %v", err) }
	if runtime.stops != 0 { t.Fatal("stale stop effect") }
	head, _, err := db.GetAutomationRecord(scope, "automation", "occurrence", r.ID, 0)
	if err != nil || head.Revision != r.Revision { t.Fatal("stale request mutated head") }
	interrupted := errors.New("stop interrupted")
	runtime.stop = func(admitted store.AutomationRecord) error {
		if admitted.Occurrence.State != "cancelling" { t.Fatal("stop before durable admission") }
		// Reentrant competing request models overlap while stop is in flight;
		// it also detects holding the store mutex across the runtime callback.
		if _, err := e.Cancel(ctx, p, scope, "automation", r.ID, r.Revision, "competitor"); !errors.Is(err, store.ErrAutomationConflict) { t.Fatalf("competitor: %v", err) }
		if runtime.stops != 1 { t.Fatal("competing stop effect") }
		if err := db.ClaimAutomationDispatch(scope, "automation", r.ID); !errors.Is(err, store.ErrAutomationConflict) { t.Fatalf("dispatch fence: %v", err) }
		if err := db.ClaimAutomationDispatch(scope, "automation", next.ID); !errors.Is(err, store.ErrAutomationConflict) { t.Fatalf("reservation released: %v", err) }
		if _, err := e.transition(p, admitted, "completed", ""); !errors.Is(err, store.ErrAutomationConflict) { t.Fatalf("writer bypass: %v", err) }
		return interrupted
	}
	out, err := e.Cancel(ctx, p, scope, "automation", r.ID, r.Revision, "cancel")
	if !errors.Is(err, interrupted) || out.Occurrence.State != "cancelling" { t.Fatalf("failure: %+v %v", out, err) }
	if err := db.Close(); err != nil { t.Fatal(err) }
	db, err = store.Open(path)
	if err != nil { t.Fatal(err) }
	s.repo = db
	e, err = NewExecutionService(s, runtime, triggerAuthorityFake{})
	if err != nil { t.Fatal(err) }
	runtime.stop = func(store.AutomationRecord) error { return nil }
	out, err = e.Cancel(ctx, p, scope, "automation", r.ID, r.Revision, "cancel")
	if err != nil || out.Occurrence.State != "cancelled" || out.Revision != r.Revision+2 { t.Fatalf("restart: %+v %v", out, err) }
	if runtime.stops != 2 { t.Fatal("retry did not resume stop") }
	if _, err := e.Cancel(ctx, p, scope, "automation", r.ID, r.Revision, "cancel"); err != nil { t.Fatal(err) }
	if runtime.stops != 2 { t.Fatal("terminal replay stopped again") }
	if err := db.ClaimAutomationDispatch(scope, "automation", next.ID); err != nil { t.Fatalf("confirmed stop retained reservation: %v", err) }
}
