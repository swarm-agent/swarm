package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
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

	// 8f. Project Task: POST /v3/projects/{id}/tasks/{taskId}/reopen
	w = call(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/reopen", `{"feedback":"adjust scope to include auth tests"}`, []string{"sessions:write"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on task reopen, got %d: %s", w.Code, w.Body.String())
	}
	var reopenResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &reopenResp); err != nil {
		t.Fatal(err)
	}
	reopenedTask := reopenResp["task"].(map[string]any)
	if reopenedTask["status"] != "in_progress" {
		t.Fatalf("expected reopened task status in_progress, got %v", reopenedTask["status"])
	}

	// 8g. Project Task: POST /v3/projects/{id}/tasks/{taskId}/complete
	w = call(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/complete", "", []string{"sessions:write"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on task complete, got %d: %s", w.Code, w.Body.String())
	}
	var completeResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &completeResp); err != nil {
		t.Fatal(err)
	}
	completedTask := completeResp["task"].(map[string]any)
	if completedTask["status"] != "completed" {
		t.Fatalf("expected completed task status completed, got %v", completedTask["status"])
	}

	// 9. Project Task: DELETE /v3/projects/{id}/tasks/{taskId}
	w = call(http.MethodDelete, "/"+projID+"/tasks/"+taskID, "", []string{"sessions:write"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on task delete, got %d: %s", w.Code, w.Body.String())
	}

	// 9b. Clear orchestrator context: POST /v3/projects/{id}/orchestrator:clear-context
	origSessionID := "test_orig_orchestrator_sess"
	now := time.Now().UnixMilli()
	_ = ss.CreateSession(store.SessionSnapshot{
		ID:             origSessionID,
		UserID:         "owner",
		AccountScopeID: "account",
		Title:          "Project Orchestrator: Test",
		WorkspacePath:  t.TempDir(),
		Mode:           "auto",
		CreatedAt:      now,
		UpdatedAt:      now,
	})
	_ = ss.PutProject("account", &store.ProjectRecord{
		ID:               projID,
		AccountID:        "account",
		Name:             "Updated Platform",
		PrimarySessionID: origSessionID,
		CreatedAt:        now,
		UpdatedAt:        now,
	})

	w = call(http.MethodPost, "/"+projID+"/orchestrator:clear-context", "", []string{"sessions:write", "projects:write"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on clear orchestrator context, got %d: %s", w.Code, w.Body.String())
	}
	var clearResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &clearResp); err != nil {
		t.Fatal(err)
	}
	if clearResp["ok"] != true {
		t.Fatalf("expected ok=true, got %v", clearResp["ok"])
	}
	newSessID, _ := clearResp["session_id"].(string)
	if newSessID == "" || newSessID == origSessionID {
		t.Fatalf("expected fresh session_id, got %q (orig: %q)", newSessID, origSessionID)
	}
	if prevSessID, _ := clearResp["previous_session_id"].(string); prevSessID != origSessionID {
		t.Fatalf("expected previous_session_id %q, got %q", origSessionID, prevSessID)
	}
	pRec, pFound, pErr := ss.GetProject("account", projID)
	if pErr != nil || !pFound || pRec.PrimarySessionID != newSessID {
		t.Fatalf("expected project primary_session_id updated to %q, got %+v", newSessID, pRec)
	}
	newSess, newFound, newErr := ss.GetSession(newSessID)
	if newErr != nil || !newFound {
		t.Fatalf("expected new orchestrator session in store, got found=%v, err=%v", newFound, newErr)
	}
	if newSess.Metadata["agent_profile"] == nil {
		t.Fatal("expected new orchestrator session to contain stored agent_profile in metadata")
	}
	if newSess.Metadata["agent_name"] != "system-orchestrator" {
		t.Fatalf("expected agent_name system-orchestrator, got %v", newSess.Metadata["agent_name"])
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
		"variant_count": 3,
		"model": "imagen-3.0-generate-002"
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
	if taskObj["model"] != "imagen-3.0-generate-002" {
		t.Fatalf("expected task model imagen-3.0-generate-002, got %v", taskObj["model"])
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

	// 10. POST /v3/projects/{id}/media (Upload media to project)
	mediaUploadBody := `{
		"title": "architecture_diagram.png",
		"media_type": "image/png",
		"kind": "image",
		"url": "data:image/png;base64,mockupload",
		"size_bytes": 2048
	}`
	w = call(http.MethodPost, "/"+projID+"/media", mediaUploadBody, []string{"sessions:write"})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 for media upload, got %d: %s", w.Code, w.Body.String())
	}
	var mediaResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &mediaResp); err != nil {
		t.Fatal(err)
	}
	createdMedia := mediaResp["media"].(map[string]any)
	mediaID := createdMedia["id"].(string)
	if mediaID == "" {
		t.Fatal("expected media ID to be generated")
	}

	// 11. GET /v3/projects/{id}/media
	w = call(http.MethodGet, "/"+projID+"/media", "", []string{"sessions:read"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for media list, got %d: %s", w.Code, w.Body.String())
	}
	var mediaListResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &mediaListResp); err != nil {
		t.Fatal(err)
	}
	if mediaListResp["count"].(float64) != 1 {
		t.Fatalf("expected 1 uploaded media, got %v", mediaListResp["count"])
	}

	// 12. POST /v3/projects/{id}/tasks with attached_media
	taskWithAttachedMedia := `{
		"title": "Iterate on architecture diagram",
		"prompt": "make 2 iterations of this diagram",
		"variant_count": 2,
		"attached_media": [
			{
				"id": "` + mediaID + `",
				"title": "architecture_diagram.png",
				"kind": "image",
				"media_type": "image/png",
				"url": "data:image/png;base64,mockupload"
			}
		]
	}`
	w = call(http.MethodPost, "/"+projID+"/tasks", taskWithAttachedMedia, []string{"sessions:write"})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 for task with attached media, got %d: %s", w.Code, w.Body.String())
	}
	var attachedTaskResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &attachedTaskResp); err != nil {
		t.Fatal(err)
	}
	attachedTaskObj := attachedTaskResp["task"].(map[string]any)
	attachedMediaSlice := attachedTaskObj["attached_media"].([]any)
	if len(attachedMediaSlice) != 1 {
		t.Fatalf("expected 1 attached media on created task, got %d", len(attachedMediaSlice))
	}

	// 13. DELETE /v3/projects/{id}/media/{media_id} (Remove uploaded media)
	w = call(http.MethodDelete, "/"+projID+"/media/"+mediaID, "", []string{"sessions:write"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for media delete, got %d: %s", w.Code, w.Body.String())
	}
	var deleteMediaResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &deleteMediaResp); err != nil {
		t.Fatal(err)
	}
	if deleteMediaResp["removed"] != true {
		t.Fatalf("expected removed=true, got %v", deleteMediaResp["removed"])
	}
	remainingMedia := deleteMediaResp["uploaded_media"].([]any)
	if len(remainingMedia) != 0 {
		t.Fatalf("expected 0 media remaining after delete, got %d", len(remainingMedia))
	}

	// 14. Fine-tuning image task execution via quick route payload with auto_approve
	fineTuneTaskBody := `{
		"title": "Fine-tune avatar image",
		"prompt": "Change lighting to warm sunset and make accents neon gold",
		"intent": "image",
		"auto_approve": true,
		"variant_count": 1,
		"attached_media": [
			{
				"id": "avatar_orig",
				"title": "cyber_avatar.png",
				"kind": "image",
				"media_type": "image/png",
				"url": "data:image/png;base64,mockavatar"
			}
		]
	}`
	w = call(http.MethodPost, "/"+projID+"/tasks", fineTuneTaskBody, []string{"sessions:write"})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 for fine-tune task, got %d: %s", w.Code, w.Body.String())
	}
	var fineTuneResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &fineTuneResp); err != nil {
		t.Fatal(err)
	}
	fineTuneTaskObj := fineTuneResp["task"].(map[string]any)
	fineTuneTaskID := fineTuneTaskObj["id"].(string)

	// Wait briefly for asynchronous execution
	var completedFineTune map[string]any
	for wait := 0; wait < 20; wait++ {
		time.Sleep(100 * time.Millisecond)
		w = call(http.MethodGet, "/"+projID+"/tasks/"+fineTuneTaskID, "", []string{"sessions:read"})
		if w.Code == http.StatusOK {
			var resp map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err == nil {
				taskObj := resp["task"].(map[string]any)
				if taskObj["status"] == "needs_review" {
					completedFineTune = taskObj
					break
				}
			}
		}
	}
	if completedFineTune == nil {
		t.Fatal("expected fine-tune task to complete with status needs_review")
	}
	fineTuneDelivs := completedFineTune["deliverables"].([]any)
	if len(fineTuneDelivs) != 1 {
		t.Fatalf("expected 1 fine-tuned deliverable, got %d", len(fineTuneDelivs))
	}
	ftDeliv := fineTuneDelivs[0].(map[string]any)
	if ftDeliv["status"] != "ready" {
		t.Fatalf("expected fine-tune deliverable ready, got %v", ftDeliv["status"])
	}
	if ftDeliv["parent_deliverable_id"] != "avatar_orig" {
		t.Fatalf("expected parent_deliverable_id 'avatar_orig', got %v", ftDeliv["parent_deliverable_id"])
	}
	if ftDeliv["source_media_ref"] != "avatar_orig" {
		t.Fatalf("expected source_media_ref 'avatar_orig', got %v", ftDeliv["source_media_ref"])
	}
	ftWhatDidDo := completedFineTune["what_did_do"].([]any)
	hasBaseRef := false
	for _, step := range ftWhatDidDo {
		if strings.Contains(step.(string), "cyber_avatar.png") {
			hasBaseRef = true
			break
		}
	}
	if !hasBaseRef {
		t.Fatalf("expected what_did_do to reference base image cyber_avatar.png, got: %v", ftWhatDidDo)
	}

	// 15. Video continuation task execution via quick route payload with auto_approve
	videoContinuationBody := `{
		"title": "Continue flight scene",
		"prompt": "Continue this video with next scene transitioning into orbital sunrise",
		"intent": "video",
		"auto_approve": true,
		"soundtrack": "Cosmic Synth Horizon",
		"attached_media": [
			{
				"id": "vid_scene_1",
				"title": "takeoff_scene.mp4",
				"kind": "video",
				"media_type": "video/mp4",
				"url": "data:video/mp4;base64,mockvid"
			}
		]
	}`
	w = call(http.MethodPost, "/"+projID+"/tasks", videoContinuationBody, []string{"sessions:write"})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 for video continuation task, got %d: %s", w.Code, w.Body.String())
	}
	var vidContResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &vidContResp); err != nil {
		t.Fatal(err)
	}
	vidContTaskObj := vidContResp["task"].(map[string]any)
	vidContTaskID := vidContTaskObj["id"].(string)

	var completedVidCont map[string]any
	for wait := 0; wait < 30; wait++ {
		time.Sleep(100 * time.Millisecond)
		w = call(http.MethodGet, "/"+projID+"/tasks/"+vidContTaskID, "", []string{"sessions:read"})
		if w.Code == http.StatusOK {
			var resp map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err == nil {
				taskObj := resp["task"].(map[string]any)
				if taskObj["status"] == "needs_review" {
					completedVidCont = taskObj
					break
				}
			}
		}
	}
	if completedVidCont == nil {
		t.Fatal("expected video continuation task to complete with status needs_review")
	}
	vidDelivs := completedVidCont["deliverables"].([]any)
	if len(vidDelivs) != 1 {
		t.Fatalf("expected 1 video deliverable, got %d", len(vidDelivs))
	}
	vd := vidDelivs[0].(map[string]any)
	if vd["status"] != "ready" || vd["kind"] != "video" {
		t.Fatalf("expected ready video deliverable, got status=%v kind=%v", vd["status"], vd["kind"])
	}
	if !strings.Contains(vd["title"].(string), "Continued from") && !strings.Contains(vd["title"].(string), "Scene 2") {
		t.Fatalf("expected deliverable title to denote continuation, got %q", vd["title"])
	}

	// 16. Single video clip task with resolution & model (8s duration)
	singleVidBody := `{
		"title": "Single Rocket Launch Shot",
		"prompt": "Cinematic 8s slow-mo shot of rocket ignition",
		"intent": "video",
		"variant_count": 1,
		"aspect_ratio": "16:9",
		"resolution": "1080p",
		"duration_seconds": 8,
		"model": "veo-3.1-generate-preview",
		"auto_approve": true
	}`
	w = call(http.MethodPost, "/"+projID+"/tasks", singleVidBody, []string{"sessions:write"})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 for single video task, got %d: %s", w.Code, w.Body.String())
	}
	var singleVidResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &singleVidResp)
	singleVidTaskID := singleVidResp["task"].(map[string]any)["id"].(string)

	var completedSingleVid map[string]any
	for wait := 0; wait < 30; wait++ {
		time.Sleep(100 * time.Millisecond)
		w = call(http.MethodGet, "/"+projID+"/tasks/"+singleVidTaskID, "", []string{"sessions:read"})
		if w.Code == http.StatusOK {
			var resp map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err == nil {
				taskObj := resp["task"].(map[string]any)
				if taskObj["status"] == "needs_review" {
					completedSingleVid = taskObj
					break
				}
			}
		}
	}
	if completedSingleVid == nil {
		t.Fatal("expected single video task to complete with status needs_review")
	}
	svDelivs := completedSingleVid["deliverables"].([]any)
	if len(svDelivs) != 1 {
		t.Fatalf("expected 1 deliverable for single video, got %d", len(svDelivs))
	}
	svd := svDelivs[0].(map[string]any)
	if !strings.Contains(svd["title"].(string), "Single Video") {
		t.Fatalf("expected single video title, got %q", svd["title"])
	}
	if svd["duration"] != "8s" {
		t.Fatalf("expected single video duration to be 8s, got %v", svd["duration"])
	}
	if !strings.Contains(svd["description"].(string), "8s") {
		t.Fatalf("expected single video description to contain 8s, got %v", svd["description"])
	}

	// 17. Sound / Audio track task
	soundBody := `{
		"title": "Launch Theme Soundtrack",
		"prompt": "Driving synthwave with pulsing bassline 120 BPM",
		"intent": "sound",
		"duration_seconds": 30,
		"model": "lyria-3.5",
		"auto_approve": true
	}`
	w = call(http.MethodPost, "/"+projID+"/tasks", soundBody, []string{"sessions:write"})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 for sound task, got %d: %s", w.Code, w.Body.String())
	}
	var soundResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &soundResp)
	soundTaskID := soundResp["task"].(map[string]any)["id"].(string)

	var completedSound map[string]any
	for wait := 0; wait < 30; wait++ {
		time.Sleep(100 * time.Millisecond)
		w = call(http.MethodGet, "/"+projID+"/tasks/"+soundTaskID, "", []string{"sessions:read"})
		if w.Code == http.StatusOK {
			var resp map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err == nil {
				taskObj := resp["task"].(map[string]any)
				if taskObj["status"] == "needs_review" {
					completedSound = taskObj
					break
				}
			}
		}
	}
	if completedSound == nil {
		t.Fatal("expected sound task to complete with status needs_review")
	}
	sndDelivs := completedSound["deliverables"].([]any)
	if len(sndDelivs) != 1 {
		t.Fatalf("expected 1 audio deliverable, got %d", len(sndDelivs))
	}
	snd := sndDelivs[0].(map[string]any)
	if snd["kind"] != "audio" || !strings.Contains(snd["title"].(string), "Audio Clip") {
		t.Fatalf("expected audio deliverable, got kind=%v title=%v", snd["kind"], snd["title"])
	}

	// 18. Multi-part video task with optional EMPTY soundtrack
	noSoundVidBody := `{
		"title": "Silent Documentary Montage",
		"prompt": "Multi-scene montage of deep space nebulae",
		"intent": "video",
		"variant_count": 3,
		"aspect_ratio": "16:9",
		"resolution": "1080p",
		"model": "veo-3.1-generate-preview",
		"auto_approve": true
	}`
	w = call(http.MethodPost, "/"+projID+"/tasks", noSoundVidBody, []string{"sessions:write"})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 for no-soundtrack multi-part video task, got %d: %s", w.Code, w.Body.String())
	}
	var noSoundVidResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &noSoundVidResp)
	noSoundVidTaskID := noSoundVidResp["task"].(map[string]any)["id"].(string)

	var completedNoSoundVid map[string]any
	for wait := 0; wait < 30; wait++ {
		time.Sleep(100 * time.Millisecond)
		w = call(http.MethodGet, "/"+projID+"/tasks/"+noSoundVidTaskID, "", []string{"sessions:read"})
		if w.Code == http.StatusOK {
			var resp map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err == nil {
				taskObj := resp["task"].(map[string]any)
				if taskObj["status"] == "needs_review" {
					completedNoSoundVid = taskObj
					break
				}
			}
		}
	}
	if completedNoSoundVid == nil {
		t.Fatal("expected no-soundtrack multi-part video task to complete with status needs_review")
	}
	nsDelivs := completedNoSoundVid["deliverables"].([]any)
	if len(nsDelivs) != 1 {
		t.Fatalf("expected 1 deliverable, got %d", len(nsDelivs))
	}
	nsd := nsDelivs[0].(map[string]any)
	if strings.Contains(nsd["description"].(string), "Ambient Electronic Beats") {
		t.Fatalf("did not expect default Ambient Electronic Beats when soundtrack is omitted, got %q", nsd["description"])
	}
	if !strings.Contains(nsd["description"].(string), "no soundtrack clip") {
		t.Fatalf("expected deliverable description to state no soundtrack clip, got %q", nsd["description"])
	}

	// 19. Single Video Direct Task: strictly 1 clip, pending_approval, no worktree, no session, 8s model clip
	directSingleVidBody := `{
		"title": "Futuristic Neon Metropolis Drone Flyover",
		"prompt": "dramatic drone shot flying over a futuristic neon city at dusk with volumetric fog",
		"intent": "video",
		"video_type": "single",
		"aspect_ratio": "16:9",
		"resolution": "1080p",
		"model": "veo-3.1-generate-preview",
		"auto_approve": false
	}`
	w = call(http.MethodPost, "/"+projID+"/tasks", directSingleVidBody, []string{"sessions:write"})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 for single video task, got %d: %s", w.Code, w.Body.String())
	}
	var directSVResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &directSVResp)
	directSVTask := directSVResp["task"].(map[string]any)
	directSVTaskID := directSVTask["id"].(string)

	if directSVTask["status"] != "pending_approval" {
		t.Fatalf("expected pending_approval for single video task, got %v", directSVTask["status"])
	}
	if directSVTask["outcome_type"] != "video_clip" {
		t.Fatalf("expected outcome_type video_clip, got %v", directSVTask["outcome_type"])
	}
	if (directSVTask["worktree_branch"] != nil && directSVTask["worktree_branch"] != "") || (directSVTask["worktree_name"] != nil && directSVTask["worktree_name"] != "") {
		t.Fatalf("expected no worktree for single video direct task, got branch=%v name=%v", directSVTask["worktree_branch"], directSVTask["worktree_name"])
	}
	if directSVTask["session_id"] != "" && directSVTask["session_id"] != nil {
		t.Fatalf("expected no session_id for direct media task, got %v", directSVTask["session_id"])
	}
	if svScenes, ok := directSVTask["scenes"].([]any); ok && len(svScenes) > 1 {
		t.Fatalf("expected 0 or 1 scene for single video, got %d scenes", len(svScenes))
	}
	if directSVTask["soundtrack"] != "" && directSVTask["soundtrack"] != nil {
		t.Fatalf("expected empty soundtrack for single video, got %v", directSVTask["soundtrack"])
	}
	directSVDelivs := directSVTask["deliverables"].([]any)
	if len(directSVDelivs) != 1 {
		t.Fatalf("expected 1 deliverable slot, got %d", len(directSVDelivs))
	}
	dsvd0 := directSVDelivs[0].(map[string]any)
	if dsvd0["duration"] != "8s" {
		t.Fatalf("expected 8s duration for single video deliverable, got %v", dsvd0["duration"])
	}

	// Approve single video task and verify direct execution to needs_review
	w = call(http.MethodPost, "/"+projID+"/tasks/"+directSVTaskID+"/approve", "", []string{"sessions:write"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for single video approve, got %d: %s", w.Code, w.Body.String())
	}
	var directCompletedSV map[string]any
	for wait := 0; wait < 30; wait++ {
		time.Sleep(100 * time.Millisecond)
		w = call(http.MethodGet, "/"+projID+"/tasks/"+directSVTaskID, "", []string{"sessions:read"})
		if w.Code == http.StatusOK {
			var resp map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err == nil {
				taskObj := resp["task"].(map[string]any)
				if taskObj["status"] == "needs_review" {
					directCompletedSV = taskObj
					break
				}
			}
		}
	}
	if directCompletedSV == nil {
		t.Fatal("expected approved single video task to complete with status needs_review")
	}
	directCompletedDelivs := directCompletedSV["deliverables"].([]any)
	if len(directCompletedDelivs) != 1 {
		t.Fatalf("expected 1 deliverable on completed single video, got %d", len(directCompletedDelivs))
	}
	cdsvd := directCompletedDelivs[0].(map[string]any)
	if cdsvd["status"] != "ready" {
		t.Fatalf("expected deliverable status ready, got %v", cdsvd["status"])
	}
	if !strings.Contains(cdsvd["description"].(string), "Single video clip") {
		t.Fatalf("expected single video description, got %q", cdsvd["description"])
	}

	// 20. Single video task with enhance_prompt=true
	enhancedSVBody := `{
		"title": "Cyberpunk Neon Alleys",
		"prompt": "neon alley in rain with holographic signs",
		"intent": "video",
		"video_type": "single",
		"enhance_prompt": true,
		"aspect_ratio": "16:9",
		"resolution": "1080p",
		"model": "veo-3.1-generate-preview",
		"auto_approve": false
	}`
	w = call(http.MethodPost, "/"+projID+"/tasks", enhancedSVBody, []string{"sessions:write"})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 for enhanced single video task, got %d: %s", w.Code, w.Body.String())
	}
	var enhResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &enhResp)
	enhTask := enhResp["task"].(map[string]any)

	if enhTask["outcome_type"] != "video_clip" {
		t.Fatalf("expected outcome_type video_clip for enhanced single video, got %v", enhTask["outcome_type"])
	}
	if (enhTask["worktree_branch"] != nil && enhTask["worktree_branch"] != "") || (enhTask["worktree_name"] != nil && enhTask["worktree_name"] != "") {
		t.Fatalf("expected no worktree for enhanced single video, got branch=%v name=%v", enhTask["worktree_branch"], enhTask["worktree_name"])
	}
	if enhScenes, ok := enhTask["scenes"].([]any); ok && len(enhScenes) > 1 {
		t.Fatalf("expected at most 1 scene for enhanced single video, got %d scenes", len(enhScenes))
	}
	if enhTask["soundtrack"] != "" && enhTask["soundtrack"] != nil {
		t.Fatalf("expected no soundtrack for enhanced single video, got %v", enhTask["soundtrack"])
	}

	// 21. Image task with 2K resolution & explicit model
	image2KBody := `{
		"title": "Cosmic Nebular Swarm",
		"prompt": "hyper-detailed nebula with swirling star clusters",
		"intent": "image",
		"aspect_ratio": "16:9",
		"resolution": "2k",
		"model": "imagen-3.0-generate-002",
		"variant_count": 2,
		"auto_approve": false
	}`
	w = call(http.MethodPost, "/"+projID+"/tasks", image2KBody, []string{"sessions:write"})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 for 2k image task, got %d: %s", w.Code, w.Body.String())
	}
	var img2KResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &img2KResp)
	img2KTask := img2KResp["task"].(map[string]any)
	if img2KTask["resolution"] != "2k" {
		t.Fatalf("expected resolution 2k, got %v", img2KTask["resolution"])
	}
	if img2KTask["model"] != "imagen-3.0-generate-002" {
		t.Fatalf("expected model imagen-3.0-generate-002, got %v", img2KTask["model"])
	}
	// Approve and verify execution deliverables reflect 2k resolution
	img2KID := img2KTask["id"].(string)
	w = call(http.MethodPost, "/"+projID+"/tasks/"+img2KID+"/approve", "{}", []string{"sessions:write"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 approving 2k image task, got %d: %s", w.Code, w.Body.String())
	}
	var completedImgTask map[string]any
	for wait := 0; wait < 30; wait++ {
		time.Sleep(100 * time.Millisecond)
		w = call(http.MethodGet, "/"+projID+"/tasks", "", []string{"sessions:read"})
		var checkTasksResp map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &checkTasksResp)
		if tasksList, ok := checkTasksResp["tasks"].([]any); ok {
			for _, item := range tasksList {
				tm := item.(map[string]any)
				if tm["id"] == img2KID && tm["status"] == "needs_review" {
					completedImgTask = tm
					break
				}
			}
		}
		if completedImgTask != nil {
			break
		}
	}
	if completedImgTask == nil {
		t.Fatal("expected approved 2k image task to complete with status needs_review")
	}
	cImgDelivs := completedImgTask["deliverables"].([]any)
	if len(cImgDelivs) != 2 {
		t.Fatalf("expected 2 deliverables for 2k image task, got %d", len(cImgDelivs))
	}
	imgDeliv := cImgDelivs[0].(map[string]any)
	if !strings.Contains(imgDeliv["description"].(string), "2k") && !strings.Contains(imgDeliv["description"].(string), "2K") {
		t.Fatalf("expected deliverable description to mention 2k, got %q", imgDeliv["description"])
	}
}

