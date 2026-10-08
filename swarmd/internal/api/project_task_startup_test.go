package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	if err := db.Delete("project_conversation_archive_migration/v1"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 70; i++ {
		id := fmt.Sprintf("archived-%03d", i)
		session := pebblestore.SessionSnapshot{ID: id, AccountScopeID: p.AccountScopeID, UserID: p.UserID, Title: "Conversation", CreatedAt: int64(i + 1), Metadata: map[string]any{"project_id": "project", "agent_name": "system-orchestrator", "private_body": strings.Repeat("archive-detail", 4096)}}
		tombstone := pebblestore.V3SessionTombstone{SessionID: id, AccountScopeID: p.AccountScopeID, UserID: p.UserID, Kind: "archived", Archived: true, UpdatedAt: int64(i + 1), Session: session}
		if err := db.PutJSON(pebblestore.KeyV3SessionTombstone(id), tombstone); err != nil {
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
	req = req.WithContext(context.WithValue(req.Context(), productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{AccountScopeID: p.AccountScopeID, UserID: p.UserID, Scopes: []string{"projects:read", "sessions:read"}}))
	// Exercise a real loopback HTTP first read after the production Open boundary;
	// only identity injection is fixture-owned, not storage or request handling.
	listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, incoming *http.Request) {
		incoming = incoming.WithContext(context.WithValue(incoming.Context(), productPrincipalRequestContextKey, p))
		incoming = incoming.WithContext(context.WithValue(incoming.Context(), productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{AccountScopeID: p.AccountScopeID, UserID: p.UserID, Scopes: []string{"projects:read", "sessions:read"}}))
		server.handleProjects(w, incoming)
	}))
	defer listener.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	started := time.Now()
	httpResponse, err := client.Get(listener.URL + req.URL.RequestURI())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(io.LimitReader(httpResponse.Body, 1<<20))
	httpResponse.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("isolated fixture only (not live acceptance): tasks=97 active=37 first_http=%d bytes=%d elapsed=%s", httpResponse.StatusCode, len(payload), time.Since(started))
	response := httptest.NewRecorder()
	response.Code = httpResponse.StatusCode
	response.HeaderMap = httpResponse.Header
	response.Body.Write(payload)
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
	archive, err := client.Get(listener.URL + "/v3/projects/project/sessions?archived_mode=only")
	if err != nil {
		t.Fatal(err)
	}
	archiveBytes, err := io.ReadAll(io.LimitReader(archive.Body, 1<<20))
	archive.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	var archiveBody struct {
		Tombstones []pebblestore.V3SessionTombstone `json:"tombstones"`
	}
	if err := json.Unmarshal(archiveBytes, &archiveBody); err != nil {
		t.Fatal(err)
	}
	if archive.StatusCode != 200 || len(archiveBody.Tombstones) != 70 || len(archiveBytes) > 100<<10 || strings.Contains(string(archiveBytes), "archive-detail") {
		t.Fatalf("archive first read status=%d rows=%d bytes=%d", archive.StatusCode, len(archiveBody.Tombstones), len(archiveBytes))
	}
	t.Logf("isolated archive fixture only: first_http=%d rows=70 bytes=%d", archive.StatusCode, len(archiveBytes))
}
