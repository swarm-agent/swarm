package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func TestOpenChatSessionCreateMetadataIsStrictV2SafeForHostRoute(t *testing.T) {
	got := captureOpenChatSessionCreateRequest(t, model.ChatRoute{ID: "host"}, "host-swarm", "local-binding", testWorkspacePath)

	if got.swarmID != "host-swarm" {
		t.Fatalf("swarm_id query = %q, want host-swarm", got.swarmID)
	}
	if got.bodyString("workspace_binding_id") != "local-binding" {
		t.Fatalf("workspace_binding_id = %q, want local-binding", got.bodyString("workspace_binding_id"))
	}
	assertV2PrimaryCreatePayload(t, got.body, "local-binding")
	assertCreateMetadataStrictV2Safe(t, got.metadata)
}

func TestOpenChatSessionCreateMetadataIsStrictV2SafeForRemoteRoute(t *testing.T) {
	route := model.ChatRoute{
		ID:                   testRemoteRouteID,
		Label:                "Child Desk",
		SwarmID:              "child-swarm",
		WorkspaceBindingID:   "binding-1",
		HostWorkspacePath:    testWorkspacePath,
		RuntimeWorkspacePath: "/workspaces/swarm-go",
	}
	got := captureOpenChatSessionCreateRequest(t, route, "host-swarm", "local-binding", testWorkspacePath)

	if got.swarmID != "child-swarm" {
		t.Fatalf("swarm_id query = %q, want child-swarm", got.swarmID)
	}
	if got.bodyString("workspace_binding_id") != "binding-1" {
		t.Fatalf("workspace_binding_id = %q, want binding-1", got.bodyString("workspace_binding_id"))
	}
	assertV2PrimaryCreatePayload(t, got.body, "binding-1")
	assertCreateMetadataStrictV2Safe(t, got.metadata)
}

type capturedCreateRequest struct {
	swarmID  string
	body     map[string]any
	metadata map[string]any
}

func (r capturedCreateRequest) bodyString(key string) string {
	value, _ := r.body[key].(string)
	return value
}

func captureOpenChatSessionCreateRequest(t *testing.T, route model.ChatRoute, hostSwarmID, localBindingID, workspacePath string) capturedCreateRequest {
	t.Helper()
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")

	captured := capturedCreateRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions":
			captured.swarmID = r.URL.Query().Get("swarm_id")
			if err := json.NewDecoder(r.Body).Decode(&captured.body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if metadata, ok := captured.body["metadata"].(map[string]any); ok {
				captured.metadata = metadata
			}
			mode := "plan"
			if value, _ := captured.body["mode"].(string); strings.TrimSpace(value) != "" {
				mode = value
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"session": map[string]any{
					"id":             "session-1",
					"workspace_path": "/workspaces/session-runtime",
					"workspace_name": "Session Workspace",
					"title":          "New Session",
					"mode":           mode,
					"metadata":       captured.metadata,
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/session-1/preference":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"preference": map[string]any{
					"provider": "anthropic",
					"model":    "claude",
					"thinking": "auto",
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/session-1/mode":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "mode": "plan"})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	homeModel := model.HomeModel{
		ModelProvider:      "anthropic",
		ModelName:          "claude",
		ThinkingLevel:      "auto",
		CurrentSwarmTarget: &model.SwarmTarget{SwarmID: hostSwarmID},
		Workspaces: []model.Workspace{{
			Name:                    "Host Repo",
			Path:                    workspacePath,
			LocalWorkspaceBindingID: localBindingID,
			TopologyRoutes:          topologyRoutesForTestChatRoute(route),
		}},
	}
	app := &App{
		api:                 client.New(server.URL),
		startupCWD:          workspacePath,
		workspacePath:       workspacePath,
		selectedChatRouteID: route.ID,
		home:                ui.NewHomePage(homeModel),
		homeModel:           homeModel,
		streamEvents:        make(chan client.StreamEventEnvelope, 1),
	}

	if err := app.openChatSession("New Session", ""); err != nil {
		t.Fatalf("openChatSession() error = %v", err)
	}
	return captured
}

func topologyRoutesForTestChatRoute(route model.ChatRoute) []model.WorkspaceTopologyRoute {
	if strings.TrimSpace(route.ID) == "host" {
		return nil
	}
	return []model.WorkspaceTopologyRoute{{
		RouteID:              route.ID,
		WorkspaceBindingID:   route.WorkspaceBindingID,
		RuntimeSwarmID:       route.SwarmID,
		RuntimeSwarmName:     route.Label,
		HostWorkspacePath:    route.HostWorkspacePath,
		RuntimeWorkspacePath: route.RuntimeWorkspacePath,
	}}
}

func assertV2PrimaryCreatePayload(t *testing.T, body map[string]any, bindingID string) {
	t.Helper()
	allowed := map[string]struct{}{
		"title": {}, "workspace_binding_id": {}, "mode": {}, "agent_name": {},
		"metadata": {}, "worktree_mode": {}, "preference": {},
	}
	for key := range body {
		if _, ok := allowed[key]; !ok {
			t.Fatalf("create payload contains field outside strict v2 primary schema %q in %#v", key, body)
		}
	}
	for _, key := range []string{"workspace_name", "workspace_path", "host_workspace_path", "runtime_workspace_path"} {
		if _, ok := body[key]; ok {
			t.Fatalf("create payload contains strict-v2-invalid authority field %q in %#v", key, body)
		}
	}
	if got := bodyString(body, "workspace_binding_id"); got != bindingID {
		t.Fatalf("workspace_binding_id = %q, want %q", got, bindingID)
	}
}

func bodyString(body map[string]any, key string) string {
	value, _ := body[key].(string)
	return value
}

func assertCreateMetadataStrictV2Safe(t *testing.T, metadata map[string]any) {
	t.Helper()
	if metadata == nil {
		return
	}
	for key := range metadata {
		normalized := strings.ToLower(strings.TrimSpace(key))
		if strings.Contains(normalized, "swarm") || strings.Contains(normalized, "target") || strings.Contains(normalized, "routing") || strings.Contains(normalized, "route") || strings.Contains(normalized, "path") || strings.Contains(normalized, "workspace_name") || strings.Contains(normalized, "managed_host") {
			t.Fatalf("create metadata contains v2 authority-looking key %q in %#v", key, metadata)
		}
	}
}
