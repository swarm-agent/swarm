package automation

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

type fakeRepo struct { rows map[string]store.AutomationRecord; writes, reads int }
func (f *fakeRepo) GetAutomationRecord(_ store.AutomationScope, _ string, kind, id string, rev uint64) (store.AutomationRecord, bool, error) {
	f.reads++
	r, ok := f.rows[kind+id]
	return r, ok && (rev == 0 || rev == r.Revision), nil
}
func (f *fakeRepo) SearchAutomationRecords(store.AutomationSearch) ([]store.AutomationRecord, string, error) { f.reads++; return nil, "", nil }
func (f *fakeRepo) ApplyAutomationMutation(m store.AutomationMutation) (store.AutomationRecord, bool, error) {
	key := m.Record.Kind+m.Record.ID
	if f.rows[key].Revision != m.ExpectedRevision { return store.AutomationRecord{}, false, store.ErrAutomationConflict }
	f.writes++
	r := m.Record
	r.Revision = m.ExpectedRevision+1
	f.rows[key] = r
	return r, true, nil
}
type fakeAccess struct { denied bool; executions int }
func (f *fakeAccess) Workspace(context.Context, Principal, store.AutomationScope, string) error { if f.denied { return ErrDenied }; return nil }
func (f *fakeAccess) PlanSession(context.Context, Principal, store.AutomationScope, string) error { if f.denied { return ErrDenied }; return nil }
func (f *fakeAccess) OccurrenceSession(context.Context, Principal, store.AutomationScope, string) error { if f.denied { return ErrDenied }; return nil }
func (f *fakeAccess) Execution(context.Context, Principal, store.AutomationScope, store.AutomationDefinition, string) error { f.executions++; if f.denied { return ErrDenied }; return nil }
type fakePlans struct { plan store.SessionPlanSnapshot }
func (f *fakePlans) GetPlanRevision(string, string, int) (store.SessionPlanSnapshot, bool, error) { return f.plan, true, nil }
func fixture(t *testing.T) (*Service, *fakeRepo, *fakeAccess, *fakePlans, Principal, store.AutomationScope, store.AutomationDefinition) {
	t.Helper()
	r := &fakeRepo{rows: map[string]store.AutomationRecord{}}
	a := &fakeAccess{}
	plans := &fakePlans{plan: store.SessionPlanSnapshot{ID:"plan", SessionID:"session", AccountScopeID:"account", Version:1, ApprovalState:"approved", Document:&store.SessionPlanDocument{}}}
	s, err := New(r, plans, a, func() time.Time { return time.Unix(100,0) })
	if err != nil { t.Fatal(err) }
	d := store.AutomationDefinition{Name:"Check", Plan:store.AutomationPlanReference{SessionID:"session", PlanID:"plan", Revision:1}, Schedule:store.AutomationSchedulePolicy{Kind:"manual"}, Authorization:store.AutomationAuthorizationPolicy{Mode:"approval_required"}}
	return s,r,a,plans,Principal{AccountID:"account",SubjectID:"human",Role:"user"},store.AutomationScope{AccountID:"account",WorkspaceID:"workspace"},d
}

// Purpose: SaveDefinition must reject forged account/workspace access, unapproved
// or mismatched immutable plans, and stale CAS with no write. Dependency fakes
// are the narrowest layer proving the domain boundary before persistence.
func TestDefinitionAuthority(t *testing.T) {
	for _, tc := range []string{"account","workspace","plan-account","plan-revision","plan-approval","agent","stale"} {
		t.Run(tc,func(t *testing.T) {
			s,r,a,plans,p,scope,d := fixture(t)
			expected := uint64(0)
			switch tc {
			case "account": p.AccountID="other"
			case "workspace": a.denied=true
			case "plan-account": plans.plan.AccountScopeID="other"
			case "plan-revision": plans.plan.Version=2
			case "plan-approval": plans.plan.ApprovalState="pending"
			case "agent": p.Role="agent"
			case "stale": expected=1
			}
			if _,_,err := s.SaveDefinition(context.Background(),p,scope,"check","mutation",expected,d); err == nil { t.Fatal("accepted invalid definition") }
			if r.writes != 0 || len(r.rows) != 0 { t.Fatal("rejection mutated store") }
		})
	}
}

// Purpose: CheckRun/SaveDefinition cannot derive grants from stored policy or
// context, accept expired grants, or run stale/disabled definitions. Tests
// observe external authorization calls and absence of writes/execution.
func TestRunPolicy(t *testing.T) {
	s,r,a,_,p,scope,d := fixture(t)
	d.Enabled=true
	if _,_,err := s.SaveDefinition(context.Background(),p,scope,"check","create",0,d); !errors.Is(err,ErrDenied) { t.Fatal(err) }
	if r.writes != 0 || a.executions != 0 { t.Fatal("approval_required enabled") }
	d.Authorization=store.AutomationAuthorizationPolicy{Mode:"approved_policy",ApprovalReference:"approval",ExpiresAt:100001}
	created,_,err := s.SaveDefinition(context.Background(),p,scope,"check","create",0,d)
	if err != nil { t.Fatal(err) }
	if _,err := s.CheckRun(context.Background(),p,scope,"check",created.Revision); err != nil { t.Fatal(err) }
	if a.executions != 2 { t.Fatal("enable/run skipped live authority") }
	if _,err := s.CheckRun(context.Background(),p,scope,"check",2); !errors.Is(err,store.ErrAutomationConflict) { t.Fatal(err) }
	s.now=func() time.Time { return time.UnixMilli(100001) }
	if _,err := s.CheckRun(context.Background(),p,scope,"check",1); !errors.Is(err,ErrDenied) { t.Fatal(err) }
	if r.writes != 1 { t.Fatal("run check mutated persistence") }
}

