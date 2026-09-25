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
	s := &Server{sessions: sessionruntime.NewService(ss, nil)}
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

	// 5. DELETE /v3/projects/{id}
	w = call(http.MethodDelete, "/"+projID, "", []string{"sessions:write"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on delete, got %d: %s", w.Code, w.Body.String())
	}

	// 6. Verify 404 after delete
	w = call(http.MethodGet, "/"+projID, "", []string{"sessions:read"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d: %s", w.Code, w.Body.String())
	}
}