func TestProjectCoderTask_PendingApproval_NoGitPollution(t *testing.T) {
	// Purpose:
	// - Invariant: A coder task in pending_approval state must NOT show unmerged commits or diffs
	//   from the root workspace, must have empty aspect_ratio, and must show a clean worktree branch name.
	// - Threat/regression: Pending tasks polluting the UI with root workspace unpushed commits and 16:9 image aspect ratio.
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

	dir := t.TempDir()
	proj := &store.ProjectRecord{
		Name: "Test Git Integration",
		Workspaces: []store.ProjectWorkspaceRef{
			{Path: dir, Label: "Main Repo", Role: "primary_code"},
		},
	}
	if err := ss.PutProject("account", proj); err != nil {
		t.Fatal(err)
	}

	taskBody := `{
		"title": "Confirm capability by committing an edit into AGENTS.md",
		"prompt": "Make an edit to AGENTS.md",
		"intent": "code",
		"aspect_ratio": "16:9",
		"workspace_path": "` + dir + `"
	}`
	w := call(http.MethodPost, "/"+proj.ID+"/tasks", taskBody, []string{"projects:write", "sessions:write"})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 created, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	taskObj := resp["task"].(map[string]any)

	if taskObj["agent"] != "coder" {
		t.Fatalf("expected agent coder, got %v", taskObj["agent"])
	}
	if taskObj["status"] != "pending_approval" {
		t.Fatalf("expected pending_approval, got %v", taskObj["status"])
	}
	if taskObj["aspect_ratio"] != nil && taskObj["aspect_ratio"] != "" {
		t.Fatalf("expected empty aspect_ratio on coder task, got %v", taskObj["aspect_ratio"])
	}
	if taskObj["unintegrated_commits"] != nil && taskObj["unintegrated_commits"].(float64) != 0 {
		t.Fatalf("expected 0 unintegrated_commits on pending task, got %v", taskObj["unintegrated_commits"])
	}
	if taskObj["diff_summary"] != nil && taskObj["diff_summary"] != "" {
		t.Fatalf("expected empty diff_summary on pending task, got %v", taskObj["diff_summary"])
	}
	if taskObj["is_dirty"] == true {
		t.Fatalf("expected is_dirty=false on pending task, got true")
	}

	branch := taskObj["worktree_branch"].(string)
	if branch == "main" || branch == "dev" || !strings.HasPrefix(branch, "agent/") {
		t.Fatalf("expected worktree branch starting with agent/, got %q", branch)
	}
	name := taskObj["worktree_name"].(string)
	if name == "" || name == "main" || name == "dev" {
		t.Fatalf("expected clean worktree_name, got %q", name)
	}

	// Verify list endpoint preserves clean state
	w = call(http.MethodGet, "/"+proj.ID+"/tasks", "", []string{"projects:read", "sessions:read"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 ok, got %d", w.Code)
	}
	var listResp map[string]any
	json.Unmarshal(w.Body.Bytes(), &listResp)
	tasksSlice := listResp["tasks"].([]any)
	if len(tasksSlice) != 1 {
		t.Fatalf("expected 1 task in list, got %d", len(tasksSlice))
	}
	first := tasksSlice[0].(map[string]any)
	if first["unintegrated_commits"] != nil && first["unintegrated_commits"].(float64) != 0 {
		t.Fatalf("expected 0 unintegrated_commits in list, got %v", first["unintegrated_commits"])
	}
	if first["is_dirty"] == true {
		t.Fatalf("expected is_dirty=false in list, got true")
	}
}

func TestProjectCoderTask_ZeroCommitsNotIntegrated(t *testing.T) {
	// Purpose:
	// - Invariant: A coder task with a clean worktree that has not created any commits
	//   must NOT be marked is_integrated=true, even if the session has messages.
	// - Boundary/authority: inspectTaskGitState in projects.go.
	// - Threat/regression: False-positive integration claims mislead users that work was integrated when no commits exist.

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
	_ = &Server{sessions: sessionruntime.NewService(ss, el)}

	dir := t.TempDir()
	cmdInit := exec.Command("git", "init", "-b", "dev", dir)
	if err := cmdInit.Run(); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("git", "-C", dir, "config", "user.email", "test@test.com").Run()
	_ = exec.Command("git", "-C", dir, "config", "user.name", "test").Run()
	_ = exec.Command("git", "-C", dir, "commit", "--allow-empty", "-m", "init").Run()
	outHead, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	baseCommit := strings.TrimSpace(string(outHead))

	wtDir := t.TempDir()
	cmdWt := exec.Command("git", "-C", dir, "worktree", "add", "-b", "agent/test-task", wtDir, "dev")
	if err := cmdWt.Run(); err != nil {
		t.Fatal(err)
	}

	task := store.ProjectTaskRecord{
		ID:             "task_zero_commits",
		ProjectID:      "proj_1",
		Status:         "completed",
		Agent:          "coder",
		WorkspacePath:  wtDir,
		WorktreeBranch: "agent/test-task",
		BaseBranch:     "dev",
		BaseCommit:     baseCommit,
	}

	gitState := inspectTaskGitState(task, ss)
	if gitState.isIntegrated {
		t.Fatalf("expected isIntegrated=false for worktree with zero commits, got true")
	}
	if gitState.unintegratedCommits != 0 {
		t.Fatalf("expected 0 unintegratedCommits, got %d", gitState.unintegratedCommits)
	}
	if gitState.actionNeeded != "" {
		t.Fatalf("expected empty actionNeeded for 0 commits clean worktree, got %q", gitState.actionNeeded)
	}
}

func TestProjectTask_IntegrateRejectsEmptyCommits(t *testing.T) {
	// Purpose:
	// - Invariant: POST /v3/projects/{id}/tasks/{taskId}/integrate rejects a task
	//   without a selected owned session and target, never faking integration.
	// - Boundary/authority: Server.handleProjects in projects.go.
	// - Threat/regression: Calling integrate on an empty task mutates database strings to claim integration occurred.

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

	dir := t.TempDir()
	cmdInit := exec.Command("git", "init", "-b", "dev", dir)
	_ = cmdInit.Run()
	_ = exec.Command("git", "-C", dir, "config", "user.email", "test@test.com").Run()
	_ = exec.Command("git", "-C", dir, "config", "user.name", "test").Run()
	_ = exec.Command("git", "-C", dir, "commit", "--allow-empty", "-m", "init").Run()
	outHead, _ := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	baseCommit := strings.TrimSpace(string(outHead))

	wtDir := t.TempDir()
	_ = exec.Command("git", "-C", dir, "worktree", "add", "-b", "agent/test-task-2", wtDir, "dev").Run()

	proj := &store.ProjectRecord{
		ID:   "proj_int_test",
		Name: "Test Integrate",
	}
	_ = ss.PutProject("account", proj)

	task := &store.ProjectTaskRecord{
		ID:             "task_no_commits",
		ProjectID:      proj.ID,
		Title:          "No Commits Task",
		Status:         "completed",
		Agent:          "coder",
		WorkspacePath:  wtDir,
		WorktreeBranch: "agent/test-task-2",
		BaseBranch:     "dev",
		BaseCommit:     baseCommit,
	}
	_ = ss.PutProjectTask("account", task)

	// Explicit selection cannot invent session lineage, even for a clean task.
	w := call(http.MethodPost, "/"+proj.ID+"/tasks/"+task.ID+"/integrate", `{"session_id":"missing","source_branch":"agent/test-task-2","target_branch":"dev"}`, []string{"projects:write", "sessions:write"})
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict for missing selected session, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "selection changed") {
		t.Fatalf("expected actionable selection error, got %s", w.Body.String())
	}

	// Verify task in DB was NOT modified to claim it was integrated
	fetched, found, _ := ss.GetProjectTask("account", proj.ID, task.ID)
	if !found {
		t.Fatal("task not found")
	}
	if fetched.IsIntegrated {
		t.Fatal("expected task.IsIntegrated to remain false")
	}
	if strings.Contains(fetched.ActionNeeded, "Integrated into") {
		t.Fatalf("expected actionNeeded not to claim integrated, got %q", fetched.ActionNeeded)
	}
}

