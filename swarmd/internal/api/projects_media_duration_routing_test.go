package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: project proposal routing must not invent per-scene duration before
// validateProjectMediaTaskSettings checks the configured model. This HTTP/store
// layer catches the router default that service-only omission tests cannot see.
// Explicit unsupported duration must still reject without creating a task.
func TestProjectVideoProposalPreservesOmittedSceneDuration(t *testing.T) {
	server, db, principal := setupDirectMediaTestServer(t)
	seedOmniVideoCatalogRecord(t, server)
	project := &pebblestore.ProjectRecord{ID: "duration-project", AccountID: principal.AccountScopeID, Name: "Duration"}
	if err := db.PutProject(principal.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	for _, explicit := range []bool{false, true} {
		payload := map[string]any{"title": "Video duration", "prompt": "A quiet landscape", "agent": "video", "model": "google:gemini-omni-video"}
		if explicit {
			payload["duration_seconds"] = 4
		}
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v3/projects/"+project.ID+"/tasks", bytes.NewReader(body))
		req = req.WithContext(identity.ContextWithPrincipal(req.Context(), principal))
		rec := httptest.NewRecorder()
		server.handleProjects(rec, req)
		if explicit {
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "does not accept duration selection") {
				t.Fatalf("explicit unsupported duration: %d %s", rec.Code, rec.Body.String())
			}
			tasks, err := db.ListProjectTasksByArchive(principal.AccountScopeID, project.ID, false)
			if err != nil || len(tasks) != 1 {
				t.Fatalf("rejected duration created a task: count=%d err=%v", len(tasks), err)
			}
			continue
		}
		if rec.Code != http.StatusCreated {
			t.Fatalf("omitted duration: %d %s", rec.Code, rec.Body.String())
		}
		var response struct {
			Task pebblestore.ProjectTaskRecord `json:"task"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		stored, found, err := db.GetProjectTask(principal.AccountScopeID, project.ID, response.Task.ID)
		if err != nil || !found || stored.DurationSeconds != 0 || len(stored.Scenes) < 2 {
			t.Fatalf("lost omitted story settings: %+v, %v", stored, err)
		}
		for _, scene := range stored.Scenes {
			if scene.DurationSec != 0 {
				t.Fatalf("router invented scene duration: %+v", scene)
			}
		}
	}
}
