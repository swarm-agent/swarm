package api

import (
 "net/http"
 "strings"
 "testing"
 "time"

 "swarm/packages/swarmd/internal/identity"
 pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the user-visible project task archive/delete routes must honor account and
// project identity, exact revision and execution guards. Threats: unauthenticated
// mutation, stale write, or a DELETE silently orphaning a launched session. The
// handleProjects HTTP layer is the narrowest layer that proves request wiring.
func TestProjectTaskManagementHTTPGuards(t *testing.T) {
 f := setupMatrixTestFixture(t)
 defer func(){ f.server.BeginShutdown(); f.server.CancelInFlightRuns(); if !f.server.WaitForInFlightRuns(5*time.Second) { t.Error("runs not stopped") }; f.db.Close() }()
 projectID := f.createProject(t)
 p := identity.Principal{Type:"user", UserID:f.userID, AccountScopeID:f.accountID}
 task := &pebblestore.ProjectTaskRecord{ID:"archive-test", ProjectID:projectID, Title:"Archived task", Agent:"coder", Status:"blocked", Revision:1}
 if err := f.server.sessions.Store().PutProjectTask(f.accountID, task); err != nil { t.Fatal(err) }
 path := "/"+projectID+"/tasks/"+task.ID
 check := func(method, suffix, body string, want int) {
  t.Helper()
  result := f.callAPI(method, path+suffix, body, p)
  if result.Code != want { t.Fatalf("%s %s = %d: %s, want %d", method, suffix, result.Code, result.Body.String(), want) }
 }
 check(http.MethodDelete, "", "", http.StatusBadRequest)
 check(http.MethodDelete, "?revision=1", "", http.StatusConflict)
 check(http.MethodPost, "/archive", `{"revision":2}`, http.StatusConflict)
 check(http.MethodPost, "/archive", `{"revision":1} trailing`, http.StatusBadRequest)
 check(http.MethodPost, "/archive", `{"revision":1}`, http.StatusOK)
 check(http.MethodPost, "/archive", `{"revision":1}`, http.StatusConflict)
 check(http.MethodDelete, "?revision=1", "", http.StatusConflict)
 current, found, err := f.server.sessions.Store().GetProjectTask(f.accountID, projectID, task.ID)
 if err != nil || !found || !current.Archived || current.Revision != 2 { t.Fatalf("failed guard changed record: %+v %v",current,err) }
 active := f.callAPI(http.MethodGet, "/"+projectID+"/tasks", "", p)
 archived := f.callAPI(http.MethodGet, "/"+projectID+"/tasks?view=archived", "", p)
 if active.Code != 200 || strings.Contains(active.Body.String(), "archive-test") || archived.Code != 200 || !strings.Contains(archived.Body.String(), "archive-test") { t.Fatalf("archive partition: active=%s archived=%s",active.Body.String(),archived.Body.String()) }
 foreign := identity.Principal{Type:"user", UserID:f.userID, AccountScopeID:"foreign"}
 result := f.callAPI(http.MethodDelete,path+"?revision=2","",foreign)
 if result.Code == 200 { t.Fatal("foreign principal deleted task") }
 check(http.MethodDelete, "?revision=2", "", http.StatusOK)
 if _, found, err := f.server.sessions.Store().GetProjectTask(f.accountID,projectID,task.ID); err != nil || found { t.Fatalf("deletion not durable: %v %v",found,err) }
}