func TestProjectCoderTask_FallbackAlertPopulatedWhenUnconfigured(t *testing.T) {
	// Purpose:
	// - Invariant: When system-agent model resolution fails or is unconfigured,
	//   the task MUST fall back to default Swarm model and MUST populate RouterAlert
	//   warning the user on the task card.
	// - Boundary/authority: deployProjectTaskExecution in projects.go.
	// - Threat/regression: Silent model fallbacks without warning user on task card.

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
		tokenRec := &store.ScopedTokenRecord{
			AccountScopeID: "account",
			UserID:         "owner",
			Scopes:         scopes,
		}
		ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, tokenRec)
		r = r.WithContext(ctx)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	proj := &store.ProjectRecord{
		ID:   "proj_alert_test",
		Name: "Test Alert",
	}
	_ = ss.PutProject("account", proj)

	taskBody := `{
		"title": "Fix memory leak in buffer pool",
		"prompt": "Fix buffer pool leak",
		"intent": "code"
	}`
	w := call(http.MethodPost, "/"+proj.ID+"/tasks", taskBody, []string{"projects:write", "sessions:write"})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	taskObj := resp["task"].(map[string]any)
	alert, _ := taskObj["router_alert"].(string)
	if alert == "" {
		t.Fatal("expected router_alert to be populated when model resolution falls back")
	}
	if !strings.Contains(alert, "fell back to Swarm default") {
		t.Fatalf("expected router_alert to mention fell back to Swarm default, got %q", alert)
	}
}

