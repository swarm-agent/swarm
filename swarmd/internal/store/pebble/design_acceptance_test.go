package pebblestore

import (
	"errors"
	"testing"
)

// Purpose: ApplyV3SessionMutation must atomically retain a design acceptance,
// its recovery index and session event. Rejection must leave all three unchanged;
// parent completion must not remove pending work. This real-store layer is the
// narrowest boundary that proves the commit, ownership and idempotency contract.
func TestDesignAcceptanceAtomicRecovery(t *testing.T) {
	s := openTaskProgramTestStore(t)
	ss := NewSessionStore(s)
	p := designTestOwner
	_, err := ss.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "parent", UserID: p.PrincipalID, AccountScopeID: p.AccountID, IdempotencyKey: "create", PayloadHash: "create", Kind: V3SessionMutationCreateSession, Session: &SessionSnapshot{ID: "parent"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ss.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "parent", UserID: p.PrincipalID, AccountScopeID: p.AccountID, IdempotencyKey: "run", PayloadHash: "run", Kind: V3SessionMutationRecordRunIntent, RunIntent: &V3SessionRunIntent{SessionID: "parent", RunID: "parent-run", SourceMessageID: "edit-message", Status: V3RunIntentPendingExecutor}})
	if err != nil {
		t.Fatal(err)
	}
	submit := designTestSubmit("accept", DesignHTML)
	input := V3SessionMutationInput{SessionID: "parent", UserID: p.PrincipalID, AccountScopeID: p.AccountID, IdempotencyKey: submit.IdempotencyKey, PayloadHash: DesignAcceptanceHash(submit), Kind: V3SessionMutationAcceptDesign, EventType: "design.accepted", DesignAcceptance: &DesignAcceptance{Submit: submit}}
	if err := ss.PutProject(p.AccountID, &ProjectRecord{ID: "project", Name: "Project", PrimarySessionID: "parent"}); err != nil { t.Fatal(err) }
	var notices []V3RealtimeOutboxRecord
	s.SetProjectPublisher(func(record V3RealtimeOutboxRecord) { notices = append(notices, record) })
	first, err := ss.ApplyV3SessionMutation(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 { t.Fatal("missing atomic project invalidation", notices) }
	if first.RealtimeOutbox == nil {
		t.Fatal("missing canonical outbox")
	}
	catalog, cursor, err := s.ListSessionDesignRequests(p, "parent", "", 20)
	if err != nil || len(catalog) != 1 || catalog[0].ID != submit.RequestID || catalog[0].SourceMessageID != "edit-message" || catalog[0].ClientRequestID != submit.IdempotencyKey || cursor != "" {
		t.Fatal("acceptance missing catalog entry", catalog, cursor, err)
	}
	replay, err := ss.ApplyV3SessionMutation(input)
	if err != nil || !replay.Replayed || replay.PrimarySeq != first.PrimarySeq || len(notices) != 1 {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	_, err = ss.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "parent", UserID: p.PrincipalID, AccountScopeID: p.AccountID, IdempotencyKey: "complete", PayloadHash: "complete", Kind: V3SessionMutationRecordRunIntent, RunIntent: &V3SessionRunIntent{SessionID: "parent", RunID: "parent-run", Status: V3RunIntentCompleted}})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := s.ListPendingDesignRequests(p, "", 1)
	if err != nil || len(pending) != 1 || pending[0].State != DesignQueued {
		t.Fatalf("lost accepted work: %+v %v", pending, err)
	}
	before, err := ss.ListV3SessionEvents("parent", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	bad := designTestSubmit("bad", DesignHTML)
	bad.ParentRunID = "missing"
	bad.Candidates[0].ArtifactID = "bad-artifact"
	input.DesignAcceptance = &DesignAcceptance{Submit: bad}
	input.IdempotencyKey = bad.IdempotencyKey
	input.PayloadHash = DesignAcceptanceHash(bad)
	if _, err = ss.ApplyV3SessionMutation(input); err == nil {
		t.Fatal("accepted absent run")
	}
	if _, err = s.GetDesignRequest(p, "bad"); !errors.Is(err, ErrDesignNotFound) {
		t.Fatal("partial request", err)
	}
	if _, err = s.GetDesignArtifact(p, "bad-artifact"); !errors.Is(err, ErrDesignNotFound) {
		t.Fatal("partial artifact", err)
	}
	after, err := ss.ListV3SessionEvents("parent", 0, 50)
	if err != nil || len(after) != len(before) || len(notices) != 1 {
		t.Fatal("rejection emitted event", err)
	}
	input.AccountScopeID = "foreign"
	if _, err = ss.ApplyV3SessionMutation(input); err == nil {
		t.Fatal("accepted foreign parent")
	}
	foreign, err := s.ListPendingDesignRequests(DesignPrincipal{AccountID: "foreign", PrincipalID: p.PrincipalID}, "", 1)
	if err != nil || len(foreign) != 0 {
		t.Fatal("foreign index mutated", err)
	}
}
