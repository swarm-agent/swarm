package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateSessionWithOptionsPostsStrictV2PrimaryPayload(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")

	var gotPath string
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.URL.RawQuery != "" {
			t.Fatalf("unexpected query on create request: %q", r.URL.RawQuery)
		}
		if r.Header.Get("X-Swarm-Token") == "" {
			t.Fatalf("missing X-Swarm-Token on v2 create request")
		}
		if r.Header.Get("X-Swarm-Client") != "swarmtui" {
			t.Fatalf("X-Swarm-Client = %q, want swarmtui", r.Header.Get("X-Swarm-Client"))
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"session": map[string]any{
				"id":             "session-1",
				"workspace_path": "/host/workspace",
				"workspace_name": "Workspace",
				"title":          "New Session",
				"mode":           "auto",
			},
			"session_execution": map[string]any{
				"session_id":           "session-1",
				"execution_class":      "primary",
				"runtime_swarm_id":     "child-swarm",
				"workspace_binding_id": "binding-child",
			},
		})
	}))
	defer server.Close()

	useCurrentBranch := false
	api := New(server.URL)
	api.SetToken("test-token")
	session, err := api.CreateSessionWithOptions(context.Background(), SessionCreateOptions{
		Title:                    "New Session",
		WorkspacePath:            "/host/workspace",
		HostWorkspacePath:        "/host/workspace",
		RuntimeWorkspacePath:     "/workspaces/swarm-go",
		WorkspaceName:            "Workspace",
		WorkspaceBindingID:       "binding-child",
		SwarmID:                  "child-swarm",
		TargetKind:               "host",
		TargetRelationship:       "self",
		WorktreeMode:             "off",
		WorktreeUseCurrentBranch: &useCurrentBranch,
		WorktreeBaseBranch:       "main",
		WorktreeBranchName:       "agent",
		Preference: ModelPreference{
			Provider: "anthropic",
			Model:    "claude",
			Thinking: "auto",
		},
	})
	if err != nil {
		t.Fatalf("CreateSessionWithOptions() error = %v", err)
	}
	if gotPath != "/v2/sessions/primary" {
		t.Fatalf("request path = %q, want /v2/sessions/primary", gotPath)
	}
	if got, _ := body["swarm_id"].(string); got != "child-swarm" {
		t.Fatalf("swarm_id = %q, want child-swarm", got)
	}
	if got, _ := body["workspace_binding_id"].(string); got != "binding-child" {
		t.Fatalf("workspace_binding_id = %q, want binding-child", got)
	}
	if session.SessionExecution == nil || session.SessionExecution.RuntimeSwarmID != "child-swarm" || session.SessionExecution.WorkspaceBindingID != "binding-child" {
		t.Fatalf("session execution = %#v, want child-swarm/binding-child", session.SessionExecution)
	}
	if got, _ := body["mode"].(string); got != "plan" {
		t.Fatalf("mode = %q, want plan", got)
	}
	if got, _ := body["agent_name"].(string); got != "swarm" {
		t.Fatalf("agent_name = %q, want swarm", got)
	}
	if got, _ := body["worktree_mode"].(string); got != "off" {
		t.Fatalf("worktree_mode = %q, want off", got)
	}
	for _, key := range []string{"worktree_use_current_branch", "worktree_base_branch", "worktree_branch_name"} {
		if _, ok := body[key]; ok {
			t.Fatalf("%s present while worktree_mode is off: %#v", key, body[key])
		}
	}
	if metadata, ok := body["metadata"].(map[string]any); !ok || len(metadata) != 0 {
		t.Fatalf("metadata = %#v, want empty object", body["metadata"])
	}
	for _, key := range []string{"workspace_name", "workspace_path", "host_workspace_path", "runtime_workspace_path", "target_swarm_id", "routing_hint"} {
		if _, ok := body[key]; ok {
			t.Fatalf("%s present in workspace-bound create request: %#v", key, body[key])
		}
	}
}

