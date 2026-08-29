package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateRoutedTaskSessionRejectsMissingRouterTitleOrOwnedWorktree(t *testing.T) {
	for _, test := range []struct {
		name       string
		title      string
		worktree  bool
		rootPath  string
		wantError string
	}{
		{name: "missing title", worktree: true, rootPath: "/worktree", wantError: "no task title"},
		{name: "worktree disabled", title: "Fix routing", rootPath: "/worktree", wantError: "required owned-worktree"},
		{name: "worktree path missing", title: "Fix routing", worktree: true, wantError: "required owned-worktree"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"ok": true, "session_id": "session-task", "title": test.title, "starting_mode": "auto",
					"session": map[string]any{"id": "session-task", "title": test.title, "mode": "auto", "worktree_enabled": test.worktree, "worktree_root_path": test.rootPath},
				})
			}))
			defer server.Close()
			api := New(server.URL)
			api.SetToken("test-token")
			_, err := api.CreateRoutedTaskSession(context.Background(), "fix routing", "task-request", false, "", RoutedTaskWorkspaceAuthority{
				WorkspacePath: "/source-workspace", WorkspaceBindingID: "source-binding", SwarmID: "host-swarm",
			})
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestCreateRoutedTaskSessionPinsWorktreeAndMode(t *testing.T) {
	for _, test := range []struct {
		name     string
		planMode bool
		wantMode string
	}{
		{name: "auto", wantMode: "auto"},
		{name: "plan", planMode: true, wantMode: "plan"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != routedTaskSessionsPath {
					t.Fatalf("request = %s %s", r.Method, r.URL.Path)
				}
				if got := r.Header.Get("Idempotency-Key"); got != "task-request" {
					t.Fatalf("Idempotency-Key = %q", got)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body["input"] != "fix routing" || body["client_request_id"] != "task-request" || body["agent_name"] != "swarm" {
					t.Fatalf("routed task identity = %#v", body)
				}
				if _, ok := body["managed_worktree_requested"]; ok {
					t.Fatalf("retired managed_worktree_requested was sent: %#v", body)
				}
				if body["plan_mode_requested"] != test.planMode {
					t.Fatalf("routed task intent = %#v", body)
				}
				if body["workspace_path"] != "/source-workspace" || body["host_workspace_path"] != "/source-workspace" || body["runtime_workspace_path"] != "/source-workspace" || body["workspace_binding_id"] != "source-binding" || body["swarm_id"] != "host-swarm" || body["target_kind"] != "host" || body["target_relationship"] != "self" {
					t.Fatalf("routed task workspace authority = %#v", body)
				}
				metadata, _ := body["metadata"].(map[string]any)
				if metadata["task_command"] != true || metadata["task_origin_session_id"] != "origin-session" {
					t.Fatalf("routed task metadata = %#v", metadata)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"ok":            true,
					"session_id":    "session-task",
					"title":         "Fix routing",
					"starting_mode": test.wantMode,
					"session": map[string]any{
						"id": "session-task", "title": "Fix routing", "mode": test.wantMode,
						"worktree_enabled": true, "worktree_root_path": "/worktree",
					},
				})
			}))
			defer server.Close()

			api := New(server.URL)
			api.SetToken("test-token")
			response, err := api.CreateRoutedTaskSession(context.Background(), "fix routing", "task-request", test.planMode, "origin-session", RoutedTaskWorkspaceAuthority{
				WorkspacePath:      "/source-workspace",
				WorkspaceBindingID: "source-binding",
				SwarmID:            "host-swarm",
			})
			if err != nil {
				t.Fatal(err)
			}
			if response.SessionID != "session-task" || !response.Session.WorktreeEnabled || response.StartingMode != test.wantMode {
				t.Fatalf("response = %#v", response)
			}
		})
	}
}
