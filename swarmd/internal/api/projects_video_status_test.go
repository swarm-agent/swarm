package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: direct video status belongs to generation, not chat-session routing.
// A warning plus no session/output must not invent a terminal failure on reads.
// handleProjects list/detail serialization and syncTaskSessionState own this
// boundary. Real temporary storage plus handler reads check that pending output
// and terminal errors survive reload unchanged, without read-triggered writes.
func TestDirectVideoSessionSyncPreservesGenerationStatus(t *testing.T) {
	server, db, p := setupDirectMediaTestServer(t)
	project := &pebblestore.ProjectRecord{ID: "video-status-project", AccountID: p.AccountScopeID, Name: "Video status"}
	if err := db.PutProject(p.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"pending_approval", "pending", "queued", "in_progress", "needs_review", "completed", "failed", "cancelled", "rejected"} {
		t.Run(status, func(t *testing.T) {
			task := &pebblestore.ProjectTaskRecord{
				ID: "video-" + status, ProjectID: project.ID, AccountID: p.AccountScopeID,
				Title: "Video", Agent: "video", Status: status, RouterAlert: "Routing warning",
			}
			if status == "failed" {
				task.LastError = "Provider rejected generation"
			}
			if err := db.PutProjectTask(p.AccountScopeID, task); err != nil {
				t.Fatal(err)
			}
			loaded, ok, err := db.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
			if err != nil || !ok {
				t.Fatalf("reload: ok=%v err=%v", ok, err)
			}
			syncTaskSessionState(loaded, db)
			if loaded.Status != status || loaded.LastError != task.LastError || loaded.RouterAlert != task.RouterAlert {
				t.Fatalf("generation state changed by session reconciliation: %+v", loaded)
			}
			for _, suffix := range []string{"", "/" + task.ID} {
				req := httptest.NewRequest(http.MethodGet, "/v3/projects/"+project.ID+"/tasks"+suffix, nil)
				req = req.WithContext(context.WithValue(req.Context(), productPrincipalRequestContextKey, p))
				req = req.WithContext(context.WithValue(req.Context(), productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{AccountScopeID: p.AccountScopeID, UserID: p.UserID, Scopes: []string{"projects:read"}}))
				response := httptest.NewRecorder()
				server.handleProjects(response, req)
				if response.Code != http.StatusOK {
					t.Fatalf("task read %q: %d %s", suffix, response.Code, response.Body.String())
				}
				var body struct {
					Task  *pebblestore.ProjectTaskRecord  `json:"task"`
					Tasks []pebblestore.ProjectTaskRecord `json:"tasks"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				presented := body.Task
				for i := range body.Tasks {
					if body.Tasks[i].ID == task.ID {
						presented = &body.Tasks[i]
					}
				}
				if presented == nil || presented.Status != status || presented.LastError != task.LastError || presented.SessionID != "" || len(presented.Deliverables) != 0 {
					t.Fatalf("serialized lifecycle changed: %+v", presented)
				}
			}
			reloaded, ok, err := db.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
			if err != nil || !ok || reloaded.Revision != loaded.Revision || reloaded.Status != status || reloaded.LastError != task.LastError {
				t.Fatalf("read changed persisted lifecycle: %+v, %v", reloaded, err)
			}
		})
	}
	// Preserve the missing-session routing failure contract for agent execution.
	code := &pebblestore.ProjectTaskRecord{Agent: "coder", Status: "in_progress", RouterAlert: "Routing failed"}
	syncTaskSessionState(code, db)
	if code.Status != "failed" || code.LastError != code.RouterAlert {
		t.Fatalf("agent routing failure hidden: %+v", code)
	}
}
