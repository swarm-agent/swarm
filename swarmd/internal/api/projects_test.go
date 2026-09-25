package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
)

func TestProjectsAPIEndpoints(t *testing.T) {
	// Purpose:
	// - Invariant: /v3/projects REST endpoints must provide authenticated, scope-checked CRUD
	//   for project records backed by the session store.
	// - Boundary/authority: Server.handleProjects in swarmd/internal/api/projects.go.
	// - Threat/regression: Unauthorized access, missing payload validation, or broken CRUD
	//   would prevent users from managing multi-workspace projects.

	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ss := store.NewSessionStore(db)
	el, err := store.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{sessions: sessionruntime.NewService(ss, el)}
	h := s.apiMux()

	call := func(method, path, body string, scopes []string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, ProjectsPath+path, strings.NewReader(body))
		p := identity.Principal{Type: "user", UserID: "owner", AccountScopeID: "account"}
		ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, p)
		if len(scopes) > 0 {
			tokenRec := &store.ScopedTokenRecord{
				AccountScopeID: "account",
				UserID:         "owner",
				Scopes:         scopes,
			}
			ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, tokenRec)
		}
		r = r.WithContext(ctx)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// 1. GET /v3/projects empty
	w := call(http.MethodGet, "", "", []string{"sessions:read"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var listResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil {
		t.Fatal(err)
	}
	if listResp["count"].(float64) != 0 {
		t.Fatalf("expected 0 projects, got %v", listResp["count"])
	}

	// 2. POST /v3/projects create
	createBody := `{
		"name": "Swarm Platform",
		"description": "Core engine and tools",
		"workspaces": [
			{"path": "/path/to/swarm-go", "role": "primary_code", "label": "Daemon"},
			{"path": "/path/to/swarm-social", "role": "auxiliary", "label": "Social"}
		],
		"project_context": "# Swarm Platform\nUnified architecture"
	}`
	w = call(http.MethodPost, "", createBody, []string{"sessions:write"})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var createResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &createResp); err != nil {
		t.Fatal(err)
	}
	projObj := createResp["project"].(map[string]any)
	projID, _ := projObj["id"].(string)
	if projID == "" {
		t.Fatal("expected generated project ID")
	}

	// 3. GET /v3/projects/{id}
	w = call(http.MethodGet, "/"+projID, "", []string{"sessions:read"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var getResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &getResp); err != nil {
		t.Fatal(err)
	}
	fetchedProj := getResp["project"].(map[string]any)
	if fetchedProj["name"].(string) != "Swarm Platform" {
		t.Fatalf("expected name Swarm Platform, got %v", fetchedProj["name"])
	}

	// 4. PATCH /v3/projects/{id}
	patchBody := `{
		"name": "Swarm Platform V3",
		"active_task_ids": ["task_101"]
	}`
	w = call(http.MethodPatch, "/"+projID, patchBody, []string{"sessions:write"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on patch, got %d: %s", w.Code, w.Body.String())
	}
	var patchResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &patchResp); err != nil {
		t.Fatal(err)
	}
	patchedProj := patchResp["project"].(map[string]any)
	if patchedProj["name"].(string) != "Swarm Platform V3" {
		t.Fatalf("expected name Swarm Platform V3, got %v", patchedProj["name"])
	}

	// 5. Context synthesis: POST /v3/projects/synthesize-context
	synthBody := `{
		"name": "Platform AI",
		"workspaces": ["/path/to/repo"]
	}`
	w = call(http.MethodPost, "/synthesize-context", synthBody, []string{"sessions:read"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on synthesize-context, got %d: %s", w.Code, w.Body.String())
	}
	var synthResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &synthResp); err != nil {
		t.Fatal(err)
	}
	if ctxStr, _ := synthResp["project_context"].(string); !strings.Contains(ctxStr, "Platform AI") {
		t.Fatalf("expected project_context to contain project name, got %q", ctxStr)
	}

	// 6. Project Tasks: POST /v3/projects/{id}/tasks
	taskCreateBody := `{
		"title": "Make 3 Video Clips",
		"description": "Generate video teasers",
		"agent": "video",
		"worker_name": "@Video Swarm",
		"pipeline_stages": ["Design", "Generate", "Deliver"]
	}`
	w = call(http.MethodPost, "/"+projID+"/tasks", taskCreateBody, []string{"sessions:write"})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 on task create, got %d: %s", w.Code, w.Body.String())
	}
	var taskCreateResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &taskCreateResp); err != nil {
		t.Fatal(err)
	}
	taskObj := taskCreateResp["task"].(map[string]any)
	taskID := taskObj["id"].(string)
	if taskID == "" {
		t.Fatal("expected task ID")
	}

	// 7. Project Tasks: GET /v3/projects/{id}/tasks
	w = call(http.MethodGet, "/"+projID+"/tasks", "", []string{"sessions:read"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on list tasks, got %d: %s", w.Code, w.Body.String())
	}
	var listTasksResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &listTasksResp); err != nil {
		t.Fatal(err)
	}
	if listTasksResp["count"].(float64) != 1 {
		t.Fatalf("expected 1 task in project, got %v", listTasksResp["count"])
	}

	// 8. Project Task: PATCH /v3/projects/{id}/tasks/{taskId}
	patchTaskBody := `{"status": "needs_review", "current_stage_index": 2}`
	w = call(http.MethodPatch, "/"+projID+"/tasks/"+taskID, patchTaskBody, []string{"sessions:write"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on task patch, got %d: %s", w.Code, w.Body.String())
	}

	// 8b. Project Task: POST /v3/projects/{id}/tasks/{taskId}/approve
	w = call(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", "", []string{"sessions:write"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on task approve, got %d: %s", w.Code, w.Body.String())
	}
	var approveResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &approveResp); err != nil {
		t.Fatal(err)
	}
	approvedTask := approveResp["task"].(map[string]any)
	if approvedTask["status"] != "in_progress" {
		t.Fatalf("expected approved task status in_progress, got %v", approvedTask["status"])
	}

	// 9. Project Task: DELETE /v3/projects/{id}/tasks/{taskId}
	w = call(http.MethodDelete, "/"+projID+"/tasks/"+taskID, "", []string{"sessions:write"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on task delete, got %d: %s", w.Code, w.Body.String())
	}

	// 10. DELETE /v3/projects/{id}
	w = call(http.MethodDelete, "/"+projID, "", []string{"sessions:write"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on delete, got %d: %s", w.Code, w.Body.String())
	}

	// 11. Verify 404 after delete
	w = call(http.MethodGet, "/"+projID, "", []string{"sessions:read"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d: %s", w.Code, w.Body.String())
	}
}
