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

func TestOpenChatSessionCreatePayloadUsesHostRouteAuthority(t *testing.T) {
	got := captureOpenChatSessionCreateRequest(t, model.ChatRoute{ID: "host"}, "host-swarm", "local-binding", testWorkspacePath, "plan")

	if got.bodyString("swarm_id") != "host-swarm" {
		t.Fatalf("swarm_id = %q, want host-swarm", got.bodyString("swarm_id"))
	}
	if got.bodyString("workspace_binding_id") != "local-binding" {
		t.Fatalf("workspace_binding_id = %q, want local-binding", got.bodyString("workspace_binding_id"))
	}
	if got.sessionExecution == nil || got.sessionExecution.RuntimeSwarmID != "host-swarm" || got.sessionExecution.WorkspaceBindingID != "local-binding" {
		t.Fatalf("session execution = %#v, want host-swarm/local-binding", got.sessionExecution)
	}
	assertV2PrimaryCreatePayload(t, got.body, "local-binding", "plan")
	assertCreateMetadataStrictV2Safe(t, got.metadata)
}

func TestOpenChatSessionCreatePayloadUsesTUICWDPrimaryExceptionOnlyForHostRoute(t *testing.T) {
	got := captureOpenChatSessionCreateRequestWithWorktreeSettings(t, model.ChatRoute{ID: "host"}, "host-swarm", "", testWorkspacePath, "plan", client.WorktreeSettings{
		WorkspacePath:    testWorkspacePath,
		Enabled:          true,
		UseCurrentBranch: false,
		BaseBranch:       "main",
		BranchName:       "feature/<id>",
	})

	if got.bodyString("swarm_id") != "host-swarm" {
		t.Fatalf("swarm_id = %q, want host-swarm", got.bodyString("swarm_id"))
	}
	if _, ok := got.body["workspace_binding_id"]; ok {
		t.Fatalf("workspace_binding_id present in TUI cwd create: %#v", got.body["workspace_binding_id"])
	}
	if got.bodyString("workspace_path") != testWorkspacePath {
		t.Fatalf("workspace_path = %q, want %q", got.bodyString("workspace_path"), testWorkspacePath)
	}
	if got.bodyString("worktree_mode") != "off" {
		t.Fatalf("worktree_mode = %q, want off", got.bodyString("worktree_mode"))
	}
	for _, key := range []string{"worktree_use_current_branch", "worktree_base_branch", "worktree_branch_name"} {
		if _, ok := got.body[key]; ok {
			t.Fatalf("%s present in TUI cwd create: %#v", key, got.body[key])
		}
	}
}

func TestOpenChatSessionCreatePayloadUsesWorktreeSettingsForBoundHostRoute(t *testing.T) {
	got := captureOpenChatSessionCreateRequestWithWorktreeSettings(t, model.ChatRoute{ID: "host"}, "host-swarm", "local-binding", testWorkspacePath, "plan", client.WorktreeSettings{
		WorkspacePath:    testWorkspacePath,
		Enabled:          true,
		UseCurrentBranch: false,
		BaseBranch:       "main",
		BranchName:       "feature/<id>",
	})

	if got.bodyString("workspace_binding_id") != "local-binding" {
		t.Fatalf("workspace_binding_id = %q, want local-binding", got.bodyString("workspace_binding_id"))
	}
	if got.bodyString("worktree_mode") != "on" {
		t.Fatalf("worktree_mode = %q, want on", got.bodyString("worktree_mode"))
	}
	if gotUseCurrent, ok := got.body["worktree_use_current_branch"].(bool); !ok || gotUseCurrent {
		t.Fatalf("worktree_use_current_branch = %#v, want false", got.body["worktree_use_current_branch"])
	}
	if got.bodyString("worktree_base_branch") != "main" {
		t.Fatalf("worktree_base_branch = %q, want main", got.bodyString("worktree_base_branch"))
	}
	if got.bodyString("worktree_branch_name") != "feature" {
		t.Fatalf("worktree_branch_name = %q, want feature", got.bodyString("worktree_branch_name"))
	}
	for _, key := range []string{"workspace_name", "workspace_path", "host_workspace_path", "runtime_workspace_path", "target_swarm_id", "routing_hint"} {
		if _, ok := got.body[key]; ok {
			t.Fatalf("create payload contains strict-v2-invalid authority field %q in %#v", key, got.body)
		}
	}
}

func TestOpenChatSessionCreatePayloadUsesSelectedRouteAuthority(t *testing.T) {
	route := model.ChatRoute{
		ID:                   testRemoteRouteID,
		Label:                "Child Desk",
		SwarmID:              "child-swarm",
		WorkspaceBindingID:   "binding-1",
		HostWorkspacePath:    testWorkspacePath,
		RuntimeWorkspacePath: "/workspaces/swarm-go",
	}
	got := captureOpenChatSessionCreateRequest(t, route, "host-swarm", "local-binding", testWorkspacePath, "auto")

	if got.bodyString("swarm_id") != "child-swarm" {
		t.Fatalf("swarm_id = %q, want child-swarm", got.bodyString("swarm_id"))
	}
	if got.bodyString("workspace_binding_id") != "binding-1" {
		t.Fatalf("workspace_binding_id = %q, want binding-1", got.bodyString("workspace_binding_id"))
	}
	if got.sessionExecution == nil || got.sessionExecution.RuntimeSwarmID != "child-swarm" || got.sessionExecution.WorkspaceBindingID != "binding-1" {
		t.Fatalf("session execution = %#v, want child-swarm/binding-1", got.sessionExecution)
	}
	assertV2PrimaryCreatePayload(t, got.body, "binding-1", "auto")
	assertCreateMetadataStrictV2Safe(t, got.metadata)
}