func TestCreateSessionWithOptionsRejectsRetiredLocalContainerEndpoint(t *testing.T) {
	api := New("http://127.0.0.1")
	api.SetToken("test-token")
	_, err := api.CreateSessionWithOptions(context.Background(), SessionCreateOptions{
		Title:              "Container Session",
		WorkspaceBindingID: "binding-container",
		SwarmID:            "container-swarm",
		TargetKind:         "container",
		TargetRelationship: "child",
	})
	if err == nil {
		t.Fatalf("CreateSessionWithOptions() succeeded, want retired local-container error")
	}
}

func TestCreateSessionWithOptionsPostsTUICWDPrimaryPayloadWithoutBinding(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")

	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Swarm-Client") != "swarmtui" {
			t.Fatalf("X-Swarm-Client = %q, want swarmtui", r.Header.Get("X-Swarm-Client"))
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"session": map[string]any{"id": "session-1", "workspace_path": "/cwd", "workspace_name": "cwd", "title": "New Session", "mode": "plan"},
		})
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	if _, err := api.CreateSessionWithOptions(context.Background(), SessionCreateOptions{
		Title:         "New Session",
		WorkspacePath: "/cwd",
		SwarmID:       "host-swarm",
		TUIPrimaryCWD: true,
		Preference: ModelPreference{
			Provider: "anthropic",
			Model:    "claude",
			Thinking: "auto",
		},
	}); err != nil {
		t.Fatalf("CreateSessionWithOptions() error = %v", err)
	}
	if _, ok := body["workspace_binding_id"]; ok {
		t.Fatalf("workspace_binding_id present in TUI cwd create: %#v", body["workspace_binding_id"])
	}
	if got, _ := body["workspace_path"].(string); got != "/cwd" {
		t.Fatalf("workspace_path = %q, want /cwd", got)
	}
	if got, _ := body["worktree_mode"].(string); got != "off" {
		t.Fatalf("worktree_mode = %q, want off", got)
	}
	for _, key := range []string{"worktree_use_current_branch", "worktree_base_branch", "worktree_branch_name"} {
		if _, ok := body[key]; ok {
			t.Fatalf("%s present in TUI cwd create: %#v", key, body[key])
		}
	}
}

func TestCreateSessionWithOptionsPostsWorktreeOnPayloadFields(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")

	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Swarm-Client") != "swarmtui" {
			t.Fatalf("X-Swarm-Client = %q, want swarmtui", r.Header.Get("X-Swarm-Client"))
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"session": map[string]any{"id": "session-1", "workspace_path": "/host/workspace", "workspace_name": "Workspace", "title": "New Session", "mode": "plan"},
		})
	}))
	defer server.Close()

	useCurrentBranch := false
	api := New(server.URL)
	api.SetToken("test-token")
	if _, err := api.CreateSessionWithOptions(context.Background(), SessionCreateOptions{
		Title:                    "New Session",
		WorkspacePath:            "/host/workspace",
		WorkspaceBindingID:       "binding-host",
		SwarmID:                  "host-swarm",
		WorktreeMode:             "on",
		WorktreeUseCurrentBranch: &useCurrentBranch,
		WorktreeBaseBranch:       "main",
		WorktreeBranchName:       "agent",
		Preference: ModelPreference{
			Provider: "anthropic",
			Model:    "claude",
			Thinking: "auto",
		},
	}); err != nil {
		t.Fatalf("CreateSessionWithOptions() error = %v", err)
	}
	if got, _ := body["worktree_mode"].(string); got != "on" {
		t.Fatalf("worktree_mode = %q, want on", got)
	}
	if got, ok := body["worktree_use_current_branch"].(bool); !ok || got {
		t.Fatalf("worktree_use_current_branch = %#v, want false", body["worktree_use_current_branch"])
	}
	if got, _ := body["worktree_base_branch"].(string); got != "main" {
		t.Fatalf("worktree_base_branch = %q, want main", got)
	}
	if got, _ := body["worktree_branch_name"].(string); got != "agent" {
		t.Fatalf("worktree_branch_name = %q, want agent", got)
	}
}
