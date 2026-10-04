package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the first HTTP collection read after the production store-open startup
// boundary must succeed on a populated pre-index database, without retry or an
// explicitly prebuilt index. This real Pebble/reopen/handler test is deterministic
// regression evidence, not a live daemon/browser performance benchmark.
func TestProjectTaskStartupFirstHTTPRead(t *testing.T) {
	path := t.TempDir()
	db, err := pebblestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p := identity.Principal{Type: "user", AccountScopeID: "account", UserID: "user"}
	if err := db.PutJSON(pebblestore.KeyProject(p.AccountScopeID, "project"), pebblestore.ProjectRecord{ID: "project", AccountID: p.AccountScopeID, Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 97; i++ {
		task := pebblestore.ProjectTaskRecord{ID: fmt.Sprintf("task-%03d", i), AccountID: p.AccountScopeID, ProjectID: "project", Title: "Task", Agent: "swarm", Status: "pending_approval", Archived: i >= 37, FullPlanMarkdown: strings.Repeat("legacy-detail", 4096)}
		if err := db.PutJSON(pebblestore.KeyProjectTask(p.AccountScopeID, "project", task.ID), task); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = pebblestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewSessionStore(db)
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{sessions: sessionruntime.NewService(store, events)}
	req := httptest.NewRequest(http.MethodGet, "/v3/projects/project/tasks", nil)
	req = req.WithContext(context.WithValue(req.Context(), productPrincipalRequestContextKey, p))
	req = req.WithContext(context.WithValue(req.Context(), productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{AccountScopeID: p.AccountScopeID, UserID: p.UserID, Scopes: []string{"projects:read"}}))
	response := httptest.NewRecorder()
	server.handleProjects(response, req)
	if response.Code != 200 {
		t.Fatalf("first HTTP read %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 37 || response.Header().Get("X-Task-Backfill-Bytes") != "0" || response.Header().Get("X-Task-Scanned-Rows") != "37" || strings.Contains(response.Body.String(), "legacy-detail") {
		t.Fatalf("invalid first read: count=%d headers=%v", body.Count, response.Header())
	}
}
