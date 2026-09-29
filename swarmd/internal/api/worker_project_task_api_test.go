package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: Project tasks API must expose precise worker attribution, worker-only filtering,
// and reject client-forged worker identities on POST and PATCH.
// Invariant:
// 1. GET /v3/projects/{id}/tasks?worker_only=true returns ONLY tasks with non-empty WorkerID (generic WorkerName without WorkerID excluded).
// 2. GET /v3/projects/{id}/tasks?worker_id={id} returns exact matching worker tasks. Aliases like filter=workers or workers_only are not supported.
// 3. POST /v3/projects/{id}/tasks rejects client-supplied worker identity fields (worker_id, worker_run_id, automation_id) with 400.
// 4. PATCH /v3/projects/{id}/tasks/{taskId} rejects client attempts to modify worker_id, worker_run_id, automation_id, session_id, project_id,
//    and rejects modifying worker_name on worker-attributed tasks, while preserving generic worker_name edits on ordinary tasks.
// Boundary: projects.go (GET/POST /v3/projects/{id}/tasks, PATCH /v3/projects/{id}/tasks/{taskId}).
func TestProjectsAPI_WorkerTaskAttributionAndFilter(t *testing.T) {
	t.Setenv("SWARM_API_NO_AUTH", "1")
	server, _, dbStore := newWorkspaceOverviewTopologyTestServer(t)
	server.ConfigureProjectRealtime(dbStore)
	sessionStore := pebblestore.NewSessionStore(dbStore)
	accountID := testPrincipal().AccountScopeID

	proj := &pebblestore.ProjectRecord{Name: "Worker Filter Test Project"}
	if err := sessionStore.PutProject(accountID, proj); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UnixMilli()

	// 1. Task without worker attribution
	taskStandard := &pebblestore.ProjectTaskRecord{
		ID:        "task_standard",
		ProjectID: proj.ID,
		AccountID: accountID,
		Title:     "Standard human task",
		Status:    "in_progress",
		Agent:     "coder",
		CreatedAt: now - 4000,
	}
	if err := sessionStore.PutProjectTask(accountID, taskStandard); err != nil {
		t.Fatal(err)
	}

	// 2. Task with generic worker name but NO worker_id (must NOT be treated as worker-attributed)
	taskGenericWorker := &pebblestore.ProjectTaskRecord{
		ID:         "task_generic_worker",
		ProjectID:  proj.ID,
		AccountID:  accountID,
		Title:      "Generic worker task",
		Status:     "in_progress",
		Agent:      "coder",
		WorkerName: "@Coder Worker",
		CreatedAt:  now - 3000,
	}
	if err := sessionStore.PutProjectTask(accountID, taskGenericWorker); err != nil {
		t.Fatal(err)
	}

	// 3. Task with worker A attribution
	taskWorkerA := &pebblestore.ProjectTaskRecord{
		ID:           "task_worker_a",
		ProjectID:    proj.ID,
		AccountID:    accountID,
		Title:        "Automated worker A task",
		Status:       "in_progress",
		Agent:        "swarm",
		WorkerID:     "worker_alpha",
		WorkerName:   "Alpha Worker",
		WorkerRunID:  "run_alpha_1",
		AutomationID: "auto_alpha_1",
		CreatedAt:    now - 2000,
	}
	if err := sessionStore.PutProjectTask(accountID, taskWorkerA); err != nil {
		t.Fatal(err)
	}

	// 4. Task with worker B attribution
	taskWorkerB := &pebblestore.ProjectTaskRecord{
		ID:           "task_worker_b",
		ProjectID:    proj.ID,
		AccountID:    accountID,
		Title:        "Automated worker B task",
		Status:       "in_progress",
		Agent:        "swarm",
		WorkerID:     "worker_beta",
		WorkerName:   "Beta Worker",
		WorkerRunID:  "run_beta_1",
		AutomationID: "auto_beta_1",
		CreatedAt:    now - 1000,
	}
	if err := sessionStore.PutProjectTask(accountID, taskWorkerB); err != nil {
		t.Fatal(err)
	}

	h := server.apiMux()
	makeReq := func(url string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, url, nil)
		p := testPrincipal()
		ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, p)
		tokenRec := &pebblestore.ScopedTokenRecord{
			AccountScopeID: p.AccountScopeID,
			UserID:         p.UserID,
			Scopes:         []string{"projects:read", "sessions:read", "projects:write", "sessions:write"},
		}
		ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, tokenRec)
		r = r.WithContext(ctx)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// (A) Normal GET without filter returns all 4 tasks
	wAll := makeReq(ProjectsPath + "/" + proj.ID + "/tasks")
	if wAll.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", wAll.Code, wAll.Body.String())
	}
	var respAll struct {
		Tasks []pebblestore.ProjectTaskRecord `json:"tasks"`
		Count int                             `json:"count"`
	}
	if err := json.Unmarshal(wAll.Body.Bytes(), &respAll); err != nil {
		t.Fatal(err)
	}
	if respAll.Count != 4 || len(respAll.Tasks) != 4 {
		t.Fatalf("expected 4 tasks, got count=%d, len=%d", respAll.Count, len(respAll.Tasks))
	}

	// (B) GET with worker_only=true returns ONLY the 2 worker-attributed tasks (excludes generic WorkerName with no WorkerID)
	wWorkerOnly := makeReq(ProjectsPath + "/" + proj.ID + "/tasks?worker_only=true")
	if wWorkerOnly.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", wWorkerOnly.Code, wWorkerOnly.Body.String())
	}
	var respWorkerOnly struct {
		Tasks []pebblestore.ProjectTaskRecord `json:"tasks"`
		Count int                             `json:"count"`
	}
	if err := json.Unmarshal(wWorkerOnly.Body.Bytes(), &respWorkerOnly); err != nil {
		t.Fatal(err)
	}
	if respWorkerOnly.Count != 2 || len(respWorkerOnly.Tasks) != 2 {
		t.Fatalf("expected exactly 2 worker tasks with worker_only=true, got count=%d, len=%d", respWorkerOnly.Count, len(respWorkerOnly.Tasks))
	}
	for _, tk := range respWorkerOnly.Tasks {
		if tk.WorkerID == "" {
			t.Errorf("worker_only returned task without WorkerID: %+v", tk)
		}
	}

	// (C) GET with unrequested aliases filter=workers or workers_only=true does not filter (returns all 4)
	wFilterAlias := makeReq(ProjectsPath + "/" + proj.ID + "/tasks?filter=workers")
	if wFilterAlias.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", wFilterAlias.Code)
	}
	var respFilterAlias struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(wFilterAlias.Body.Bytes(), &respFilterAlias); err != nil {
		t.Fatal(err)
	}
	if respFilterAlias.Count != 4 {
		t.Fatalf("expected 4 tasks when using ignored filter alias, got %d", respFilterAlias.Count)
	}

	// (D) GET with worker_id=worker_alpha returns only 1 task
	wAlpha := makeReq(ProjectsPath + "/" + proj.ID + "/tasks?worker_id=worker_alpha")
	if wAlpha.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", wAlpha.Code, wAlpha.Body.String())
	}
	var respAlpha struct {
		Tasks []pebblestore.ProjectTaskRecord `json:"tasks"`
		Count int                             `json:"count"`
	}
	if err := json.Unmarshal(wAlpha.Body.Bytes(), &respAlpha); err != nil {
		t.Fatal(err)
	}
	if respAlpha.Count != 1 || len(respAlpha.Tasks) != 1 {
		t.Fatalf("expected 1 task for worker_alpha, got count=%d", respAlpha.Count)
	}
	tkAlpha := respAlpha.Tasks[0]
	if tkAlpha.WorkerID != "worker_alpha" || tkAlpha.WorkerName != "Alpha Worker" || tkAlpha.WorkerRunID != "run_alpha_1" || tkAlpha.AutomationID != "auto_alpha_1" {
		t.Errorf("wrong worker fields on task: %+v", tkAlpha)
	}

	// (E) Negative POST: client attempts to forge worker identity fields must be rejected (400 Bad Request)
	p := testPrincipal()
	tokenRec := &pebblestore.ScopedTokenRecord{
		AccountScopeID: p.AccountScopeID,
		UserID:         p.UserID,
		Scopes:         []string{"projects:read", "sessions:read", "projects:write", "sessions:write"},
	}

	postSpoofWorkerID, _ := json.Marshal(map[string]any{
		"title":     "Spoofed worker task",
		"agent":     "swarm",
		"worker_id": "worker_spoofed",
	})
	rPost := httptest.NewRequest(http.MethodPost, ProjectsPath+"/"+proj.ID+"/tasks", bytes.NewReader(postSpoofWorkerID))
	ctx := context.WithValue(rPost.Context(), productPrincipalRequestContextKey, p)
	ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, tokenRec)
	rPost = rPost.WithContext(ctx)
	wPost := httptest.NewRecorder()
	h.ServeHTTP(wPost, rPost)
	if wPost.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when client specifies worker_id in POST, got %d: %s", wPost.Code, wPost.Body.String())
	}

	postSpoofRunID, _ := json.Marshal(map[string]any{
		"title":         "Spoofed run task",
		"agent":         "swarm",
		"worker_run_id": "run_spoofed",
	})
	rPostRun := httptest.NewRequest(http.MethodPost, ProjectsPath+"/"+proj.ID+"/tasks", bytes.NewReader(postSpoofRunID)).WithContext(ctx)
	wPostRun := httptest.NewRecorder()
	h.ServeHTTP(wPostRun, rPostRun)
	if wPostRun.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when client specifies worker_run_id in POST, got %d: %s", wPostRun.Code, wPostRun.Body.String())
	}

	postSpoofAutoID, _ := json.Marshal(map[string]any{
		"title":         "Spoofed auto task",
		"agent":         "swarm",
		"automation_id": "auto_spoofed",
	})
	rPostAuto := httptest.NewRequest(http.MethodPost, ProjectsPath+"/"+proj.ID+"/tasks", bytes.NewReader(postSpoofAutoID)).WithContext(ctx)
	wPostAuto := httptest.NewRecorder()
	h.ServeHTTP(wPostAuto, rPostAuto)
	if wPostAuto.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when client specifies automation_id in POST, got %d: %s", wPostAuto.Code, wPostAuto.Body.String())
	}

	// Ordinary task POST with generic worker_name is allowed
	postOrdinary, _ := json.Marshal(map[string]any{
		"title":       "Ordinary generic task",
		"agent":       "coder",
		"worker_name": "Generic Assistant",
	})
	rPostOrd := httptest.NewRequest(http.MethodPost, ProjectsPath+"/"+proj.ID+"/tasks", bytes.NewReader(postOrdinary)).WithContext(ctx)
	wPostOrd := httptest.NewRecorder()
	h.ServeHTTP(wPostOrd, rPostOrd)
	if wPostOrd.Code != http.StatusCreated {
		t.Fatalf("expected 201 for ordinary task with generic worker_name, got %d: %s", wPostOrd.Code, wPostOrd.Body.String())
	}
	var postOrdResp struct {
		Task pebblestore.ProjectTaskRecord `json:"task"`
	}
	if err := json.Unmarshal(wPostOrd.Body.Bytes(), &postOrdResp); err != nil {
		t.Fatal(err)
	}
	if postOrdResp.Task.WorkerName != "Generic Assistant" || postOrdResp.Task.WorkerID != "" {
		t.Fatalf("unexpected worker fields on ordinary task: %+v", postOrdResp.Task)
	}

	// (F) Negative PATCH: client cannot modify worker_id, worker_run_id, automation_id, session_id, project_id
	patchWorkerID, _ := json.Marshal(map[string]any{"worker_id": "malicious_id"})
	rPatchWID := httptest.NewRequest(http.MethodPatch, ProjectsPath+"/"+proj.ID+"/tasks/"+postOrdResp.Task.ID, bytes.NewReader(patchWorkerID)).WithContext(ctx)
	wPatchWID := httptest.NewRecorder()
	h.ServeHTTP(wPatchWID, rPatchWID)
	if wPatchWID.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on PATCH worker_id, got %d: %s", wPatchWID.Code, wPatchWID.Body.String())
	}

	patchRunID, _ := json.Marshal(map[string]any{"worker_run_id": "malicious_run"})
	rPatchRun := httptest.NewRequest(http.MethodPatch, ProjectsPath+"/"+proj.ID+"/tasks/"+postOrdResp.Task.ID, bytes.NewReader(patchRunID)).WithContext(ctx)
	wPatchRun := httptest.NewRecorder()
	h.ServeHTTP(wPatchRun, rPatchRun)
	if wPatchRun.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on PATCH worker_run_id, got %d: %s", wPatchRun.Code, wPatchRun.Body.String())
	}

	patchSessID, _ := json.Marshal(map[string]any{"session_id": "malicious_sess"})
	rPatchSess := httptest.NewRequest(http.MethodPatch, ProjectsPath+"/"+proj.ID+"/tasks/"+postOrdResp.Task.ID, bytes.NewReader(patchSessID)).WithContext(ctx)
	wPatchSess := httptest.NewRecorder()
	h.ServeHTTP(wPatchSess, rPatchSess)
	if wPatchSess.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on PATCH session_id, got %d: %s", wPatchSess.Code, wPatchSess.Body.String())
	}

	// Client cannot rewrite worker_name on a worker-attributed task (taskWorkerA)
	// Put taskWorkerA in pending_approval status so status guard does not trigger first
	taskWorkerA.Status = "pending_approval"
	if err := sessionStore.PutProjectTask(accountID, taskWorkerA); err != nil {
		t.Fatal(err)
	}
	patchAttrWorkerName, _ := json.Marshal(map[string]any{"worker_name": "Tampered Alpha"})
	rPatchAttr := httptest.NewRequest(http.MethodPatch, ProjectsPath+"/"+proj.ID+"/tasks/"+taskWorkerA.ID, bytes.NewReader(patchAttrWorkerName)).WithContext(ctx)
	wPatchAttr := httptest.NewRecorder()
	h.ServeHTTP(wPatchAttr, rPatchAttr)
	if wPatchAttr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on PATCH worker_name of attributed task, got %d: %s", wPatchAttr.Code, wPatchAttr.Body.String())
	}

	// Client CAN rewrite generic worker_name on ordinary task
	patchOrdWorkerName, _ := json.Marshal(map[string]any{"worker_name": "Updated Generic Assistant"})
	rPatchOrd := httptest.NewRequest(http.MethodPatch, ProjectsPath+"/"+proj.ID+"/tasks/"+postOrdResp.Task.ID, bytes.NewReader(patchOrdWorkerName)).WithContext(ctx)
	wPatchOrd := httptest.NewRecorder()
	h.ServeHTTP(wPatchOrd, rPatchOrd)
	if wPatchOrd.Code != http.StatusOK {
		t.Fatalf("expected 200 on PATCH generic worker_name, got %d: %s", wPatchOrd.Code, wPatchOrd.Body.String())
	}
	var patchResp struct {
		Task pebblestore.ProjectTaskRecord `json:"task"`
	}
	if err := json.Unmarshal(wPatchOrd.Body.Bytes(), &patchResp); err != nil {
		t.Fatal(err)
	}
	if patchResp.Task.WorkerName != "Updated Generic Assistant" {
		t.Errorf("expected updated generic worker_name, got %q", patchResp.Task.WorkerName)
	}
}
