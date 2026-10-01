package pebblestore

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

// Purpose: the canonical create transaction must bind exactly one owned Designer
// child and run to the request, even on racing/replayed allocation and reopen.
// Store-level assertions prove no orphan session or partial attempt on stale or
// foreign input; this is narrower than invoking an executor/provider. Parent
// scoped invalidation must commit with allocation and survive replay/reopen.
func TestDesignAllocationAtomicRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	ss := NewSessionStore(s)
	p := designTestOwner
	for _, in := range []V3SessionMutationInput{
		{SessionID: "parent", UserID: p.PrincipalID, AccountScopeID: p.AccountID, Kind: V3SessionMutationCreateSession, IdempotencyKey: "parent", PayloadHash: "parent", Session: &SessionSnapshot{ID: "parent"}},
		{SessionID: "parent", UserID: p.PrincipalID, AccountScopeID: p.AccountID, Kind: V3SessionMutationRecordRunIntent, IdempotencyKey: "run", PayloadHash: "run", RunIntent: &V3SessionRunIntent{SessionID: "parent", RunID: "parent-run", Status: V3RunIntentPendingExecutor}},
	} {
		if _, err := ss.ApplyV3SessionMutation(in); err != nil {
			t.Fatal(err)
		}
	}
	submit := designTestSubmit("allocation", DesignHTML)
	if _, err := ss.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "parent", UserID: p.PrincipalID, AccountScopeID: p.AccountID, Kind: V3SessionMutationAcceptDesign, IdempotencyKey: submit.IdempotencyKey, PayloadHash: DesignAcceptanceHash(submit), DesignAcceptance: &DesignAcceptance{Submit: submit}}); err != nil {
		t.Fatal(err)
	}
	// Parent completion is deliberately before allocation, not just after it.
	if _, err := ss.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "parent", UserID: p.PrincipalID, AccountScopeID: p.AccountID, Kind: V3SessionMutationRecordRunIntent, IdempotencyKey: "complete", PayloadHash: "complete", RunIntent: &V3SessionRunIntent{SessionID: "parent", RunID: "parent-run", Status: V3RunIntentCompleted}}); err != nil {
		t.Fatal(err)
	}
	a := DesignAllocation{RequestID: submit.RequestID, ExpectedRevision: 1, Candidate: 0, Attempt: 1, Preference: ModelPreference{Provider: "test", Model: "configured"}}
	stale := a
	stale.ExpectedRevision = 9
	input := NewDesignAllocationMutation(p, stale)
	if _, err := ss.ApplyV3SessionMutation(input); !errors.Is(err, ErrDesignConflict) {
		t.Fatalf("stale: %v", err)
	}
	if _, ok, err := ss.GetSession(input.SessionID); err != nil || ok {
		t.Fatalf("orphan: %v %v", ok, err)
	}
	foreign := DesignPrincipal{AccountID: "foreign", PrincipalID: p.PrincipalID}
	fi := NewDesignAllocationMutation(foreign, a)
	if _, err := ss.ApplyV3SessionMutation(fi); !errors.Is(err, ErrDesignNotFound) {
		t.Fatalf("foreign: %v", err)
	}
	if _, ok, err := ss.GetSession(fi.SessionID); err != nil || ok {
		t.Fatal("foreign child written")
	}
	input = NewDesignAllocationMutation(p, a)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := ss.ApplyV3SessionMutation(input); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ss = NewSessionStore(s)
	replay, err := ss.ApplyV3SessionMutation(input)
	if err != nil || !replay.Replayed {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	parentEvents, err := ss.ListV3SessionEvents("parent", 0, 20)
	if err != nil || len(parentEvents) != 5 || parentEvents[4].EventType != "design.updated" { t.Fatal("missing or duplicate parent allocation event", parentEvents, err) }
	parentOutbox, err := ss.ListV3RealtimeOutboxForSessionAfterEndpoint("parent", 0, 20)
	if err != nil || len(parentOutbox) != 5 || parentOutbox[4].Event.ID != parentEvents[4].ID { t.Fatal("missing parent allocation outbox", err) }
	r, err := s.GetDesignRequest(p, submit.RequestID)
	if err != nil || r.Revision != 2 || len(r.Candidates[0].Attempts) != 1 {
		t.Fatalf("binding: %+v %v", r, err)
	}
	child, err := ss.VerifyDesignChild(p, r, 0)
	if err != nil {
		t.Fatal(err)
	}
	intent, ok, err := ss.GetV3SessionRunIntent(child.ChildSessionID, child.RunID)
	if err != nil || !ok || intent.AccountScopeID != p.AccountID || intent.UserID != p.PrincipalID || intent.Status != V3RunIntentPendingExecutor {
		t.Fatalf("intent: %+v %v", intent, err)
	}
	pending, err := s.ListPendingDesignRequests(p, "", 1)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending: %+v %v", pending, err)
	}
}
