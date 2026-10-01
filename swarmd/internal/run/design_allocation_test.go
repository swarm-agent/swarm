package run

import (
 "context"
 "errors"
 "testing"
 "time"

 "swarm/packages/swarmd/internal/executioncapacity"
 store "swarm/packages/swarmd/internal/store/pebble"
)

func designAllocationFixture(t *testing.T) (*Service,store.DesignPrincipal,store.DesignRequest) {
 t.Helper()
 svc,parent,_,_ := capacityBoundaryFixture(t)
 p := store.DesignPrincipal{AccountID:parent.AccountScopeID,PrincipalID:parent.UserID}
 _,err := svc.sessions.ApplySessionMutation(store.V3SessionMutationInput{SessionID:parent.ID,UserID:p.PrincipalID,AccountScopeID:p.AccountID,Kind:store.V3SessionMutationRecordRunIntent,IdempotencyKey:"design-parent",PayloadHash:"design-parent",RunIntent:&store.V3SessionRunIntent{SessionID:parent.ID,RunID:"design-parent",Status:store.V3RunIntentPendingExecutor}})
 if err != nil { t.Fatal(err) }
 submit := store.DesignSubmit{RequestID:"request",IdempotencyKey:"request",ParentSessionID:parent.ID,ParentRunID:"design-parent",Candidates:[]store.DesignCandidateSpec{{ArtifactID:"artifact",Kind:store.DesignHTML,Operation:store.DesignGenerate,Brief:"Design a card"}}}
 _,err = svc.sessions.ApplySessionMutation(store.V3SessionMutationInput{SessionID:parent.ID,UserID:p.PrincipalID,AccountScopeID:p.AccountID,Kind:store.V3SessionMutationAcceptDesign,IdempotencyKey:submit.IdempotencyKey,PayloadHash:store.DesignAcceptanceHash(submit),DesignAcceptance:&store.DesignAcceptance{Submit:submit}})
 if err != nil { t.Fatal(err) }
 r,err := svc.sessions.DesignStore().GetDesignRequest(p,submit.RequestID); if err != nil { t.Fatal(err) }
 return svc,p,r
}

// Purpose: AllocateDesignChild must use permission-owned configured capacity, not
// a private limit. A bounded admission timeout must leave no child or attempt.
// The service fixture uses real admission and Pebble, without invoking providers.
func TestDesignAllocationAdmissionRejection(t *testing.T) {
 svc,p,r := designAllocationFixture(t)
 ctx,cancel := context.WithTimeout(context.Background(),time.Second); defer cancel()
 lease,err := svc.permissions.AdmitExecution(ctx,executioncapacity.AcquireRequest{AccountScopeID:p.AccountID,SessionID:r.ParentSessionID,RunID:r.ParentRunID})
 if err != nil { t.Fatal(err) }; defer lease.Release()
 bounded,stop := context.WithTimeout(ctx,20*time.Millisecond); defer stop()
 _,got,err := svc.AllocateDesignChild(bounded,p,r.ID,r.Revision,0,1)
 if !errors.Is(err,context.DeadlineExceeded) || got != nil { t.Fatalf("admission: %v %v",got,err) }
 after,err := svc.sessions.DesignStore().GetDesignRequest(p,r.ID)
 if err != nil || after.Revision != r.Revision || len(after.Candidates[0].Attempts)!=0 { t.Fatalf("partial attempt: %+v %v",after,err) }
 if _,ok,err := svc.sessions.GetSession(store.DesignChildID(p,r.ID,0,1)); err != nil || ok { t.Fatal("orphan child",err) }
}

// Purpose: cancellation reconciliation must reject foreign authority, stop the
// actual canonical pending run, and only then confirm history. Repeated polling
// cannot fabricate success or allocate another child. No provider is necessary.
func TestDesignAllocationCancellation(t *testing.T) {
 svc,p,r := designAllocationFixture(t)
 input := store.NewDesignAllocationMutation(p,store.DesignAllocation{RequestID:r.ID,ExpectedRevision:r.Revision,Candidate:0,Attempt:1,Preference:store.ModelPreference{Provider:"test",Model:"configured"}})
 if _,err := svc.sessions.ApplySessionMutation(input); err != nil { t.Fatal(err) }
 r,err := svc.sessions.DesignStore().GetDesignRequest(p,r.ID); if err != nil { t.Fatal(err) }
 a := r.Candidates[0].Attempts[0]
 r,err = svc.sessions.DesignStore().RecordDesignAttempt(p,r.ID,store.DesignAttemptMutation{IdempotencyKey:"cancel",ExpectedRevision:r.Revision,Candidate:0,State:store.DesignCancelRequested,ChildSessionID:a.ChildSessionID,RunID:a.RunID})
 if err != nil { t.Fatal(err) }
 foreign := p; foreign.AccountID = "foreign"
 if _,err := svc.ReconcileDesignCancellation(foreign,r.ID,0); !errors.Is(err,store.ErrDesignNotFound) { t.Fatal(err) }
 r,err = svc.ReconcileDesignCancellation(p,r.ID,0)
 if err != nil || r.State != store.DesignCancelled { t.Fatalf("cancel: %+v %v",r,err) }
 intent,ok,err := svc.sessions.Store().GetV3SessionRunIntent(a.ChildSessionID,a.RunID)
 if err != nil || !ok || intent.Status != store.V3RunIntentCancelled { t.Fatalf("canonical cancellation: %+v %v",intent,err) }
 if _,err := svc.ReconcileDesignCancellation(p,r.ID,0); !errors.Is(err,store.ErrDesignConflict) { t.Fatal("duplicate confirmation",err) }
}
