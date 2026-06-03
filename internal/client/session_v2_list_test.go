package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestListSessionsForExactCWDUsesStrictV2CWDRoute(t *testing.T) {
	var gotMethod, gotPath string
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"sessions": []map[string]any{
				{"id": "session-1", "workspace_path": "/cwd", "title": "Session"},
			},
		})
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	sessions, err := api.ListSessionsForExactCWD(context.Background(), 25, " /cwd ")
	if err != nil {
		t.Fatalf("ListSessionsForExactCWD() error = %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Fatalf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/v2/sessions" {
		t.Fatalf("path = %q, want /v2/sessions", gotPath)
	}
	if gotQuery.Get("cwd") != "/cwd" {
		t.Fatalf("cwd query = %q, want /cwd", gotQuery.Get("cwd"))
	}
	if gotQuery.Get("workspace_binding_id") != "" {
		t.Fatalf("workspace_binding_id query = %q, want empty", gotQuery.Get("workspace_binding_id"))
	}
	if gotQuery.Get("limit") != "25" {
		t.Fatalf("limit query = %q, want 25", gotQuery.Get("limit"))
	}
	if len(sessions) != 1 || sessions[0].ID != "session-1" {
		t.Fatalf("sessions = %#v, want session-1", sessions)
	}
}

func TestListSessionsForWorkspaceBindingUsesStrictV2BindingRoute(t *testing.T) {
	var gotMethod, gotPath string
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"sessions": []map[string]any{
				{"id": "session-1", "workspace_path": "/host/workspace", "title": "Primary"},
				{"id": "session-2", "workspace_path": "/workspaces/project", "title": "Container"},
			},
		})
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	sessions, err := api.ListSessionsForWorkspaceBinding(context.Background(), 0, " binding-primary ")
	if err != nil {
		t.Fatalf("ListSessionsForWorkspaceBinding() error = %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Fatalf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/v2/sessions" {
		t.Fatalf("path = %q, want /v2/sessions", gotPath)
	}
	if gotQuery.Get("workspace_binding_id") != "binding-primary" {
		t.Fatalf("workspace_binding_id query = %q, want binding-primary", gotQuery.Get("workspace_binding_id"))
	}
	if gotQuery.Get("cwd") != "" {
		t.Fatalf("cwd query = %q, want empty", gotQuery.Get("cwd"))
	}
	if gotQuery.Get("limit") != "100" {
		t.Fatalf("limit query = %q, want default 100", gotQuery.Get("limit"))
	}
	if len(sessions) != 2 {
		t.Fatalf("sessions len = %d, want 2", len(sessions))
	}
}

func TestListSessionsV2RequiresExplicitRouteValue(t *testing.T) {
	api := New("http://127.0.0.1:1")
	api.SetToken("test-token")
	if _, err := api.ListSessionsForExactCWD(context.Background(), 10, " "); err == nil {
		t.Fatalf("ListSessionsForExactCWD() error = nil, want required cwd error")
	}
	if _, err := api.ListSessionsForWorkspaceBinding(context.Background(), 10, " "); err == nil {
		t.Fatalf("ListSessionsForWorkspaceBinding() error = nil, want required binding error")
	}
}