func TestProjectTask_SessionLifecycleSync(t *testing.T) {
	// Written Purpose:
	// - Product Requirement: An in-progress task must reflect the live session reality:
	//   when an agent session run completes (or reaches waiting_review), the task
	//   status must transition to needs_review, never directly to completed,
	//   ensuring user review and git integration gates are respected.
	// - Boundary/authority: syncTaskSessionState in projects.go.
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

	sessID := "sess_lifecycle_sync_1"
	now := time.Now().UnixMilli()
	sessSnap := store.SessionSnapshot{
		ID:             sessID,
		UserID:         "owner",
		AccountScopeID: "account",
		Title:          "Worker session",
		Mode:           "auto",
		CreatedAt:      now,
		UpdatedAt:      now,
		MessageCount:   2,
		Lifecycle: &store.SessionLifecycleSnapshot{
			SessionID: sessID,
			Active:    false, // run completed!
			Phase:     "completed",
		},
	}
	_, err = s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessID,
		UserID:          "owner",
		AccountScopeID:  "account",
		ClientRequestID: "create:" + sessID,
		IdempotencyKey:  "create:" + sessID,
		PayloadHash:     "create:" + sessID,
		RequestHash:     "create:" + sessID,
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session:         &sessSnap,
		NowUnixMs:       now,
	})
	if err != nil {
		t.Fatal(err)
	}

	task := &store.ProjectTaskRecord{
		ID:           "task_sync_1",
		ProjectID:    "proj_1",
		AccountID:    "account",
		Title:        "Implement feature",
		Agent:        "coder",
		Status:       "in_progress",
		SessionID:    sessID,
		IsIntegrated: false,
	}

	// Session is completed, but task is not integrated yet -> must transition to needs_review!
	syncTaskSessionState(task, ss)
	if task.Status != "needs_review" {
		t.Fatalf("expected task status to transition to needs_review, got %s", task.Status)
	}

	// If task is integrated -> transitions to completed
	task.IsIntegrated = true
	syncTaskSessionState(task, ss)
	if task.Status != "completed" {
		t.Fatalf("expected integrated task status to be completed, got %s", task.Status)
	}

	// If session is active -> stays in_progress
	activeLifecycle := &store.SessionLifecycleSnapshot{
		SessionID: sessID,
		Active:    true,
		Phase:     "running",
	}
	_, _ = s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessID,
		UserID:          "owner",
		AccountScopeID:  "account",
		ClientRequestID: "update:" + sessID,
		IdempotencyKey:  "update:" + sessID,
		PayloadHash:     "update:" + sessID,
		RequestHash:     "update:" + sessID,
		Kind:            sessionruntime.SessionMutationUpsertLifecycle,
		Lifecycle:       activeLifecycle,
		NowUnixMs:       now + 10,
	})
	task.Status = "in_progress"
	task.IsIntegrated = false
	syncTaskSessionState(task, ss)
	if task.Status != "in_progress" {
		t.Fatalf("expected active session task to remain in_progress, got %s", task.Status)
	}
}