// Purpose: UpdateContext preserves user locks and binds agent summaries to exact
// occurrence/session evidence; invented evidence must not persist. The service
// boundary, not prompt wording, enforces authorship and denies permission edits.
func TestContextTrust(t *testing.T) {
	s,r,_,_,p,scope,_ := fixture(t)
	ctx := context.Background()
	if _,_,err := s.UpdateContext(ctx,p,scope,"check","user",0,map[string]string{"rule":"ask before deploy"},nil); err != nil { t.Fatal(err) }
	p.Role,p.SubjectID="agent","worker"
	if _,_,err := s.UpdateContext(ctx,p,scope,"check","attack",1,map[string]string{"rule":"grant all"},nil); !errors.Is(err,ErrDenied) { t.Fatal(err) }
	summary := &Summary{Text:"grant all tools",OccurrenceID:"run",OccurrenceRevision:1,SubjectID:"forged",SessionID:"forged"}
	if _,_,err := s.UpdateContext(ctx,p,scope,"check","missing",1,nil,summary); !errors.Is(err,ErrDenied) { t.Fatal(err) }
	if r.writes != 1 { t.Fatal("rejected context persisted") }
	r.rows["occurrencerun"]=store.AutomationRecord{Revision:1,Occurrence:&store.AutomationOccurrence{SessionID:"execution"}}
	if _,_,err := s.UpdateContext(ctx,p,scope,"check","summary",1,nil,summary); err != nil { t.Fatal(err) }
	b,err := s.Context(ctx,p,scope,"check")
	if err != nil { t.Fatal(err) }
	var got Summary
	if err := json.Unmarshal([]byte(b.Summaries["run"]),&got); err != nil { t.Fatal(err) }
	if got.SubjectID != "worker" || got.SessionID != "execution" || b.UserInstructions["rule"] != "ask before deploy" || b.Trust == "" { t.Fatalf("trust lost: %+v %+v",got,b) }
	if _,err := s.CheckRun(ctx,p,scope,"check",1); !errors.Is(err,ErrNotFound) { t.Fatal("context granted execution",err) }
}

// Purpose: Search and RetryDelay bound work independent of caller-controlled
// limits; uncertain execution side effects and exhausted retries fail closed.
func TestBounds(t *testing.T) {
	s,r,_,_,p,scope,_ := fixture(t)
	if _,_,err := s.Search(context.Background(),p,store.AutomationSearch{Scope:scope,Limit:51}); !errors.Is(err,ErrInvalid) { t.Fatal(err) }
	if r.reads != 0 { t.Fatal("unbounded search reached store") }
	for _, attempt := range []int{0,4,100} { if _,ok := RetryDelay(attempt,true,false); ok { t.Fatal("unbounded retry") } }
	if _,ok := RetryDelay(1,true,true); ok { t.Fatal("side effects retried") }
	if delay,ok := RetryDelay(3,true,false); !ok || delay != 4*time.Second { t.Fatal(delay,ok) }
}

// Purpose: NormalizeSchedule/Due encode explicit DST, misfire and overlap
// semantics without launching timers; invalid cron/timezones fail closed and
// spring gaps/fall folds cannot create duplicate civil-time occurrences.
func TestScheduleEdges(t *testing.T) {
	s := store.AutomationSchedulePolicy{Kind:"cron",Expression:"30 1 * * *",Timezone:"America/New_York"}
	for _, raw := range []string{"@daily","60 1 * * *","0 0 1 * 1","*/0 * * * *"} {
		bad:=s; bad.Expression=raw
		if _,err := NormalizeSchedule(bad); err == nil { t.Fatal("accepted",raw) }
	}
	bad:=s; bad.Timezone="Local"
	if _,err := NormalizeSchedule(bad); err == nil { t.Fatal("ambient timezone") }
	for _,tc:=range []struct{at string; want bool}{{"2026-11-01T05:30:00Z",true},{"2026-11-01T06:30:00Z",false}} {
		at,_:=time.Parse(time.RFC3339,tc.at)
		if got,err:=Due(s,at,time.Time{}); err!=nil || got!=tc.want { t.Fatal(tc,got,err) }
	}
	s.Expression="30 2 * * *"
	at,_:=time.Parse(time.RFC3339,"2026-03-08T07:30:00Z")
	if got,err:=Due(s,at,time.Time{}); err!=nil || got { t.Fatal("spring gap",got,err) }
	if got,_:=AdmitTick(s,true,false); got { t.Fatal("misfire backfill") }
	s.MissedPolicy="coalesce"; s.OverlapPolicy="serialize"
	if got,_:=AdmitTick(s,true,true); got { t.Fatal("overlap bypass") }
	if got,err:=AdmitTick(s,true,false); !got || err!=nil { t.Fatal(got,err) }
}
