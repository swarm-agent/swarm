package run

import (
 "context"
 "crypto/sha256"
 "encoding/hex"
 "encoding/json"
 "errors"
 "strings"
 "sync"
 "testing"
 "time"

 "swarm/packages/swarmd/internal/model"
 provideriface "swarm/packages/swarmd/internal/provider/interfaces"
 "swarm/packages/swarmd/internal/provider/registry"
 store "swarm/packages/swarmd/internal/store/pebble"
)

type designContractRunner struct { mu sync.Mutex; requests []provideriface.Request; call func(context.Context,provideriface.Request)(provideriface.Response,error) }
func (*designContractRunner) ID() string { return "test" }
func (r *designContractRunner) CreateResponse(ctx context.Context,req provideriface.Request)(provideriface.Response,error) { r.mu.Lock(); r.requests=append(r.requests,req); r.mu.Unlock(); return r.call(ctx,req) }
func (r *designContractRunner) CreateResponseStreaming(ctx context.Context,req provideriface.Request,_ func(provideriface.StreamEvent))(provideriface.Response,error) { return r.CreateResponse(ctx,req) }
func designExecutionFixture(t *testing.T)(*Service,store.DesignPrincipal,store.DesignRequest,*designContractRunner) {
 t.Helper()
 s,p,r:=designAllocationFixture(t)
 db:=s.sessions.DesignStore()
 catalog:=store.NewModelCatalogStore(db)
 if err:=catalog.SetRecord(store.ModelCatalogRecord{Provider:"test",Model:"configured-design"});err!=nil { t.Fatal(err) }
 s.model=model.NewService(store.NewModelStore(db),nil,model.NewCatalogService(catalog))
 if _,err:=store.NewModelStore(db).SetPreferenceForAccount(p.AccountID,p.PrincipalID,"test","configured-design","","","");err!=nil { t.Fatal(err) }
 s.agentModelSettings=nil
 runner:=&designContractRunner{call:func(context.Context,provideriface.Request)(provideriface.Response,error){return provideriface.Response{Text:"<!doctype html><html><body>card</body></html>"},nil}}
 s.providers=registry.New();s.providers.RegisterRunner(runner)
 return s,p,r,runner
}
func acceptDesignFixture(t *testing.T,s *Service,p store.DesignPrincipal,parent store.DesignRequest,id string,specs []store.DesignCandidateSpec,snapshots []store.DesignContextSnapshot) store.DesignRequest {
 t.Helper()
 in:=store.DesignSubmit{RequestID:id,IdempotencyKey:id,ParentSessionID:parent.ParentSessionID,ParentRunID:parent.ParentRunID,Candidates:specs,Context:snapshots}
 if _,err:=s.sessions.ApplySessionMutation(store.V3SessionMutationInput{SessionID:in.ParentSessionID,AccountScopeID:p.AccountID,UserID:p.PrincipalID,Kind:store.V3SessionMutationAcceptDesign,IdempotencyKey:id,PayloadHash:store.DesignAcceptanceHash(in),DesignAcceptance:&store.DesignAcceptance{Submit:in}});err!=nil { t.Fatal(err) }
 r,err:=s.sessions.DesignStore().GetDesignRequest(p,id);if err!=nil {t.Fatal(err)};return r
}

