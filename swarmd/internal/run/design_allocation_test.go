package run

import (
	"context"
	"errors"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/executioncapacity"
	"swarm/packages/swarmd/internal/model"
	store "swarm/packages/swarmd/internal/store/pebble"
)

func designAllocationFixture(t *testing.T) (*Service, store.DesignPrincipal, store.DesignRequest) {
	t.Helper()
	svc, parent, _, _ := capacityBoundaryFixture(t)
	p := store.DesignPrincipal{AccountID: parent.AccountScopeID, PrincipalID: parent.UserID}
	_, err := svc.sessions.ApplySessionMutation(store.V3SessionMutationInput{SessionID: parent.ID, UserID: p.PrincipalID, AccountScopeID: p.AccountID, Kind: store.V3SessionMutationRecordRunIntent, IdempotencyKey: "design-parent", PayloadHash: "design-parent", RunIntent: &store.V3SessionRunIntent{SessionID: parent.ID, RunID: "design-parent", Status: store.V3RunIntentPendingExecutor}})
	if err != nil {
		t.Fatal(err)
	}
	submit := store.DesignSubmit{RequestID: "request", IdempotencyKey: "request", ParentSessionID: parent.ID, ParentRunID: "design-parent", Candidates: []store.DesignCandidateSpec{{ArtifactID: "artifact", Kind: store.DesignHTML, Operation: store.DesignGenerate, Brief: "Design a card"}}}
	_, err = svc.sessions.ApplySessionMutation(store.V3SessionMutationInput{SessionID: parent.ID, UserID: p.PrincipalID, AccountScopeID: p.AccountID, Kind: store.V3SessionMutationAcceptDesign, IdempotencyKey: submit.IdempotencyKey, PayloadHash: store.DesignAcceptanceHash(submit), DesignAcceptance: &store.DesignAcceptance{Submit: submit}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := svc.sessions.DesignStore().GetDesignRequest(p, submit.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	return svc, p, r
}

// Purpose: AllocateDesignChild must use permission-owned configured capacity, not
// a private limit. A bounded admission timeout must leave no child or attempt.
// The service fixture uses real admission and Pebble, without invoking providers.
func TestDesignAllocationAdmissionRejection(t *testing.T) {
	svc, p, r := designAllocationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	lease, err := svc.permissions.AdmitExecution(ctx, executioncapacity.AcquireRequest{AccountScopeID: p.AccountID, SessionID: r.ParentSessionID, RunID: r.ParentRunID})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	bounded, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	_, got, err := svc.AllocateDesignChild(bounded, p, r.ID, r.Revision, 0, 1)
	if !errors.Is(err, context.DeadlineExceeded) || got != nil {
		t.Fatalf("admission: %v %v", got, err)
	}
	after, err := svc.sessions.DesignStore().GetDesignRequest(p, r.ID)
	if err != nil || after.Revision != r.Revision || len(after.Candidates[0].Attempts) != 0 {
		t.Fatalf("partial attempt: %+v %v", after, err)
	}
	if _, ok, err := svc.sessions.GetSession(store.DesignChildID(p, r.ID, 0, 1)); err != nil || ok {
		t.Fatal("orphan child", err)
	}
}

// Purpose: cancellation reconciliation must reject foreign authority, stop the
// actual canonical pending run, and only then confirm history. Repeated polling
// cannot fabricate success or allocate another child. No provider is necessary.
func TestDesignAllocationCancellation(t *testing.T) {
	svc, p, r := designAllocationFixture(t)
	input := store.NewDesignAllocationMutation(p, store.DesignAllocation{RequestID: r.ID, ExpectedRevision: r.Revision, Candidate: 0, Attempt: 1, Preference: store.ModelPreference{Provider: "test", Model: "configured"}})
	if _, err := svc.sessions.ApplySessionMutation(input); err != nil {
		t.Fatal(err)
	}
	r, err := svc.sessions.DesignStore().GetDesignRequest(p, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	a := r.Candidates[0].Attempts[0]
	r, err = svc.sessions.DesignStore().RecordDesignAttempt(p, r.ID, store.DesignAttemptMutation{IdempotencyKey: "cancel", ExpectedRevision: r.Revision, Candidate: 0, State: store.DesignCancelRequested, ChildSessionID: a.ChildSessionID, RunID: a.RunID})
	if err != nil {
		t.Fatal(err)
	}
	foreign := p
	foreign.AccountID = "foreign"
	if _, err := svc.ReconcileDesignCancellation(foreign, r.ID, 0); !errors.Is(err, store.ErrDesignNotFound) {
		t.Fatal(err)
	}
	r, err = svc.ReconcileDesignCancellation(p, r.ID, 0)
	if err != nil || r.State != store.DesignCancelled {
		t.Fatalf("cancel: %+v %v", r, err)
	}
	intent, ok, err := svc.sessions.Store().GetV3SessionRunIntent(a.ChildSessionID, a.RunID)
	if err != nil || !ok || intent.Status != store.V3RunIntentCancelled {
		t.Fatalf("canonical cancellation: %+v %v", intent, err)
	}
	if _, err := svc.ReconcileDesignCancellation(p, r.ID, 0); !errors.Is(err, store.ErrDesignConflict) {
		t.Fatal("duplicate confirmation", err)
	}
}

// Purpose: AllocateDesignChild must resolve only configured account defaults on
// missing Designer assignment and retain a visible RouterAlert. The real model
// catalog and store prove persisted preference/alert and idempotent recovery;
// no provider is invoked and no model name is a production fallback.
func TestDesignAllocationVisibleAccountFallback(t *testing.T) {
	svc, p, r := designAllocationFixture(t)
	db := svc.sessions.DesignStore()
	catalog := store.NewModelCatalogStore(db)
	if err := catalog.SetRecord(store.ModelCatalogRecord{Provider: "test", Model: "account-choice"}); err != nil {
		t.Fatal(err)
	}
	svc.model = model.NewService(store.NewModelStore(db), nil, model.NewCatalogService(catalog))
	if _, err := store.NewModelStore(db).SetPreferenceForAccount(p.AccountID, p.PrincipalID, "test", "account-choice", "", "", ""); err != nil {
		t.Fatal(err)
	}
	svc.agentModelSettings = nil
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a, lease, err := svc.AllocateDesignChild(ctx, p, r.ID, r.Revision, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if lease == nil {
		t.Fatal("missing admission lease")
	}
	defer lease.Release()
	child, ok, err := svc.sessions.GetSession(a.ChildSessionID)
	if err != nil || !ok || child.Preference.Model != "account-choice" || a.RouterAlert == "" || child.Metadata["router_alert"] != a.RouterAlert {
		t.Fatalf("fallback: %+v %+v %v", a, child, err)
	}
	recovered, extra, err := svc.AllocateDesignChild(ctx, p, r.ID, r.Revision, 0, 1)
	if err != nil || extra != nil || recovered.RouterAlert != a.RouterAlert || recovered.ChildSessionID != a.ChildSessionID {
		t.Fatalf("recovery: %+v %v", recovered, err)
	}
}

// Purpose: ReconcileDesignCancellation must not report cancellation when a
// canonical running child has no live executor to acknowledge StopSessionRun.
// Real V3 intent and attempt postconditions prove the unavailable-executor
// failure remains retryable rather than silently manufacturing terminal state.
func TestDesignAllocationCancellationRequiresExecutorAcknowledgement(t *testing.T) {
	svc, p, r := designAllocationFixture(t)
	input := store.NewDesignAllocationMutation(p, store.DesignAllocation{RequestID: r.ID, ExpectedRevision: r.Revision, Candidate: 0, Attempt: 1, Preference: store.ModelPreference{Provider: "test", Model: "configured"}})
	if _, err := svc.sessions.ApplySessionMutation(input); err != nil {
		t.Fatal(err)
	}
	r, err := svc.sessions.DesignStore().GetDesignRequest(p, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	a := r.Candidates[0].Attempts[0]
	if _, err := svc.sessions.ApplySessionMutation(store.V3SessionMutationInput{SessionID: a.ChildSessionID, UserID: p.PrincipalID, AccountScopeID: p.AccountID, Kind: store.V3SessionMutationRecordRunIntent, IdempotencyKey: "start", PayloadHash: "start", RunIntent: &store.V3SessionRunIntent{SessionID: a.ChildSessionID, RunID: a.RunID, Status: store.V3RunIntentRunning}}); err != nil {
		t.Fatal(err)
	}
	r, err = svc.sessions.DesignStore().RecordDesignAttempt(p, r.ID, store.DesignAttemptMutation{IdempotencyKey: "cancel", ExpectedRevision: r.Revision, Candidate: 0, State: store.DesignCancelRequested, ChildSessionID: a.ChildSessionID, RunID: a.RunID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReconcileDesignCancellation(p, r.ID, 0); err == nil {
		t.Fatal("missing executor falsely acknowledged")
	}
	after, err := svc.sessions.DesignStore().GetDesignRequest(p, r.ID)
	if err != nil || after.Revision != r.Revision || after.State != store.DesignCancelRequested {
		t.Fatalf("fabricated terminal state %+v %v", after, err)
	}
	intent, ok, err := svc.sessions.Store().GetV3SessionRunIntent(a.ChildSessionID, a.RunID)
	if err != nil || !ok || intent.Status != store.V3RunIntentRunning {
		t.Fatalf("canonical state changed %+v %v", intent, err)
	}
}
