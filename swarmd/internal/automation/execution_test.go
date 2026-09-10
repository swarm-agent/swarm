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
