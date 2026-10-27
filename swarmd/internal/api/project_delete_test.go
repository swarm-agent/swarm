package api

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the project DELETE handler must reject active sessions/tasks without
// mutation, then remove only records and archive readable sessions. A real-store
// handler test is the narrowest layer covering HTTP errors, the archive service,
// task admission after deletion and preservation of actual folder contents.
func TestProjectDeleteRoute(t *testing.T) {
	for _, state := range []string{"running-task", "running-session", "active-lifecycle", "settled"} {
		t.Run(state, func(t *testing.T) {
			s, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
			p := testPrincipal()
			db := sessions.Store()
			folder := t.TempDir()
			file := filepath.Join(folder, "keep.txt")
			if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			project := &pebblestore.ProjectRecord{ID: "project", Name: "Project", Workspaces: []pebblestore.ProjectWorkspaceRef{{Path: folder}}}
			if err := db.PutProject(p.AccountScopeID, project); err != nil {
				t.Fatal(err)
			}
			foreign := httptest.NewRecorder()
			s.deleteProject(foreign, "other-account", "project")
			if foreign.Code != 404 {
				t.Fatalf("foreign delete: %d", foreign.Code)
			}
			if _, found, err := db.GetProject(p.AccountScopeID, "project"); err != nil || !found {
				t.Fatalf("foreign delete changed project: %v", err)
			}
			task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "Task", Agent: "finder", Status: "completed"}
			if state == "running-task" {
				task.Status = "in_progress"
			}
			if err := db.PutProjectTask(p.AccountScopeID, task); err != nil {
				t.Fatal(err)
			}
			if err := db.CreateSessionForAccount(pebblestore.SessionSnapshot{ID: "conversation", Metadata: map[string]any{"project_id": "project"}}, p.UserID, p.AccountScopeID); err != nil {
				t.Fatal(err)
			}
			if state == "running-session" {
				intent := pebblestore.V3SessionRunIntent{SessionID: "conversation", UserID: p.UserID, AccountScopeID: p.AccountScopeID, RunID: "run", Status: sessionruntime.RunIntentPendingExecutor}
				if _, err := sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationRecordRunIntent, SessionID: "conversation", UserID: p.UserID, AccountScopeID: p.AccountScopeID, RunIntent: &intent, IdempotencyKey: "run", PayloadHash: "run"}); err != nil {
					t.Fatal(err)
				}
			}
			if state == "active-lifecycle" {
				if err := db.UpsertSessionLifecycle(pebblestore.SessionLifecycleSnapshot{SessionID: "conversation", AccountScopeID: p.AccountScopeID, UserID: p.UserID, Active: true}); err != nil {
					t.Fatal(err)
				}
			}
			req := httptest.NewRequest("DELETE", ProjectsPath+"/project", nil)
			req = req.WithContext(identity.ContextWithPrincipal(req.Context(), p))
			response := httptest.NewRecorder()
			s.handleProjects(response, req)
			want := 200
			if state != "settled" {
				want = 409
			}
			if response.Code != want {
				t.Fatalf("HTTP %d: %s", response.Code, response.Body.String())
			}
			_, found, err := db.GetProject(p.AccountScopeID, "project")
			if err != nil || found != (state != "settled") {
				t.Fatalf("project found=%v err=%v", found, err)
			}
			_, found, err = db.GetProjectTask(p.AccountScopeID, "project", "task")
			if err != nil || found != (state != "settled") {
				t.Fatalf("task found=%v err=%v", found, err)
			}
			session, found, err := db.GetSession("conversation")
			if err != nil || found != (state != "settled") {
				t.Fatalf("session %+v found=%v err=%v", session, found, err)
			}
			if state == "settled" {
				tombstone, found, err := db.GetV3SessionTombstone("conversation")
				if err != nil || !found || !tombstone.Archived || tombstone.Deleted || tombstone.Session.ID != "conversation" {
					t.Fatalf("archive not readable: %+v %v", tombstone, err)
				}
				response = httptest.NewRecorder()
				s.handleProjects(response, req)
				if response.Code != 404 {
					t.Fatalf("missing project: %d", response.Code)
				}
			} else if !strings.Contains(response.Body.String(), "1") {
				t.Fatal("refusal omitted count")
			}
			if data, err := os.ReadFile(file); err != nil || string(data) != "keep" {
				t.Fatalf("folder changed: %q %v", data, err)
			}
		})
	}
}
