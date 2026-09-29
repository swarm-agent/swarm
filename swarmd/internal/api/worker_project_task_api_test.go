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

// Requirement: Project tasks API must expose precise worker attribution and worker-only filtering.
// Invariant: GET /v3/projects/{id}/tasks must support ?worker_only=true and ?worker_id={id},
// returning tasks with serialized worker_id, worker_name, worker_run_id, automation_id.
// POST and PATCH endpoints must persist worker fields.
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
		CreatedAt: now - 3000,
	}
	if err := sessionStore.PutProjectTask(accountID, taskStandard); err != nil {
		t.Fatal(err)
	}

	// 2. Task with worker A attribution
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

	// 3. Task with worker B attribution
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

	// (A) Normal GET without filter returns all 3 tasks
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
	if respAll.Count != 3 || len(respAll.Tasks) != 3 {
		t.Fatalf("expected 3 tasks, got count=%d, len=%d", respAll.Count, len(respAll.Tasks))
	}

	// (B) GET with worker_only=true returns only worker tasks (2)
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
		t.Fatalf("expected 2 worker tasks, got count=%d, len=%d", respWorkerOnly.Count, len(respWorkerOnly.Tasks))
	}
	for _, tk := range respWorkerOnly.Tasks {
		if tk.WorkerID == "" && tk.WorkerName == "" {
			t.Errorf("expected worker attribution on task %s", tk.ID)
		}
	}

	// (C) GET with filter=workers returns only worker tasks (2)
	wFilterWorkers := makeReq(ProjectsPath + "/" + proj.ID + "/tasks?filter=workers")
	if wFilterWorkers.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", wFilterWorkers.Code, wFilterWorkers.Body.String())
	}
	var respFilterWorkers struct {
		Tasks []pebblestore.ProjectTaskRecord `json:"tasks"`
		Count int                             `json:"count"`
	}
	if err := json.Unmarshal(wFilterWorkers.Body.Bytes(), &respFilterWorkers); err != nil {
		t.Fatal(err)
	}
	if respFilterWorkers.Count != 2 {
		t.Fatalf("expected 2 worker tasks with filter=workers, got %d", respFilterWorkers.Count)
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

	// (E) POST /v3/projects/{id}/tasks with worker fields
	postBody, _ := json.Marshal(map[string]any{
		"title":         "Created worker task",
		"agent":         "swarm",
		"worker_id":     "worker_gamma",
		"worker_name":   "Gamma Worker",
		"worker_run_id": "run_gamma_1",
		"automation_id": "auto_gamma_1",
	})
	rPost := httptest.NewRequest(http.MethodPost, ProjectsPath+"/"+proj.ID+"/tasks", bytes.NewReader(postBody))
	p := testPrincipal()
	ctx := context.WithValue(rPost.Context(), productPrincipalRequestContextKey, p)
	tokenRec := &pebblestore.ScopedTokenRecord{
		AccountScopeID: p.AccountScopeID,
		UserID:         p.UserID,
		Scopes:         []string{"projects:read", "sessions:read", "projects:write", "sessions:write"},
	}
	ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, tokenRec)
	rPost = rPost.WithContext(ctx)
	wPost := httptest.NewRecorder()
	h.ServeHTTP(wPost, rPost)
	if wPost.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", wPost.Code, wPost.Body.String())
	}
	var postResp struct {
		Task pebblestore.ProjectTaskRecord `json:"task"`
	}
	if err := json.Unmarshal(wPost.Body.Bytes(), &postResp); err != nil {
		t.Fatal(err)
	}
	if postResp.Task.WorkerID != "worker_gamma" || postResp.Task.WorkerName != "Gamma Worker" || postResp.Task.WorkerRunID != "run_gamma_1" || postResp.Task.AutomationID != "auto_gamma_1" {
		t.Errorf("POST created task missing worker fields: %+v", postResp.Task)
	}

	// (F) PATCH /v3/projects/{id}/tasks/{taskId} updating worker fields
	patchBody, _ := json.Marshal(map[string]any{
		"worker_name": "Updated Gamma Worker",
	})
	rPatch := httptest.NewRequest(http.MethodPatch, ProjectsPath+"/"+proj.ID+"/tasks/"+postResp.Task.ID, bytes.NewReader(patchBody))
	rPatch = rPatch.WithContext(ctx)
	wPatch := httptest.NewRecorder()
	h.ServeHTTP(wPatch, rPatch)
	if wPatch.Code != http.StatusOK {
		t.Fatalf("expected 200 on PATCH, got %d: %s", wPatch.Code, wPatch.Body.String())
	}
	var patchResp struct {
		Task pebblestore.ProjectTaskRecord `json:"task"`
	}
	if err := json.Unmarshal(wPatch.Body.Bytes(), &patchResp); err != nil {
		t.Fatal(err)
	}
	if patchResp.Task.WorkerName != "Updated Gamma Worker" {
		t.Errorf("PATCH expected updated worker_name, got %q", patchResp.Task.WorkerName)
	}
}
