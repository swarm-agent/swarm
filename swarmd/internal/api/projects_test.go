package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	if approvedTask["status"] != "needs_review" && approvedTask["status"] != "in_progress" {
		t.Fatalf("expected approved media task status needs_review or in_progress, got %v", approvedTask["status"])
	}
	delivs, ok := approvedTask["deliverables"].([]any)
	if !ok || len(delivs) == 0 {
		t.Fatalf("expected deliverables populated on approved media task, got %v", approvedTask["deliverables"])
	}

	// 8c. Project Task: POST /v3/projects/{id}/tasks/{taskId}/refine (User feedback refinement)
	refineBody := `{"feedback": "keep all changes only in web workspace, do not touch daemon"}`
	w = call(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/refine", refineBody, []string{"sessions:write"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on task refine, got %d: %s", w.Code, w.Body.String())
	}
	var refineResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &refineResp); err != nil {
		t.Fatal(err)
	}
	refinedTask := refineResp["task"].(map[string]any)
	if refinedTask["status"] != "pending_approval" {
		t.Fatalf("expected refined task status pending_approval, got %v", refinedTask["status"])
	}
	if rev, ok := refinedTask["revision"].(float64); !ok || rev < 2 {
		t.Fatalf("expected task revision >= 2, got %v", refinedTask["revision"])
	}

	// 8d. Project Task: POST /v3/projects/{id}/tasks/{taskId}/refine (Error recovery re-plan)
	errorRefineBody := `{"error_summary": "TS2322: Type 'string' is not assignable to type 'number'"}`
	w = call(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/refine", errorRefineBody, []string{"sessions:write"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on error refine, got %d: %s", w.Code, w.Body.String())
	}
	var errRefineResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &errRefineResp); err != nil {
		t.Fatal(err)
	}
	errRefinedTask := errRefineResp["task"].(map[string]any)
	if errRefinedTask["last_error"] != "TS2322: Type 'string' is not assignable to type 'number'" {
		t.Fatalf("expected last_error to be recorded, got %v", errRefinedTask["last_error"])
	}
	if rev, ok := errRefinedTask["revision"].(float64); !ok || rev < 3 {
		t.Fatalf("expected task revision >= 3, got %v", errRefinedTask["revision"])
	}

	// 8e. Project Task: Agent Task (Coder) creates a valid V3 session with compiled agent_profile
	coderTaskBody := `{
		"title": "Fix memory leak in pebble iterator",
		"description": "Close iterators on error",
		"agent": "coder"
	}`
	w = call(http.MethodPost, "/"+projID+"/tasks", coderTaskBody, []string{"sessions:write"})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 on coder task create, got %d: %s", w.Code, w.Body.String())
	}
	var coderTaskResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &coderTaskResp); err != nil {
		t.Fatal(err)
	}
	coderTask := coderTaskResp["task"].(map[string]any)
	coderSessID, _ := coderTask["session_id"].(string)
	if coderSessID == "" {
		t.Fatalf("expected session_id to be created for agent task")
	}
	sessSnap, found, err := ss.GetSession(coderSessID)
	if err != nil || !found {
		t.Fatalf("expected session %s to exist in store: %v", coderSessID, err)
	}
	if sessSnap.Metadata["agent_profile"] == nil {
		t.Fatalf("expected session metadata to contain agent_profile")
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

func TestDirectMediaTaskLifecycle(t *testing.T) {
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

	// 1. Create project
	w := call(http.MethodPost, "", `{"name":"Media Test Project","workspaces":[{"path":"/tmp/ws","role":"primary_code"}]}`, []string{"sessions:write"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create project: %d", w.Code)
	}
	var projResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &projResp)
	projID := projResp["project"].(map[string]any)["id"].(string)

	// 2. Propose 3 image variants (pending_approval)
	imgTaskBody := `{
		"title": "Generate Panda Images",
		"description": "make 3 cute panda images",
		"agent": "image",
		"aspect_ratio": "16:9",
		"variant_count": 3
	}`
	w = call(http.MethodPost, "/"+projID+"/tasks", imgTaskBody, []string{"sessions:write"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create image task: %d %s", w.Code, w.Body.String())
	}
	var taskResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &taskResp)
	taskObj := taskResp["task"].(map[string]any)
	taskID := taskObj["id"].(string)

	if taskObj["status"] != "pending_approval" {
		t.Fatalf("expected pending_approval status, got %v", taskObj["status"])
	}

	delivs, ok := taskObj["deliverables"].([]any)
	if !ok || len(delivs) != 3 {
		t.Fatalf("expected 3 deliverable slots in pending status, got %v", delivs)
	}
	for i, d := range delivs {
		dm := d.(map[string]any)
		if dm["status"] != "pending" {
			t.Fatalf("expected deliverable slot %d to have status pending, got %v", i+1, dm["status"])
		}
	}

	// 3. Approve task: should transition task to in_progress and deliverables to generating
	w = call(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", "", []string{"sessions:write"})
	if w.Code != http.StatusOK {
		t.Fatalf("approve task: %d %s", w.Code, w.Body.String())
	}
	var approveResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &approveResp)
	approvedTask := approveResp["task"].(map[string]any)

	appDelivs := approvedTask["deliverables"].([]any)
	if len(appDelivs) != 3 {
		t.Fatalf("expected 3 deliverables on approved task, got %d", len(appDelivs))
	}
	firstDeliv := appDelivs[0].(map[string]any)
	if firstDeliv["status"] != "generating" && firstDeliv["status"] != "ready" {
		t.Fatalf("expected first deliverable to be generating or ready, got %v", firstDeliv["status"])
	}

	// 4. Wait for background generation to complete all 3 deliverables
	deadline := time.Now().Add(5 * time.Second)
	var finalTask map[string]any
	for time.Now().Before(deadline) {
		time.Sleep(150 * time.Millisecond)
		w = call(http.MethodGet, "/"+projID+"/tasks/"+taskID, "", []string{"sessions:read"})
		if w.Code == http.StatusOK {
			var getResp map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &getResp)
			finalTask = getResp["task"].(map[string]any)
			if finalTask["status"] == "needs_review" {
				break
			}
		}
	}

	if finalTask == nil || finalTask["status"] != "needs_review" {
		t.Fatalf("expected task to transition to needs_review after background generation, got %v", finalTask)
	}

	finalDelivs := finalTask["deliverables"].([]any)
	if len(finalDelivs) != 3 {
		t.Fatalf("expected 3 final deliverables, got %d", len(finalDelivs))
	}
	for i, d := range finalDelivs {
		dm := d.(map[string]any)
		if dm["status"] != "ready" {
			t.Fatalf("expected deliverable %d to be ready, got %v", i+1, dm["status"])
		}
		mediaURL, _ := dm["media_url"].(string)
		if !strings.HasPrefix(mediaURL, "data:image/") {
			t.Fatalf("expected deliverable %d to have valid image data URL, got %q", i+1, mediaURL)
		}
	}
}