func TestProjectTaskProgram_StandaloneExecutionAndRedeploy(t *testing.T) {
	// Written test purpose:
	// - Product requirement/invariant: Project tasks with embedded TaskPrograms must be deployable standalone,
	//   register durable TaskProgramRecords, hydrate live status to task queries, and support in-task job redeployment.
	// - Regression prevented: Prevents regressions where project tasks cannot run parallel multi-agent programs,
	//   or where conflicted/failed jobs cannot be redeployed within the same task.
	// - Boundary/authority: deployProjectTaskProgram and redeployTaskProgramJob in project_task_program.go.
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ss := store.NewSessionStore(db)
	s := &Server{sessions: sessionruntime.NewService(ss, nil)}

	principal := identity.Principal{Type: "user", UserID: "owner", AccountScopeID: "account"}
	proj := &store.ProjectRecord{
		ID:        "proj_tp_1",
		AccountID: "account",
		Name:      "Test Project",
	}
	_ = ss.PutProject("account", proj)

	taskProgram := &store.TaskProgramDefinition{
		ID: "prog_tp_1",
		Stages: []store.TaskProgramStageSpec{
			{ID: "stage_ui", DependencyEvidence: "UI components ready"},
			{ID: "stage_test", DependsOn: []string{"stage_ui"}, DependencyEvidence: "Components built"},
		},
		Jobs: []store.TaskProgramJobSpec{
			{ID: "job_navbar", StageID: "stage_ui", AgentType: "coder", Title: "Fix Navbar", MetaPrompt: "Align navbar", OwnedScope: []string{"web/navbar/**"}},
			{ID: "job_modal", StageID: "stage_ui", AgentType: "coder", Title: "Fix Modal", MetaPrompt: "Close button handler", OwnedScope: []string{"web/modal/**"}},
			{ID: "job_e2e", StageID: "stage_test", DependsOn: []string{"job_navbar", "job_modal"}, AgentType: "coder", Title: "E2E Tests", MetaPrompt: "Run test suite"},
		},
	}

	task := &store.ProjectTaskRecord{
		ID:          "task_tp_1",
		ProjectID:   proj.ID,
		AccountID:   "account",
		Title:       "Fix 2 UI Issues in Parallel",
		Agent:       "coder",
		Status:      "pending_approval",
		TaskProgram: taskProgram,
	}
	_ = ss.PutProjectTask("account", task)

	// 1. Deploy the task program standalone
	err = s.deployProjectTaskProgram(principal, proj, task)
	if err != nil {
		t.Fatalf("deployProjectTaskProgram failed: %v", err)
	}

	if task.Status != "in_progress" {
		t.Fatalf("expected task status in_progress, got %s", task.Status)
	}
	if task.SessionID == "" {
		t.Fatal("expected coordinator session to be allocated")
	}
	if task.TaskProgramID != "prog_tp_1" {
		t.Fatalf("expected task program id prog_tp_1, got %s", task.TaskProgramID)
	}

	// 2. Verify durable TaskProgramRecord was created in Pebble
	progRecord, ok, err := ss.GetTaskProgram(task.SessionID, task.TaskProgramID)
	if err != nil || !ok {
		t.Fatalf("expected task program record to exist in store: ok=%v, err=%v", ok, err)
	}
	if len(progRecord.Jobs) != 3 {
		t.Fatalf("expected 3 jobs in task program record, got %d", len(progRecord.Jobs))
	}
	if progRecord.ActiveStageID != "stage_ui" {
		t.Fatalf("expected active stage stage_ui, got %s", progRecord.ActiveStageID)
	}

	// 3. Verify status hydration
	freshTask := &store.ProjectTaskRecord{
		ID:            task.ID,
		SessionID:     task.SessionID,
		TaskProgramID: task.TaskProgramID,
	}
	hydrateTaskProgramStatus(freshTask, ss)
	if freshTask.TaskProgramStatus == nil {
		t.Fatal("expected hydrated TaskProgramStatus on task record")
	}
	if len(freshTask.TaskProgramStatus.Jobs) != 3 {
		t.Fatalf("expected 3 jobs in hydrated status, got %d", len(freshTask.TaskProgramStatus.Jobs))
	}

	// 4. Test redeploying a job
	err = s.redeployTaskProgramJob(principal, proj.ID, task.ID, "job_navbar", "Please resolve conflict with modal changes")
	if err != nil {
		t.Fatalf("redeployTaskProgramJob failed: %v", err)
	}

	// Verify attempt number incremented and history recorded
	updatedRecord, ok, _ := ss.GetTaskProgram(task.SessionID, task.TaskProgramID)
	if !ok {
		t.Fatal("expected updated task program record")
	}
	navbarJob := findJobRecord(updatedRecord.Jobs, "job_navbar")
	if navbarJob == nil {
		t.Fatal("expected navbar job in updated record")
	}
	if navbarJob.AttemptNumber != 2 {
		t.Fatalf("expected attempt number 2 on redeployed job, got %d", navbarJob.AttemptNumber)
	}
	if len(navbarJob.GenerationHistory) != 1 {
		t.Fatalf("expected 1 generation history entry, got %d", len(navbarJob.GenerationHistory))
	}
}

func TestProjectTaskProgram_SingleTaskLifecycleAndHydration(t *testing.T) {
	// Written test purpose:
	// - Product requirement/invariant: A single task with an attached single-job Task Program must deploy
	//   standalone through DeployProjectTask, maintain in_progress status while running, transition to
	//   needs_review upon completion, and hydrate TaskProgramStatus for task queries.
	// - Regression prevented: Prevents regressions where single-task Task Programs fail to deploy or
	//   fail to sync live TaskProgram status in task listings.
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}
	defer db.Close()

	ss := store.NewSessionStore(db)
	s := &Server{
		sessions: sessionruntime.NewService(ss, nil),
		runner:   &testMockRunService{},
	}

	accountID := "acct_single_tp"
	projID := "proj_single_tp_1"
	taskID := "task_single_tp_1"

	proj := &store.ProjectRecord{
		ID:        projID,
		AccountID: accountID,
		Name:      "Single TP Test",
	}
	if err := ss.PutProject(accountID, proj); err != nil {
		t.Fatal(err)
	}

	taskProg := &store.TaskProgramDefinition{
		ID: "prog_single_1",
		Stages: []store.TaskProgramStageSpec{
			{ID: "stage_core", DependencyEvidence: "Initial stage"},
		},
		Jobs: []store.TaskProgramJobSpec{
			{
				ID:                 "job_core",
				StageID:            "stage_core",
				AgentType:          "coder",
				Title:              "Core Backend Implementation",
				MetaPrompt:         "Implement core logic",
				Deliverable:        "core.go",
				AcceptanceCriteria: []string{"test passes"},
				DependencyEvidence: "None",
			},
		},
	}

	task := &store.ProjectTaskRecord{
		ID:          taskID,
		ProjectID:   projID,
		AccountID:   accountID,
		Title:       "Core Feature Delegation",
		Agent:       "coder",
		Status:      "queued",
		TaskProgram: taskProg,
	}
	if err := ss.PutProjectTask(accountID, task); err != nil {
		t.Fatal(err)
	}

	p := identity.Principal{Type: "user", UserID: "usr_single_tp", AccountScopeID: accountID}
	// 1. Deploy via DeployProjectTask
	if err := s.DeployProjectTask(context.Background(), p, projID, taskID); err != nil {
		t.Fatalf("DeployProjectTask failed: %v", err)
	}

	deployedTask, found, err := ss.GetProjectTask(accountID, projID, taskID)
	if err != nil || !found {
		t.Fatalf("failed retrieving deployed task: %v", err)
	}
	if deployedTask.Status != "in_progress" {
		t.Fatalf("expected status in_progress, got %s", deployedTask.Status)
	}
	if deployedTask.TaskProgramID != "prog_single_1" {
		t.Fatalf("expected task program id prog_single_1, got %s", deployedTask.TaskProgramID)
	}

	// 2. syncTaskSessionState while running
	syncTaskSessionState(deployedTask, ss)
	if deployedTask.Status != "in_progress" {
		t.Fatalf("expected status in_progress while program running, got %s", deployedTask.Status)
	}

	// 3. Complete the program in store and test syncTaskSessionState
	completedState := store.TaskProgramStateCompleted
	_, _, err = ss.TransitionTaskProgram(deployedTask.SessionID, deployedTask.TaskProgramID, store.TaskProgramTransition{
		ExpectedRevision: 1,
		MutationID:       "test_complete",
		State:            &completedState,
	})
	if err != nil {
		t.Fatalf("TransitionTaskProgram failed: %v", err)
	}

	syncTaskSessionState(deployedTask, ss)
	if deployedTask.Status != "needs_review" {
		t.Fatalf("expected status needs_review after program completion, got %s", deployedTask.Status)
	}
	if deployedTask.TaskProgramStatus == nil {
		t.Fatal("expected non-nil TaskProgramStatus on task")
	}
	if deployedTask.TaskProgramStatus.State != store.TaskProgramStateCompleted {
		t.Fatalf("expected TaskProgramStatus state completed, got %s", deployedTask.TaskProgramStatus.State)
	}
}

