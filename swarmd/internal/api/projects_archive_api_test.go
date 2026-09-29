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

func TestProjectTaskArchiveLivenessGuards(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer func() {
		f.server.BeginShutdown()
		f.server.CancelInFlightRuns()
		if !f.server.WaitForInFlightRuns(5 * time.Second) {
			t.Error("runs not stopped")
		}
		f.db.Close()
	}()
	projectID := f.createProject(t)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	store := f.server.sessions.Store()

	// 1. Task with WorktreeBranch and in_progress status but NO session: MUST be archivable!
	unlinkedTask := &pebblestore.ProjectTaskRecord{
		ID:             "unlinked-task",
		ProjectID:      projectID,
		Title:          "Unlinked task",
		Agent:          "coder",
		Status:         "in_progress",
		WorktreeBranch: "agent/unlinked-branch",
		Revision:       1,
	}
	if err := store.PutProjectTask(f.accountID, unlinkedTask); err != nil {
		t.Fatal(err)
	}
	res := f.callAPI(http.MethodPost, "/"+projectID+"/tasks/"+unlinkedTask.ID+"/archive", `{"revision":1}`, p)
	if res.Code != http.StatusOK {
		t.Fatalf("expected 200 archiving task without session, got %d: %s", res.Code, res.Body.String())
	}

	// 2. Task with active running session: MUST be protected from archiving (409 Conflict)!
	activeSessID := "session-running-123"
	if err := store.CreateSession(pebblestore.SessionSnapshot{
		ID:             activeSessID,
		AccountScopeID: f.accountID,
		Lifecycle:      &pebblestore.SessionLifecycleSnapshot{Active: true, Phase: "running"},
	}); err != nil {
		t.Fatal(err)
	}
	activeTask := &pebblestore.ProjectTaskRecord{
		ID:             "active-task",
		ProjectID:      projectID,
		Title:          "Active task",
		Agent:          "coder",
		Status:         "in_progress",
		SessionID:      activeSessID,
		WorktreeBranch: "agent/active-branch",
		Revision:       1,
	}
	if err := store.PutProjectTask(f.accountID, activeTask); err != nil {
		t.Fatal(err)
	}
	resActive := f.callAPI(http.MethodPost, "/"+projectID+"/tasks/"+activeTask.ID+"/archive", `{"revision":1}`, p)
	if resActive.Code != http.StatusConflict {
		t.Fatalf("expected 409 archiving actively running task, got %d: %s", resActive.Code, resActive.Body.String())
	}
	if !strings.Contains(resActive.Body.String(), "active run") {
		t.Fatalf("expected active run error message, got: %s", resActive.Body.String())
	}

	// 3. Task with terminated/inactive session: MUST be archivable!
	doneSessID := "session-done-456"
	if err := store.CreateSession(pebblestore.SessionSnapshot{
		ID:             doneSessID,
		AccountScopeID: f.accountID,
		Lifecycle:      &pebblestore.SessionLifecycleSnapshot{Active: false, Phase: "completed"},
	}); err != nil {
		t.Fatal(err)
	}
	doneTask := &pebblestore.ProjectTaskRecord{
		ID:             "done-task",
		ProjectID:      projectID,
		Title:          "Done task",
		Agent:          "coder",
		Status:         "in_progress",
		SessionID:      doneSessID,
		WorktreeBranch: "agent/done-branch",
		Revision:       1,
	}
	if err := store.PutProjectTask(f.accountID, doneTask); err != nil {
		t.Fatal(err)
	}
	resDone := f.callAPI(http.MethodPost, "/"+projectID+"/tasks/"+doneTask.ID+"/archive", `{"revision":1}`, p)
	if resDone.Code != http.StatusOK {
		t.Fatalf("expected 200 archiving terminated session task, got %d: %s", resDone.Code, resDone.Body.String())
	}
}
