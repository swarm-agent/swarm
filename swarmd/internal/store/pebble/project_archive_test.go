package pebblestore

import (
 "strings"
 "testing"
)

// Purpose: archival is metadata-only even for historical incoherent contracts;
// strict PutProjectTask validation remains the creation authority. Threats are
// a failed archive or a mutation of retained execution data, stale revision,
// foreign scope, and accidental acceptance of linked active work. The Pebble
// store is the narrowest layer proving durable record fidelity and guards.
func TestArchiveHistoricalProjectTask(t *testing.T) {
 db,err:=Open(t.TempDir()); if err!=nil {t.Fatal(err)}; defer db.Close()
 s:=NewSessionStore(db)
 project:=&ProjectRecord{ID:"project-a",Name:"Project"}; if err:=s.PutProject("account-a",project);err!=nil {t.Fatal(err)}
 legacy:=&ProjectTaskRecord{ID:"legacy",ProjectID:project.ID,Title:"Old task",Agent:"coder",OutcomeType:"media_bundle",Status:"blocked",Revision:1,FullPlanMarkdown:"retained plan",Deliverables:[]ProjectTaskDeliverable{{Kind:"image"}}}
 if err:=s.PutProjectTask("account-a",legacy);err==nil {t.Fatal("creation accepted incoherent contract")}
 // Historical record is injected through the store's durable mutation boundary,
 // never via a production creation or a local database edit.
 legacy.AccountID="account-a"
 db.projectsMu.Lock(); mut,err:=s.persistProjectTaskLocked("account-a",legacy,false);db.projectsMu.Unlock()
 if err!=nil {t.Fatal(err)};db.publishProjectRealtime(mut)
 if _,err:=s.ArchiveProjectTaskIfRevision("account-b",project.ID,legacy.ID,1);err==nil {t.Fatal("foreign account archived task")}
 if _,err:=s.ArchiveProjectTaskIfRevision("account-a","project-b",legacy.ID,1);err==nil {t.Fatal("foreign project archived task")}
 if _,err:=s.ArchiveProjectTaskIfRevision("account-a",project.ID,legacy.ID,2);err==nil {t.Fatal("stale revision archived task")}
 before,ok,err:=s.GetProjectTask("account-a",project.ID,legacy.ID);if err!=nil||!ok||before.Archived||before.Revision!=1 {t.Fatalf("rejected archive changed record: %+v %v",before,err)}
 archived,err:=s.ArchiveProjectTaskIfRevision("account-a",project.ID,legacy.ID,1);if err!=nil {t.Fatal(err)}
 if !archived.Archived||archived.Revision!=2 {t.Fatalf("archive metadata: %+v",archived)}
 retained,ok,err:=s.GetProjectTask("account-a",project.ID,legacy.ID)
 if err!=nil||!ok||!retained.Archived||retained.Revision!=2||retained.Agent!="coder"||retained.OutcomeType!="media_bundle"||retained.FullPlanMarkdown!="retained plan"||len(retained.Deliverables)!=1||retained.Status!="blocked" {t.Fatalf("archive lost historical record: %+v %v",retained,err)}
 if _,err:=s.ArchiveProjectTaskIfRevision("account-a",project.ID,legacy.ID,2);err==nil {t.Fatal("duplicate archival succeeded")}
 active:=&ProjectTaskRecord{ID:"active",ProjectID:project.ID,Title:"Active",Agent:"coder",Status:"in_progress",SessionID:"session-running",Revision:1}
 if err:=s.PutProjectTask("account-a",active);err!=nil {t.Fatal(err)}
 if _,err:=s.ArchiveProjectTaskIfRevision("account-a",project.ID,active.ID,1);err==nil||!strings.Contains(err.Error(),"lifecycle") {t.Fatalf("linked active task not protected: %v",err)}
 got,_,err:=s.GetProjectTask("account-a",project.ID,active.ID);if err!=nil||got.Archived||got.Revision!=1 {t.Fatalf("active task mutated: %+v %v",got,err)}
 stale:=&ProjectTaskRecord{ID:"stale",ProjectID:project.ID,Title:"Stale",Agent:"coder",Status:"in_progress",Revision:1}
 if err:=s.PutProjectTask("account-a",stale);err!=nil {t.Fatal(err)}
 got,err=s.ArchiveProjectTaskIfRevision("account-a",project.ID,stale.ID,1);if err!=nil||!got.Archived||got.Status!="in_progress" {t.Fatalf("orphaned historical task archive: %+v %v",got,err)}
}