func TestProjectOrchestrator_ClearContext_ResolvesPlanModelAndValidProfile(t *testing.T) {
	// Purpose:
	// - Product invariant: /v3/projects/{id}/orchestrator:clear-context must provision a replacement
	//   session carrying the configured Swarm plan agent model (settings.Swarm.Plan), complete
	//   server metadata with stored agent_profile and swarm_v3_runtime_swarm_id, so execution does not
	//   fail with 'v3 session is missing stored agent profile' and stop triggers resolve target_swarm_id.
	// - Regression prevented: Prevents regressions where clearing context wipes the agent profile,
	//   causing immediate run failure, or ignores account plan model configuration.

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
	principal := identity.Principal{Type: "user", UserID: "owner", AccountScopeID: "account-test"}
	settingsStore := store.NewAgentModelSettingsStore(db)
	compactAssignment := store.AgentModelAssignment{Provider: "google", Model: "gemini-3.8-flash", Thinking: "low"}
	_, err = settingsStore.PutForAccount(store.AgentModelSettingsRecord{
		AccountScopeID: principal.AccountScopeID,
		Swarm: store.SwarmAgentModelAssignments{
			Action: store.AgentModelAssignment{Provider: "google", Model: "gemini-3.8-flash", Thinking: "low"},
			Plan:   store.AgentModelAssignment{Provider: "anthropic", Model: "claude-3-7-sonnet", Thinking: "high"},
		},
		SystemAgents: store.SystemAgentModelAssignments{
			Compact:  compactAssignment,
			Finder:   compactAssignment,
			Coder:    compactAssignment,
			Designer: compactAssignment,
			Router:   compactAssignment,
		},
	})
	if err != nil {
		t.Fatalf("put initial swarm settings: %v", err)
	}
	settingsSvc := agentmodelsettings.NewService(settingsStore)

	swarmStore := store.NewSwarmStore(db)
	_, _ = swarmStore.PutLocalNode(store.SwarmLocalNodeRecord{
		SwarmID: "swarm-local-node-123",
		Role:    "host",
	})

	agentsSvc := agentruntime.NewService(nil, nil)

	s := &Server{
		sessions:           sessionruntime.NewService(ss, el),
		agentModelSettings: settingsSvc,
		agents:             agentsSvc,
		swarmStore:         swarmStore,
	}
	h := s.apiMux()

	projID := "proj_orch_test_1"
	now := time.Now().UnixMilli()
	origSessionID := "sess_orch_orig"
	_ = ss.CreateSession(store.SessionSnapshot{
		ID:             origSessionID,
		UserID:         principal.UserID,
		AccountScopeID: principal.AccountScopeID,
		Title:          "Project Orchestrator: Swarm Platform",
		WorkspacePath:  t.TempDir(),
		Mode:           "auto",
		CreatedAt:      now,
		UpdatedAt:      now,
	})
	_ = ss.PutProject(principal.AccountScopeID, &store.ProjectRecord{
		ID:               projID,
		AccountID:        principal.AccountScopeID,
		Name:             "Swarm Platform",
		PrimarySessionID: origSessionID,
		CreatedAt:        now,
		UpdatedAt:        now,
	})

	req := httptest.NewRequest(http.MethodPost, ProjectsPath+"/"+projID+"/orchestrator:clear-context", nil)
	pCtx := context.WithValue(req.Context(), productPrincipalRequestContextKey, principal)
	tokenRec := &store.ScopedTokenRecord{
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
		Scopes:         []string{"sessions:write", "projects:write"},
	}
	pCtx = context.WithValue(pCtx, productScopedTokenRequestContextKey, tokenRec)
	req = req.WithContext(pCtx)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on clear-context, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	newSessionID, _ := resp["session_id"].(string)
	if newSessionID == "" || newSessionID == origSessionID {
		t.Fatalf("expected new session ID, got %q", newSessionID)
	}

	// Fetch new session from store and verify invariants
	newSess, ok, err := ss.GetSession(newSessionID)
	if err != nil || !ok {
		t.Fatalf("new orchestrator session not found in store: ok=%v, err=%v", ok, err)
	}

	// 1. Must contain stored agent profile to prevent 'v3 session is missing stored agent profile'
	profileRaw, hasProfile := newSess.Metadata["agent_profile"]
	if !hasProfile || profileRaw == nil {
		t.Fatal("new orchestrator session metadata missing stored agent_profile")
	}

	// 2. Must resolve Plan model (claude-3-7-sonnet) rather than Action or hardcoded fallback
	if newSess.Preference.Model != "claude-3-7-sonnet" {
		t.Fatalf("expected session preference model claude-3-7-sonnet, got %q", newSess.Preference.Model)
	}
	if newSess.Preference.Provider != "anthropic" {
		t.Fatalf("expected session preference provider anthropic, got %q", newSess.Preference.Provider)
	}

	// 3. Must populate swarm_v3_runtime_swarm_id for stop trigger resolution
	swarmID, _ := newSess.Metadata["swarm_v3_runtime_swarm_id"].(string)
	if swarmID != "swarm-local-node-123" {
		t.Fatalf("expected swarm_v3_runtime_swarm_id swarm-local-node-123, got %q", swarmID)
	}

	// 4. Verify resolveSessionV3EffectivePreference resolves the Plan model
	storedProfile, err := sessionV3AgentProfileFromMetadata(newSess.Metadata)
	if err != nil {
		t.Fatalf("sessionV3AgentProfileFromMetadata failed: %v", err)
	}
	effectivePref, err := resolveSessionV3EffectivePreference(newSess, storedProfile)
	if err != nil {
		t.Fatalf("resolveSessionV3EffectivePreference failed: %v", err)
	}
	if effectivePref.Model != "claude-3-7-sonnet" {
		t.Fatalf("expected effective model claude-3-7-sonnet, got %q", effectivePref.Model)
	}
}

func TestProjectsAPI_ApprovalIdempotency(t *testing.T) {
	// Written test purpose:
	// - Product requirement/invariant: Repeated approval of a task must be idempotent,
	//   returning status="already_approved" without launching duplicate executions or sessions.
	// - Regression prevented: Duplicated execution runs and goroutines when user or frontend double-clicks approve.

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

	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, ProjectsPath+path, strings.NewReader(body))
		p := identity.Principal{Type: "user", UserID: "owner", AccountScopeID: "account"}
		ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, p)
		tokenRec := &store.ScopedTokenRecord{
			AccountScopeID: "account",
			UserID:         "owner",
			Scopes:         []string{"sessions:write", "projects:write", "projects:read", "sessions:read"},
		}
		ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, tokenRec)
		r = r.WithContext(ctx)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// 1. Create project
	w := call(http.MethodPost, "", `{"name":"Idempotency Project","workspaces":[{"path":"/ws","role":"primary_code","label":"Code"}]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create project failed: %d: %s", w.Code, w.Body.String())
	}
	var projResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &projResp)
	projID := projResp["project"].(map[string]any)["id"].(string)

	// 2. Create pending coder task
	w = call(http.MethodPost, "/"+projID+"/tasks", `{"title":"Add feature","agent":"coder"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create task failed: %d: %s", w.Code, w.Body.String())
	}
	var taskResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &taskResp)
	taskID := taskResp["task"].(map[string]any)["id"].(string)

	// 3. First approve -> 200 approved
	w = call(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", "")
	if w.Code != http.StatusOK {
		t.Fatalf("first approve failed: %d: %s", w.Code, w.Body.String())
	}
	var appResp1 map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &appResp1)
	if appResp1["status"] != "approved" {
		t.Fatalf("expected approved status, got %v", appResp1["status"])
	}

	// 4. Second approve -> 200 already_approved
	w = call(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", "")
	if w.Code != http.StatusOK {
		t.Fatalf("second approve expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var appResp2 map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &appResp2)
	if appResp2["status"] != "already_approved" {
		t.Fatalf("expected already_approved status on repeated approval, got %v", appResp2["status"])
	}
}

func TestProjectsAPI_ExplicitCoderMediaKeywordsNoImageDeliverable(t *testing.T) {
	// Written test purpose:
	// - Product requirement/invariant: Proposing a task with explicit agent="coder"
	//   and media wording in prompt must produce a coder task with code PR deliverable,
	//   NOT image deliverables or media bundle.
	// - Threat/regression: The original bug where Orchestrator UI task was turned into image generation.

	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ss := store.NewSessionStore(db)
	el, _ := store.NewEventLog(db)
	s := &Server{sessions: sessionruntime.NewService(ss, el)}
	h := s.apiMux()

	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, ProjectsPath+path, strings.NewReader(body))
		p := identity.Principal{Type: "user", UserID: "owner", AccountScopeID: "account"}
		ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, p)
		tokenRec := &store.ScopedTokenRecord{
			AccountScopeID: "account",
			UserID:         "owner",
			Scopes:         []string{"sessions:write", "projects:write", "projects:read", "sessions:read"},
		}
		ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, tokenRec)
		r = r.WithContext(ctx)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	w := call(http.MethodPost, "", `{"name":"Media Keyword Test Project"}`)
	var projResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &projResp)
	projID := projResp["project"].(map[string]any)["id"].(string)

	w = call(http.MethodPost, "/"+projID+"/tasks", `{
		"title": "Add profile PNG upload button and allow selection from media",
		"prompt": "Allow profile PNG upload or selection from media, remove acct_* label, improve project layout",
		"agent": "coder"
	}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 on task create, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	task := resp["task"].(map[string]any)
	if task["agent"] != "coder" {
		t.Fatalf("expected agent coder, got %v", task["agent"])
	}
	if task["outcome_type"] != "code_pr" {
		t.Fatalf("expected outcome_type code_pr, got %v", task["outcome_type"])
	}
	delivs, _ := task["deliverables"].([]any)
	if len(delivs) == 0 {
		t.Fatal("expected deliverables")
	}
	for _, d := range delivs {
		dm := d.(map[string]any)
		if dm["kind"] == "image" || dm["kind"] == "video" || dm["kind"] == "audio" {
			t.Fatalf("coder task must not have media deliverable, got: %+v", dm)
		}
	}
}

func TestProjectsAPI_RejectIncompatibleTaskFields(t *testing.T) {
	// Written test purpose:
	// - Product requirement/invariant: Task-to-executor pipeline must reject incoherent
	//   combinations (e.g. coder with media_bundle, finder with code_pr) with an actionable 400 error.
	// - Threat/regression: Malformed tasks being saved or dispatched to wrong executors.

	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ss := store.NewSessionStore(db)
	el, _ := store.NewEventLog(db)
	s := &Server{sessions: sessionruntime.NewService(ss, el)}
	h := s.apiMux()

	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, ProjectsPath+path, strings.NewReader(body))
		p := identity.Principal{Type: "user", UserID: "owner", AccountScopeID: "account"}
		ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, p)
		tokenRec := &store.ScopedTokenRecord{
			AccountScopeID: "account",
			UserID:         "owner",
			Scopes:         []string{"sessions:write", "projects:write", "projects:read", "sessions:read"},
		}
		ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, tokenRec)
		r = r.WithContext(ctx)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	w := call(http.MethodPost, "", `{"name":"Incompatible Test Project"}`)
	var projResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &projResp)
	projID := projResp["project"].(map[string]any)["id"].(string)

	// Incompatible: coder with media_bundle
	w = call(http.MethodPost, "/"+projID+"/tasks", `{
		"title": "Bad combination",
		"agent": "coder",
		"outcome_type": "media_bundle"
	}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for coder with media_bundle, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "incoherent task contract") {
		t.Fatalf("expected incoherent contract message, got: %s", w.Body.String())
	}

	// Incompatible: finder with code_pr
	w = call(http.MethodPost, "/"+projID+"/tasks", `{
		"title": "Bad finder",
		"agent": "finder",
		"outcome_type": "code_pr"
	}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for finder with code_pr, got %d: %s", w.Code, w.Body.String())
	}
}

func TestProjectsAPI_ModelPreviewEndpoint(t *testing.T) {
	// Written test purpose:
	// - Product requirement/invariant: GET /v3/projects/{id}/tasks/{taskId}/model-preview must return
	//   authoritative resolved model preview and reflect per-task model overrides.

	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ss := store.NewSessionStore(db)
	el, _ := store.NewEventLog(db)
	s := &Server{sessions: sessionruntime.NewService(ss, el)}
	h := s.apiMux()

	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, ProjectsPath+path, strings.NewReader(body))
		p := identity.Principal{Type: "user", UserID: "owner", AccountScopeID: "account"}
		ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, p)
		tokenRec := &store.ScopedTokenRecord{
			AccountScopeID: "account",
			UserID:         "owner",
			Scopes:         []string{"sessions:write", "projects:write", "projects:read", "sessions:read"},
		}
		ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, tokenRec)
		r = r.WithContext(ctx)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	w := call(http.MethodPost, "", `{"name":"Model Preview Project"}`)
	var projResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &projResp)
	projID := projResp["project"].(map[string]any)["id"].(string)

	w = call(http.MethodPost, "/"+projID+"/tasks", `{"title":"Preview task","agent":"coder"}`)
	var taskResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &taskResp)
	taskID := taskResp["task"].(map[string]any)["id"].(string)

	// Check model-preview endpoint
	w = call(http.MethodGet, "/"+projID+"/tasks/"+taskID+"/model-preview", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on model-preview, got %d: %s", w.Code, w.Body.String())
	}
	var prevResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &prevResp)
	prev := prevResp["model_preview"].(map[string]any)
	if prev["resolved_agent"] != "coder" {
		t.Fatalf("expected resolved_agent coder, got %v", prev["resolved_agent"])
	}
	if prev["account_settings_path"] != "/v3/agents/model-settings" {
		t.Fatalf("expected account_settings_path /v3/agents/model-settings, got %v", prev["account_settings_path"])
	}

	// Update task with model override
	w = call(http.MethodPatch, "/"+projID+"/tasks/"+taskID, `{"model":"gpt-6-astra"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on patch model, got %d: %s", w.Code, w.Body.String())
	}

	// Check model preview reflects task override
	w = call(http.MethodGet, "/"+projID+"/tasks/"+taskID+"/model-preview", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on second model-preview, got %d: %s", w.Code, w.Body.String())
	}
	var prevResp2 map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &prevResp2)
	prev2 := prevResp2["model_preview"].(map[string]any)
	if prev2["task_model_override"] != "gpt-6-astra" {
		t.Fatalf("expected task_model_override gpt-6-astra, got %v", prev2["task_model_override"])
	}
}