// Purpose: the narrow hermetic adapter/service contract proves actual source text
// (not inaccessible paths), configured model, isolated context and no tool grants
// cross executeDesign. A failed sibling cannot replace successful immutable bytes.
// This fake adapter test is not a live AI run or benchmark.
func TestDesignExecutionPayloadAndPartialFailure(t *testing.T) {
 s,p,parent,runner:=designExecutionFixture(t)
 snapshots:=[]store.DesignContextSnapshot{}
 for path,text:=range map[string]string{"TaskCard.tsx":"export const TaskCard = () => <article>task</article>","card.css":"article { color: rebeccapurple; }"} {
  h:=sha256.Sum256([]byte(text)); snapshots=append(snapshots,store.DesignContextSnapshot{Path:path,Content:[]byte(text),SHA256:hex.EncodeToString(h[:])})
 }
 r:=acceptDesignFixture(t,s,p,parent,"batch",[]store.DesignCandidateSpec{{ArtifactID:"one",Kind:store.DesignHTML,Operation:store.DesignGenerate,Brief:"style task card"},{ArtifactID:"two",Kind:store.DesignHTML,Operation:store.DesignGenerate,Brief:"fail candidate"}},snapshots)
 runner.call=func(_ context.Context,req provideriface.Request)(provideriface.Response,error){
  b,_:=json.Marshal(req.Input)
  if !strings.Contains(string(b),"TaskCard") || !strings.Contains(string(b),"rebeccapurple") { t.Error("actual source text missing") }
  if req.Model!="configured-design" || len(req.Tools)!=0 || req.ToolInvoker!=nil || req.ToolChoice!="none" || !req.ForceFreshProviderContext || req.NativeContinuationAllowed || req.WorkspacePath!="" {t.Error("provider boundary escaped")}
  if strings.Contains(string(b),"fail candidate") {return provideriface.Response{},errors.New("hermetic failure")}
  return provideriface.Response{Text:"<!doctype html><html><body>ok</body></html>"},nil
 }
 ctx,cancel:=context.WithTimeout(context.Background(),time.Second);defer cancel()
 s.executeDesign(ctx,p,r.ID,0);s.executeDesign(ctx,p,r.ID,1)
 got,err:=s.sessions.DesignStore().GetDesignRequest(p,r.ID)
 if err!=nil || got.State!=store.DesignPartial || got.Candidates[0].Attempts[0].Result==nil || got.Candidates[1].State!=store.DesignFailed {t.Fatalf("result: %+v %v",got,err)}
 history,err:=s.sessions.DesignStore().DesignHistory(p,"one",0,10)
 if err!=nil || len(history)!=1 || len(history[0].Content)!=0 {t.Fatal("metadata history",err)}
 s.executeDesign(ctx,p,r.ID,0)
 if len(runner.requests)!=2 {t.Fatal("replayed provider call")}
}

// Purpose: exact retained edit and plan-source bytes must be sent unchanged;
// foreign/stale references must fail before a provider call. Real Pebble reference
// checks plus an inspected hermetic provider request are the narrow boundary.
func TestDesignExecutionExactInputsAndPlan(t *testing.T) {
 s,p,parent,runner:=designExecutionFixture(t)
 ctx,cancel:=context.WithTimeout(context.Background(),time.Second);defer cancel()
 s.executeDesign(ctx,p,parent.ID,0)
 completed,_:=s.sessions.DesignStore().GetDesignRequest(p,parent.ID)
 base:=completed.Candidates[0].Attempts[0].Result
 edit:=acceptDesignFixture(t,s,p,parent,"edit",[]store.DesignCandidateSpec{{ArtifactID:"artifact",Kind:store.DesignHTML,Operation:store.DesignEdit,Brief:"change color",Base:base}},nil)
 s.executeDesign(ctx,p,edit.ID,0)
 b,_:=json.Marshal(runner.requests[1].Input)
 if !strings.Contains(string(b),"exact_base") || !strings.Contains(string(b),"card") || !strings.Contains(string(b),base.SHA256) {t.Fatal("exact edit bytes absent")}
 runner.call=func(_ context.Context,req provideriface.Request)(provideriface.Response,error){return provideriface.Response{Text:"Plan: preserve task hierarchy."},nil}
 plan:=acceptDesignFixture(t,s,p,parent,"plan",[]store.DesignCandidateSpec{{ArtifactID:"plan-artifact",Kind:store.DesignPlan,Operation:store.DesignGenerate,Brief:"plan only"}},nil)
 s.executeDesign(ctx,p,plan.ID,0)
 result,_:=s.sessions.DesignStore().GetDesignRequest(p,plan.ID)
 ref:=result.Candidates[0].Attempts[0].Result
 if ref==nil {t.Fatal("plan not retained")}
 html:=acceptDesignFixture(t,s,p,parent,"from-plan",[]store.DesignCandidateSpec{{ArtifactID:"from-plan-artifact",Kind:store.DesignHTML,Operation:store.DesignGenerate,Brief:"implement plan",PlanSource:ref}},nil)
 s.executeDesign(ctx,p,html.ID,0)
 b,_=json.Marshal(runner.requests[3].Input)
 if !strings.Contains(string(b),"Plan: preserve task hierarchy.") {t.Fatal("plan source missing")}
 foreign:=p;foreign.PrincipalID="foreign"
 s.executeDesign(ctx,foreign,parent.ID,0)
 if len(runner.requests)!=4 {t.Fatal("unauthorized provider submission")}
}