type capturedCreateRequest struct {
	body             map[string]any
	metadata         map[string]any
	sessionExecution *client.SessionExecutionV2
}

func (r capturedCreateRequest) bodyString(key string) string {
	value, _ := r.body[key].(string)
	return value
}

func captureOpenChatSessionCreateRequest(t *testing.T, route model.ChatRoute, hostSwarmID, localBindingID, workspacePath, sessionMode string) capturedCreateRequest {
	t.Helper()
	return captureOpenChatSessionCreateRequestWithWorktreeSettings(t, route, hostSwarmID, localBindingID, workspacePath, sessionMode, client.WorktreeSettings{WorkspacePath: workspacePath})
}

func captureOpenChatSessionCreateRequestWithWorktreeSettings(t *testing.T, route model.ChatRoute, hostSwarmID, localBindingID, workspacePath, sessionMode string, worktreeSettings client.WorktreeSettings) capturedCreateRequest {
	t.Helper()
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")

	captured := capturedCreateRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/sessions/primary":
			if r.URL.RawQuery != "" {
				t.Fatalf("unexpected query on create request: %q", r.URL.RawQuery)
			}
			if r.Header.Get("X-Swarm-Token") == "" {
				t.Fatalf("missing X-Swarm-Token on v2 create request")
			}
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
				"session_execution": map[string]any{
					"session_id":              "session-1",
					"execution_class":         "primary",
					"runtime_swarm_id":        captured.bodyString("swarm_id"),
					"runtime_kind":            "host",
					"authority_host_swarm_id": captured.bodyString("swarm_id"),
					"workspace_binding_id":    captured.bodyString("workspace_binding_id"),
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/worktrees":
			if got := r.URL.Query().Get("workspace_path"); got != workspacePath {
				t.Fatalf("worktrees workspace_path = %q, want %q", got, workspacePath)
			}
			settings := worktreeSettings
			if strings.TrimSpace(settings.WorkspacePath) == "" {
				settings.WorkspacePath = workspacePath
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "worktrees": settings})
		case r.Method == http.MethodGet && r.URL.Path == "/v2/sessions/session-1/preference":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"preference": map[string]any{
					"provider": "anthropic",
					"model":    "claude",
					"thinking": "auto",
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v2/sessions/session-1/mode":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "mode": captured.bodyString("mode")})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/providers":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "providers": []any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/models/favorites":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "favorites": []any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/model/catalog":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "models": []any{}})
		case r.Method == http.MethodGet && (r.URL.Path == "/v1/sessions/session-1/messages" || r.URL.Path == "/v2/sessions/session-1/messages"):
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": "session-1", "messages": []any{}})
		case r.Method == http.MethodGet && (r.URL.Path == "/v1/sessions/session-1/usage" || r.URL.Path == "/v2/sessions/session-1/usage"):
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": "session-1", "has_usage_summary": false, "turn_usage_records": []any{}})
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
	homePage := ui.NewHomePage(homeModel)
	homePage.SetSessionMode(sessionMode)
	app := &App{
		api:                 testAPIWithToken(server.URL),
		startupCWD:          workspacePath,
		workspacePath:       workspacePath,
		selectedChatRouteID: route.ID,
		home:                homePage,
		homeModel:           homeModel,
		streamEvents:        make(chan client.StreamEventEnvelope, 1),
	}

	if err := app.openChatSession("New Session", ""); err != nil {
		t.Fatalf("openChatSession() error = %v", err)
	}
	for _, summary := range app.homeModel.RecentSessions {
		if strings.TrimSpace(summary.ID) == "session-1" {
			captured.sessionExecution = summary.SessionExecution
			break
		}
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

func assertV2PrimaryCreatePayload(t *testing.T, body map[string]any, bindingID, mode string) {
	t.Helper()
	allowed := map[string]struct{}{
		"swarm_id": {}, "workspace_binding_id": {}, "workspace_path": {}, "title": {}, "mode": {}, "agent_name": {},
		"metadata": {}, "worktree_mode": {}, "worktree_use_current_branch": {}, "worktree_base_branch": {}, "worktree_branch_name": {}, "preference": {},
	}
	for key := range body {
		if _, ok := allowed[key]; !ok {
			t.Fatalf("create payload contains field outside strict v2 primary schema %q in %#v", key, body)
		}
	}
	if got := bodyString(body, "swarm_id"); got == "" {
		t.Fatalf("swarm_id missing in strict v2 create payload: %#v", body)
	}
	if got := bodyString(body, "mode"); got != mode {
		t.Fatalf("mode = %q, want %q", got, mode)
	}
	if got := bodyString(body, "agent_name"); got == "" {
		t.Fatalf("agent_name missing in strict v2 create payload: %#v", body)
	}
	if got := bodyString(body, "worktree_mode"); got != "off" {
		t.Fatalf("worktree_mode = %q, want off", got)
	}
	for _, key := range []string{"workspace_name", "workspace_path", "host_workspace_path", "runtime_workspace_path", "target_swarm_id", "routing_hint"} {
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