func TestProjectsAPI_PendingOnlyPatchEnforcement(t *testing.T) {
	// Written test purpose:
	// - Product requirement/invariant: Only pending tasks (pending_approval, queued) can be modified via PATCH.
	//   Active tasks in progress or completed tasks cannot have their contract patched.
	// - Regression prevented: Modifying agent, scopes, deliverables or models mid-execution.

	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ss := store.NewSessionStore(db)
	el, _ := store.NewEventLog(db)
	s := &Server{sessions: sessionruntime.NewService(ss, el)}
	h := s.apiMux()

	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, ProjectsPath+path, strings.NewReader(body))
		p := identity.Principal{Type: "user", UserID: "owner", AccountScopeID: "account"}
		ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, p)
		tokenRec := &store.ScopedTokenRecord{
			AccountScopeID: "account",
			UserID:         "owner",
			Scopes:         []string{"sessions:write", "projects:write", "projects:read", "sessions:read"},
		}
		ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, tokenRec)
		r = r.WithContext(ctx)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	w := call(http.MethodPost, "", `{"name":"Patch Guard Project"}`)
	var projResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &projResp)
	projID := projResp["project"].(map[string]any)["id"].(string)

	w = call(http.MethodPost, "/"+projID+"/tasks", `{"title":"Pending task","agent":"coder"}`)
	var taskResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &taskResp)
	taskID := taskResp["task"].(map[string]any)["id"].(string)

	// 1. PATCH while pending_approval -> 200 OK
	w = call(http.MethodPatch, "/"+projID+"/tasks/"+taskID, `{"title":"Updated title"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on patch pending task, got %d: %s", w.Code, w.Body.String())
	}

	// 2. Approve task -> transitions to in_progress
	w = call(http.MethodPost, "/"+projID+"/tasks/"+taskID+"/approve", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on approve, got %d: %s", w.Code, w.Body.String())
	}

	// 3. PATCH while in_progress -> 400 Bad Request
	w = call(http.MethodPatch, "/"+projID+"/tasks/"+taskID, `{"title":"Hijacked title"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on patch in_progress task, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "only pending tasks can be updated") {
		t.Fatalf("expected pending-only guard message, got: %s", w.Body.String())
	}
}

