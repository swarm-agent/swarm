package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: handleProjects must authenticate and revision-guard explicit unarchive
// requests, returning persisted needs_review metadata rather than restarting work.
// HTTP plus the real store proves route wiring and rejection postconditions.
func TestProjectTaskUnarchiveHTTP(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer func() {
		f.server.BeginShutdown()
		f.server.CancelInFlightRuns()
		f.server.WaitForInFlightRuns(5 * time.Second)
		f.db.Close()
	}()
	project := f.createProject(t)
	owner := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	store := f.server.sessions.Store()
	task := &pebblestore.ProjectTaskRecord{ID: "restore", ProjectID: project, Title: "Restore", Agent: "coder", Status: "needs_review", Archived: true, Revision: 2, WorkerName: "Worker"}
	if err := store.PutProjectTask(f.accountID, task); err != nil {
		t.Fatal(err)
	}
	path := "/" + project + "/tasks/restore/unarchive"
	before, _, _ := store.GetProjectTask(f.accountID, project, task.ID)
	for _, body := range []string{`{}`, `{"revision":0}`, `{"revision":1}`, `{"revision":2} trailing`} {
		if result := f.callAPI(http.MethodPost, path, body, owner); result.Code == http.StatusOK {
			t.Fatalf("invalid request accepted: %s", body)
		}
	}
	foreign := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: "foreign"}
	if result := f.callAPI(http.MethodPost, path, `{"revision":2}`, foreign); result.Code == http.StatusOK {
		t.Fatal("foreign request accepted")
	}
	got, _, _ := store.GetProjectTask(f.accountID, project, task.ID)
	if !reflect.DeepEqual(before, got) {
		t.Fatal("rejected HTTP request mutated task")
	}
	result := f.callAPI(http.MethodPost, path, `{"revision":2}`, owner)
	if result.Code != http.StatusOK {
		t.Fatalf("unarchive: %d %s", result.Code, result.Body.String())
	}
	var response struct {
		Task pebblestore.ProjectTaskRecord `json:"task"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Task.Archived || response.Task.Revision != 3 || response.Task.Status != "needs_review" || response.Task.WorkerName != "Worker" {
		t.Fatalf("incorrect receipt: %+v", response.Task)
	}
	got, _, _ = store.GetProjectTask(f.accountID, project, task.ID)
	if got.Archived || got.Revision != response.Task.Revision || got.Status != response.Task.Status {
		t.Fatal("receipt not persisted")
	}
	if repeat := f.callAPI(http.MethodPost, path, `{"revision":3}`, owner); repeat.Code != http.StatusConflict {
		t.Fatalf("already restored: %d", repeat.Code)
	}
}
