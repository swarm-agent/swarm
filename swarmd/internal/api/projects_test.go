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
	// - Invariant: POST /v3/projects/{id}/tasks/{taskId}/integrate must reject tasks
	//   that have 0 unintegrated commits with 400 Bad Request, never faking integration.
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

	// POST integrate should fail with 400 Bad Request
	w := call(http.MethodPost, "/"+proj.ID+"/tasks/"+task.ID+"/integrate", "", []string{"projects:write", "sessions:write"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request when integrating task with 0 commits, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "no commits to integrate") {
		t.Fatalf("expected error message to mention no commits to integrate, got %s", w.Body.String())
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
