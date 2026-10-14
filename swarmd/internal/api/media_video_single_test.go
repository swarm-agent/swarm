package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// A video request marked single is one clip. Regression: the direct media
// path dropped video_type, so the router planned its default two-scene story
// and an 8-second request made two paid generations joined into 16 seconds
// (swarm_request_media always asks for single).
func TestDirectVideoRequestMarkedSingleIsOneClip(t *testing.T) {
	server, db, principal := setupDirectMediaTestServer(t)
	seedOmniVideoCatalogRecord(t, server)
	project := &pebblestore.ProjectRecord{ID: "clip-project", AccountID: principal.AccountScopeID, Name: "Clips"}
	if err := db.PutProject(principal.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"title": "Bees", "prompt": "A swarm of bees rises over a meadow", "agent": "video", "model": "google:gemini-omni-video", "video_type": "single", "scenes_count": 1})
	req := httptest.NewRequest(http.MethodPost, "/v3/projects/"+project.ID+"/tasks", bytes.NewReader(body))
	req = req.WithContext(identity.ContextWithPrincipal(req.Context(), principal))
	rec := httptest.NewRecorder()
	server.handleProjects(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Task pebblestore.ProjectTaskRecord `json:"task"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	stored, found, err := db.GetProjectTask(principal.AccountScopeID, project.ID, response.Task.ID)
	if err != nil || !found {
		t.Fatalf("task not stored: %v", err)
	}
	if len(stored.Scenes) != 0 || stored.OutcomeType != "video_clip" {
		t.Fatalf("single video planned %d scenes, outcome %q; want one clip", len(stored.Scenes), stored.OutcomeType)
	}
}
