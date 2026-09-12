package pebblestore

import (
	"errors"
	"reflect"
	"sync"
	"testing"
)

// Purpose: the private V2 participant in ApplyV3SessionMutation must create
// authorization and binding only with the exact review; a real store proves
// failure rollback, concurrent idempotency and durable reload, without V1 fixtures.
func TestAutomationV2AtomicAcceptance(t *testing.T) {
	for _, expiration := range []AutomationV2Expiration{{Kind:"indefinite"},{Kind:"at",ExpiresAt:4102444800000}} {
		t.Run(expiration.Kind,func(t *testing.T){ testAutomationV2AtomicAcceptance(t,expiration) })
	}
}
func testAutomationV2AtomicAcceptance(t *testing.T, expiration AutomationV2Expiration) {
	path := t.TempDir()
	db,err := Open(path); if err != nil { t.Fatal(err) }
	s := NewSessionStore(db)
	available := true
	original := SessionSnapshot{ID:"conversation",AccountScopeID:"account",UserID:"owner",WorkspacePath:t.TempDir(),WorkspaceGrants:[]WorkspaceGrant{{Kind:WorkspaceGrantPrimary,WorkspaceID:"workspace",Path:t.TempDir(),Available:&available}}}
	if err := s.CreateSession(original); err != nil { t.Fatal(err) }
	doc := SessionPlanDocument{Title:"Exact instructions",Info:SessionPlanInfo{Goal:"Preserve these bytes"},AutomationV2:&AutomationV2Settings{SchemaVersion:2,Schedule:AutomationV2Schedule{Kind:"interval",IntervalSeconds:60},Missed:"skip",Overlap:"serialize",ActivateOnAccept:true,Expiration:expiration}}
	p,err := s.ProposeAutomationV2("account","owner","workspace",original.ID,doc,AutomationV2Review{}); if err != nil { t.Fatal(err) }
	if _,ok,err := s.GetAutomationV2Record("account","owner","workspace",original.ID); err != nil || ok { t.Fatal("pending authorization",ok,err) }
	if _,ok,err := s.GetV3SessionActiveRunIntent(original.ID); err != nil || ok { t.Fatal("pending run",ok,err) }
	bad := p.AutomationV2Review; bad.Digest = "stale"
	if _,err := s.AcceptAutomationV2("account","owner","workspace",original.ID,bad); err == nil { t.Fatal("stale accepted") }
	if _,err := s.AcceptAutomationV2("account","foreign","workspace",original.ID,p.AutomationV2Review); err == nil { t.Fatal("foreign accepted") }
	restore := s.SetAutomationV2CommitHookForTest(func(string) error { return errors.New("injected") })
	if _,err := s.AcceptAutomationV2("account","owner","workspace",original.ID,p.AutomationV2Review); err == nil { t.Fatal("failure accepted") }
	restore()
	current,_,err := s.GetSession(original.ID); if err != nil || current.AutomationV2 != nil { t.Fatal("partial binding",err) }
	if _,ok,err := s.GetAutomationV2Record("account","owner","workspace",original.ID); err != nil || ok { t.Fatal("partial record",err) }
	var wg sync.WaitGroup
	results := make(chan AutomationV2Record,2)
	failures := make(chan error,2)
	for i:=0;i<2;i++ { wg.Add(1); go func(){ defer wg.Done(); r,e := s.AcceptAutomationV2("account","owner","workspace",original.ID,p.AutomationV2Review); results<-r; failures<-e }() }
	wg.Wait(); close(results); close(failures)
	for e := range failures { if e != nil { t.Fatal(e) } }
	var accepted AutomationV2Record
	for r := range results { if accepted.AutomationID != "" && !reflect.DeepEqual(accepted,r) { t.Fatal("distinct receipts") }; accepted=r }
	if !reflect.DeepEqual(accepted.Document,doc) || accepted.Authorization != expiration || !accepted.Enabled { t.Fatal("review changed",accepted) }
	if err := db.Close(); err != nil { t.Fatal(err) }
	db,err = Open(path); if err != nil { t.Fatal(err) }; defer db.Close()
	s = NewSessionStore(db)
	replayed,err := s.AcceptAutomationV2("account","owner","workspace",original.ID,p.AutomationV2Review)
	if err != nil || !reflect.DeepEqual(accepted,replayed) { t.Fatal("restart replay changed",err) }
}

// Purpose: policy validation rejects ambiguous schedules and implicit finite
// grants at the narrow pure validator before any durable mutation is possible.
func TestAutomationV2Policy(t *testing.T) {
	base := AutomationV2Settings{SchemaVersion:2,Schedule:AutomationV2Schedule{Kind:"interval",IntervalSeconds:60},Missed:"coalesce",Overlap:"independent",ActivateOnAccept:true,Expiration:AutomationV2Expiration{Kind:"at",ExpiresAt:2000}}
	if err := ValidateAutomationV2Settings(&base,1000); err != nil { t.Fatal(err) }
	for _,schedule := range []AutomationV2Schedule{{Kind:"interval",IntervalSeconds:59},{Kind:"cron",Cron:"0 0 1 * 1",Timezone:"UTC"},{Kind:"cron",Cron:"0-5 * * * *",Timezone:"UTC"},{Kind:"cron",Cron:"* * * * *",Timezone:"Local"}} {
		a := base; a.Schedule=schedule
		if ValidateAutomationV2Settings(&a,1000)==nil { t.Fatal("invalid schedule accepted",schedule) }
	}
	if ValidateAutomationV2Settings(&base,2000)==nil { t.Fatal("expired grant accepted") }
}