func TestProjectsAPI_MediaModelResolution_NeverSwarmLLM(t *testing.T) {
	// Written test purpose:
	// - Product requirement/invariant: Media tasks (image, video, sound) must resolve to dedicated
	//   media models (e.g. imagen, veo, lyria), NEVER falling back to Swarm LLM defaults.
	// - Regression prevented: Generating images or videos with text LLM models or wrong provider.

	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ss := store.NewSessionStore(db)
	el, _ := store.NewEventLog(db)
	s := &Server{sessions: sessionruntime.NewService(ss, el)}
	h := s.apiMux()

	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, ProjectsPath+path, strings.NewReader(body))
		p := identity.Principal{Type: "user", UserID: "owner", AccountScopeID: "account"}
		ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, p)
		tokenRec := &store.ScopedTokenRecord{
			AccountScopeID: "account",
			UserID:         "owner",
			Scopes:         []string{"sessions:write", "projects:write", "projects:read", "sessions:read"},
		}
		ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, tokenRec)
		r = r.WithContext(ctx)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	w := call(http.MethodPost, "", `{"name":"Media Model Project"}`)
	var projResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &projResp)
	projID := projResp["project"].(map[string]any)["id"].(string)

	// Image task
	w = call(http.MethodPost, "/"+projID+"/tasks", `{"title":"Panda in forest","intent":"image"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 on image task, got %d: %s", w.Code, w.Body.String())
	}
	var imgTaskResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &imgTaskResp)
	imgTaskID := imgTaskResp["task"].(map[string]any)["id"].(string)

	w = call(http.MethodGet, "/"+projID+"/tasks/"+imgTaskID+"/model-preview", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on image model preview, got %d: %s", w.Code, w.Body.String())
	}
	var imgPrevResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &imgPrevResp)
	imgPrev := imgPrevResp["model_preview"].(map[string]any)
	resolvedImgModel := imgPrev["resolved_model"].(map[string]any)["model"].(string)
	if !strings.Contains(resolvedImgModel, "imagen") {
		t.Fatalf("expected imagen model for image task, got %q", resolvedImgModel)
	}

	// Video task
	w = call(http.MethodPost, "/"+projID+"/tasks", `{"title":"Drone flyover","intent":"video"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 on video task, got %d: %s", w.Code, w.Body.String())
	}
	var vidTaskResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &vidTaskResp)
	vidTaskID := vidTaskResp["task"].(map[string]any)["id"].(string)

	w = call(http.MethodGet, "/"+projID+"/tasks/"+vidTaskID+"/model-preview", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on video model preview, got %d: %s", w.Code, w.Body.String())
	}
	var vidPrevResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &vidPrevResp)
	vidPrev := vidPrevResp["model_preview"].(map[string]any)
	resolvedVidModel := vidPrev["resolved_model"].(map[string]any)["model"].(string)
	if !strings.Contains(resolvedVidModel, "veo") {
		t.Fatalf("expected veo model for video task, got %q", resolvedVidModel)
	}

	// Audio task
	w = call(http.MethodPost, "/"+projID+"/tasks", `{"title":"Upbeat track","intent":"sound"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 on sound task, got %d: %s", w.Code, w.Body.String())
	}
	var sndTaskResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &sndTaskResp)
	sndTaskID := sndTaskResp["task"].(map[string]any)["id"].(string)

	w = call(http.MethodGet, "/"+projID+"/tasks/"+sndTaskID+"/model-preview", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on sound model preview, got %d: %s", w.Code, w.Body.String())
	}
	var sndPrevResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &sndPrevResp)
	sndPrev := sndPrevResp["model_preview"].(map[string]any)
	resolvedSndModel := sndPrev["resolved_model"].(map[string]any)["model"].(string)
	if !strings.Contains(resolvedSndModel, "lyria") {
		t.Fatalf("expected lyria model for sound task, got %q", resolvedSndModel)
	}
}

func TestProjectsAPI_TaskCreationValidationFailClosed(t *testing.T) {
	// Written test purpose:
	// - Product requirement/invariant: Task creation must reject missing, unknown, or conflicting
	//   structured configurations with actionable 400 errors.
	// - Regression prevented: Silent fallback to coder or accepting coder with big feature.

	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ss := store.NewSessionStore(db)
	el, _ := store.NewEventLog(db)
	s := &Server{sessions: sessionruntime.NewService(ss, el)}
	h := s.apiMux()

	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, ProjectsPath+path, strings.NewReader(body))
		p := identity.Principal{Type: "user", UserID: "owner", AccountScopeID: "account"}
		ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, p)
		tokenRec := &store.ScopedTokenRecord{
			AccountScopeID: "account",
			UserID:         "owner",
			Scopes:         []string{"sessions:write", "projects:write", "projects:read", "sessions:read"},
		}
		ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, tokenRec)
		r = r.WithContext(ctx)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	w := call(http.MethodPost, "", `{"name":"Validation Project"}`)
	var projResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &projResp)
	projID := projResp["project"].(map[string]any)["id"].(string)

	// Missing agent and intent
	w = call(http.MethodPost, "/"+projID+"/tasks", `{"title":"Vague task"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on missing agent and intent, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "missing structured configuration") {
		t.Fatalf("expected missing configuration message, got: %s", w.Body.String())
	}

	// Conflicting: coder with big feature
	w = call(http.MethodPost, "/"+projID+"/tasks", `{"title":"Big feature","agent":"coder","feature_size":"big"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on coder + big feature, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "cannot use coder agent") {
		t.Fatalf("expected cannot use coder message, got: %s", w.Body.String())
	}

	// Unknown agent
	w = call(http.MethodPost, "/"+projID+"/tasks", `{"title":"Unknown agent task","agent":"rogue_bot"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on unknown agent, got %d: %s", w.Code, w.Body.String())
	}
}

// TestProjectTaskPatch_DeliverableClientMetadataSpoofFails proves:
//   - Requirement: Client PATCH on project tasks must NOT allow spoofing of server-generated deliverable metadata
//     (Model, AspectRatio, Resolution, DurationSeconds, VideoProvenance). Server-generated metadata must be retained
//     by deliverable ID, and unknown IDs must have these fields cleared.
//   - Threat/regression: Malicious or misbehaving client modifies deliverable model/settings or provenance via PATCH.
//   - Boundary: Server.handleProjectTask PATCH in projects.go.
//   - Test layer: Direct HTTP endpoint boundary asserting retention of server-generated metadata and rejection of spoofed fields.
func TestProjectTaskPatch_DeliverableClientMetadataSpoofFails(t *testing.T) {
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

	p := identity.Principal{Type: "user", UserID: "owner", AccountScopeID: "account"}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, ProjectsPath+path, strings.NewReader(body))
		ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, p)
		tokenRec := &store.ScopedTokenRecord{
			AccountScopeID: "account",
			UserID:         "owner",
			Scopes:         []string{"sessions:read", "sessions:write", "projects:read", "projects:write"},
		}
		ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, tokenRec)
		r = r.WithContext(ctx)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// 1. Create a project
	proj := &store.ProjectRecord{
		ID:        "proj-spoof-test",
		AccountID: p.AccountScopeID,
		Name:      "Spoof Defense Project",
	}
	if err := ss.PutProject(p.AccountScopeID, proj); err != nil {
		t.Fatalf("put project: %v", err)
	}

	// 2. Put a task with a server-authored deliverable
	initialProv := &store.VideoProvenance{
		AccountScopeID:      p.AccountScopeID,
		Provider:            "google",
		Model:               "veo-3.1-generate-preview",
		Transport:           store.VideoTransportGooglePredictLongRunning,
		Operation:           store.VideoOperationCreate,
		InteractionID:       "secret-interaction-123",
		ProviderResource:    "secret-resource-456",
		AspectRatio:         "16:9",
		Resolution:          "720p",
		DurationSeconds:     8,
		ObservedDurationMs:  8000,
		ExtensionCountKnown: true,
	}
	task := &store.ProjectTaskRecord{
		ID:        "task-spoof-1",
		ProjectID: proj.ID,
		AccountID: p.AccountScopeID,
		Title:     "Original Task",
		Status:    "in_progress",
		Agent:     "video",
		Model:     "veo-3.1-generate-preview",
		Deliverables: []store.ProjectTaskDeliverable{
			{
				ID:              "deliv-legit-1",
				Title:           "Original Deliverable",
				Kind:            "video",
				Status:          "ready",
				Model:           "google:veo-3.1-generate-preview",
				AspectRatio:     "16:9",
				Resolution:      "720p",
				DurationSeconds: 8,
				VideoProvenance: initialProv,
			},
		},
	}
	if err := ss.PutProjectTask(p.AccountScopeID, task); err != nil {
		t.Fatalf("put project task: %v", err)
	}

	// 3. Client attempts to spoof deliverable fields and inject an unknown deliverable
	patchBody := `{
		"deliverables": [
			{
				"id": "deliv-legit-1",
				"title": "Renamed Deliverable",
				"model": "spoofed-openrouter:super-model",
				"aspect_ratio": "1:1",
				"resolution": "4K",
				"duration_seconds": 999,
				"video_provenance": {
					"account_scope_id": "attacker-account",
					"provider": "attacker-provider",
					"model": "fake-model"
				}
			},
			{
				"id": "deliv-unknown-2",
				"title": "Injected Deliverable",
				"model": "injected-model",
				"aspect_ratio": "9:16",
				"resolution": "1080p",
				"duration_seconds": 60,
				"video_provenance": {
					"provider": "fake"
				}
			}
		]
	}`

	w := call(http.MethodPatch, "/"+proj.ID+"/tasks/"+task.ID, patchBody)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on task patch, got %d: %s", w.Code, w.Body.String())
	}

	// 4. Verify in storage that existing deliverable metadata is completely unchanged
	updated, ok, err := ss.GetProjectTask(p.AccountScopeID, proj.ID, task.ID)
	if err != nil || !ok {
		t.Fatalf("get project task failed: ok=%v, err=%v", ok, err)
	}
	if len(updated.Deliverables) != 2 {
		t.Fatalf("expected 2 deliverables, got %d", len(updated.Deliverables))
	}

	d0 := updated.Deliverables[0]
	if d0.ID != "deliv-legit-1" {
		t.Fatalf("expected d0 id deliv-legit-1, got %q", d0.ID)
	}
	if d0.Title != "Renamed Deliverable" {
		t.Errorf("title update should succeed, got %q", d0.Title)
	}
	// Spoofed fields must be ignored and retain original server metadata
	if d0.Model != "google:veo-3.1-generate-preview" {
		t.Errorf("Model was overwritten by client spoof: got %q, want google:veo-3.1-generate-preview", d0.Model)
	}
	if d0.AspectRatio != "16:9" {
		t.Errorf("AspectRatio was overwritten by client spoof: got %q, want 16:9", d0.AspectRatio)
	}
	if d0.Resolution != "720p" {
		t.Errorf("Resolution was overwritten by client spoof: got %q, want 720p", d0.Resolution)
	}
	if d0.DurationSeconds != 8 {
		t.Errorf("DurationSeconds was overwritten by client spoof: got %d, want 8", d0.DurationSeconds)
	}
	if d0.VideoProvenance == nil || d0.VideoProvenance.InteractionID != "secret-interaction-123" {
		t.Errorf("VideoProvenance was altered: got %#v", d0.VideoProvenance)
	}

	// Unknown deliverable ID must have server metadata cleared
	d1 := updated.Deliverables[1]
	if d1.ID != "deliv-unknown-2" {
		t.Fatalf("expected d1 id deliv-unknown-2, got %q", d1.ID)
	}
	if d1.Model != "" {
		t.Errorf("unknown deliverable Model must be cleared, got %q", d1.Model)
	}
	if d1.AspectRatio != "" {
		t.Errorf("unknown deliverable AspectRatio must be cleared, got %q", d1.AspectRatio)
	}
	if d1.Resolution != "" {
		t.Errorf("unknown deliverable Resolution must be cleared, got %q", d1.Resolution)
	}
	if d1.DurationSeconds != 0 {
		t.Errorf("unknown deliverable DurationSeconds must be 0, got %d", d1.DurationSeconds)
	}
	if d1.VideoProvenance != nil {
		t.Errorf("unknown deliverable VideoProvenance must be nil, got %#v", d1.VideoProvenance)
	}

	// 5. Verify sanitized response returned to client
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	taskMap := resp["task"].(map[string]any)
	delivsList := taskMap["deliverables"].([]any)
	d0Map := delivsList[0].(map[string]any)
	if d0Map["model"] != "google:veo-3.1-generate-preview" {
		t.Errorf("response d0 model mismatch: got %v", d0Map["model"])
	}
	provMap := d0Map["video_provenance"].(map[string]any)
	if provMap["interaction_id"] != nil {
		t.Errorf("interaction_id leaked in response: %v", provMap["interaction_id"])
	}
	if provMap["has_interaction"] != true {
		t.Errorf("expected has_interaction=true in sanitized response, got %v", provMap["has_interaction"])
	}
	if provMap["has_provider_resource"] != true {
		t.Errorf("expected has_provider_resource=true in sanitized response, got %v", provMap["has_provider_resource"])
	}
	if int(provMap["duration_seconds"].(float64)) != 8 {
		t.Errorf("expected duration_seconds=8 in sanitized response, got %v", provMap["duration_seconds"])
	}
}