// Purpose: daemon ownership must outlive the parent and restart must never replay
// an uncertain allocated child. The real durable queue/claim boundary is exercised
// with a blocking hermetic adapter, bounded channels and no live provider.
func TestDesignDispatcherParentLifetimeAndCancellation(t *testing.T) {
 s,p,r,runner:=designExecutionFixture(t)
 started:=make(chan struct{})
 runner.call=func(ctx context.Context,_ provideriface.Request)(provideriface.Response,error){close(started);<-ctx.Done();return provideriface.Response{},ctx.Err()}
 parentIntent,_,_:=s.sessions.Store().GetV3SessionRunIntent(r.ParentSessionID,r.ParentRunID)
 parentIntent.Status=store.V3RunIntentCompleted
 if _,err:=s.sessions.ApplySessionMutation(store.V3SessionMutationInput{SessionID:r.ParentSessionID,AccountScopeID:p.AccountID,UserID:p.PrincipalID,Kind:store.V3SessionMutationRecordRunIntent,IdempotencyKey:"parent-return",PayloadHash:"parent-return",RunIntent:&parentIntent});err!=nil {t.Fatal(err)}
 ctx,cancel:=context.WithTimeout(context.Background(),2*time.Second);defer cancel()
 d:=s.StartDesignDispatcher(ctx)
 select {case <-started:case <-ctx.Done():d.Close();t.Fatal("not dispatched after parent return")}
 current,_:=s.sessions.DesignStore().GetDesignRequest(p,r.ID);a:=current.Candidates[0].Attempts[0]
 if _,err:=s.sessions.DesignStore().RecordDesignAttempt(p,r.ID,store.DesignAttemptMutation{IdempotencyKey:"stop",ExpectedRevision:current.Revision,Candidate:0,ChildSessionID:a.ChildSessionID,RunID:a.RunID,State:store.DesignCancelRequested});err!=nil {t.Fatal(err)}
 d.Close()
 current,_=s.sessions.DesignStore().GetDesignRequest(p,r.ID)
 if current.State!=store.DesignCancelled {t.Fatalf("cancellation not reconciled: %+v",current)}
 // A fresh queued probe proves the restarted dispatcher actually ran a
 // discovery/execution cycle; immediate Close would prove nothing.
 probe:=acceptDesignFixture(t,s,p,r,"restart-probe",[]store.DesignCandidateSpec{{ArtifactID:"restart-probe-artifact",Kind:store.DesignHTML,Operation:store.DesignGenerate,Brief:"probe"}},nil)
 runner.call=func(context.Context,provideriface.Request)(provideriface.Response,error){return provideriface.Response{Text:"<!doctype html><html></html>"},nil}
 d=s.StartDesignDispatcher(ctx)
 capacityBoundaryAwait(t,func()bool{got,err:=s.sessions.DesignStore().GetDesignRequest(p,probe.ID);return err==nil && got.State==store.DesignSucceeded})
 d.Close()
 current,_=s.sessions.DesignStore().GetDesignRequest(p,r.ID)
 if current.State!=store.DesignCancelled || len(runner.requests)!=2 {t.Fatal("restart replayed cancelled provider")}
}

