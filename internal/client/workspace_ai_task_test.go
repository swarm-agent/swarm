package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
				if body["managed_worktree_requested"] != true || body["plan_mode_requested"] != test.planMode {
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
