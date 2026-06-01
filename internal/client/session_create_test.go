package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateSessionWithOptionsOmitsAuthorityFieldsWhenBindingIsPresent(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")

	var gotPath string
	var gotSwarmID string
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSwarmID = r.URL.Query().Get("swarm_id")
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
		})
	}))
	defer server.Close()

	api := New(server.URL)
	_, err := api.CreateSessionWithOptions(context.Background(), SessionCreateOptions{
		Title:                "New Session",
		WorkspacePath:        "/host/workspace",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/workspaces/swarm-go",
		WorkspaceName:        "Workspace",
		WorkspaceBindingID:   "binding-child",
		Mode:                 "auto",
		SwarmID:              "child-swarm",
		Preference: ModelPreference{
			Provider: "anthropic",
			Model:    "claude",
			Thinking: "auto",
		},
	})
	if err != nil {
		t.Fatalf("CreateSessionWithOptions() error = %v", err)
	}
	if gotPath != "/v1/sessions" {
		t.Fatalf("request path = %q, want /v1/sessions", gotPath)
	}
	if gotSwarmID != "child-swarm" {
		t.Fatalf("swarm_id query = %q, want child-swarm", gotSwarmID)
	}
	if got, _ := body["workspace_binding_id"].(string); got != "binding-child" {
		t.Fatalf("workspace_binding_id = %q, want binding-child", got)
	}
	for _, key := range []string{"workspace_name", "workspace_path", "host_workspace_path", "runtime_workspace_path"} {
		if _, ok := body[key]; ok {
			t.Fatalf("%s present in workspace-bound create request: %#v", key, body[key])
		}
	}
}