// Purpose: a persisted pre-crash claim is uncertain regardless of whether a
// provider receipt exists. Recovery must interrupt, not replay, that child.
func TestDesignDispatcherUncertainRecovery(t *testing.T) {
 s,p,r,runner:=designExecutionFixture(t)
 ctx,cancel:=context.WithTimeout(context.Background(),time.Second);defer cancel()
 a,lease,err:=s.AllocateDesignChild(ctx,p,r.ID,r.Revision,0,1)
 if err!=nil {t.Fatal(err)};lease.Release()
 if err=s.designRunState(p,a,store.V3RunIntentPendingExecutor,store.V3RunIntentRunning);err!=nil {t.Fatal(err)}
 d:=s.StartDesignDispatcher(ctx)
 // Bounded test-only observation; production dispatch is wake/index driven.
 observe:=time.NewTicker(time.Millisecond);defer observe.Stop()
 for {
  current,readErr:=s.sessions.DesignStore().GetDesignRequest(p,r.ID)
  if readErr!=nil {d.Close();t.Fatal(readErr)}
  if current.State==store.DesignInterrupted {break}
  select {case <-observe.C:case <-ctx.Done():d.Close();t.Fatal("recovery deadline")}
 }
 d.Close()
 s.executeDesign(ctx,p,r.ID,0)
 after,_:=s.sessions.DesignStore().GetDesignRequest(p,r.ID)
 if after.State!=store.DesignInterrupted || len(runner.requests)!=0 {t.Fatal("uncertain execution replayed")}
}

// Purpose: sibling completion conflicts must retry only persistence, retaining
// both exact outputs, while duplicate canonical claims never authorize replay.
// Real allocation and V3 CAS are tested without needing a provider invocation.
func TestDesignExecutionSiblingCASAndDuplicateClaim(t *testing.T) {
 s,p,parent,_:=designExecutionFixture(t)
 r:=acceptDesignFixture(t,s,p,parent,"siblings",[]store.DesignCandidateSpec{{ArtifactID:"left",Kind:store.DesignHTML,Operation:store.DesignGenerate,Brief:"left"},{ArtifactID:"right",Kind:store.DesignHTML,Operation:store.DesignGenerate,Brief:"right"}},nil)
 ctx,cancel:=context.WithTimeout(context.Background(),time.Second);defer cancel()
 for i:=0;i<2;i++ {
  r,_=s.sessions.DesignStore().GetDesignRequest(p,r.ID)
  a,lease,err:=s.AllocateDesignChild(ctx,p,r.ID,r.Revision,i,1)
  if err!=nil {t.Fatal(err)};lease.Release()
  if err=s.designRunState(p,a,store.V3RunIntentPendingExecutor,store.V3RunIntentRunning);err!=nil {t.Fatal(err)}
  if err=s.designRunState(p,a,store.V3RunIntentPendingExecutor,store.V3RunIntentRunning);err==nil {t.Fatal("duplicate claim accepted")}
 }
 done:=make(chan error,2)
 for i:=0;i<2;i++ {go func(i int){done<-s.finishDesign(p,r.ID,i,[]byte("<!doctype html><html><body>retained</body></html>"),store.DesignSucceeded)}(i)}
 for i:=0;i<2;i++ {select {case err:=<-done:if err!=nil {t.Fatal(err)};case <-ctx.Done():t.Fatal("completion deadline")}}
 result,err:=s.sessions.DesignStore().GetDesignRequest(p,r.ID)
 if err!=nil || result.State!=store.DesignSucceeded {t.Fatalf("lost sibling: %+v %v",result,err)}
 for _,c:=range result.Candidates {if c.Attempts[0].Result==nil {t.Fatal("missing immutable result")}}
}
