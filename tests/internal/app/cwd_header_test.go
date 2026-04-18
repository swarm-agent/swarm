package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func TestOpenChatSessionUsesCurrentDirectoryNameWhenWorkspaceRootDiffers(t *testing.T) {
	var createReq struct {
		WorkspacePath string `json:"workspace_path"`
		WorkspaceName string `json:"workspace_name"`
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions":
			if err := json.NewDecoder(r.Body).Decode(&createReq); err != nil {
				t.Fatalf("decode create-session request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"session": map[string]any{
					"id":             "session-test",
					"workspace_path": "/tmp/runway",
					"workspace_name": "swarm-go",
					"title":          "New Session",
					"mode":           "plan",
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/session-test/preference":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"provider": "openai",
				"model":    "gpt-5",
				"thinking": "medium",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/session-test/mode":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": "session-test", "mode": "plan"})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/sessions/session-test/messages"):
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": "session-test", "messages": []any{}})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/sessions/session-test/permissions"):
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": "session-test", "permissions": []any{}})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "not found"})
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	a := &App{
		home:          ui.NewHomePage(model.EmptyHome()),
		route:         "home",
		config:        defaultAppConfig(),
		api:           client.New(server.URL),
		startupCWD:    "/tmp/runway",
		activePath:    "/tmp/runway",
		workspacePath: "/tmp/swarm-go",
		homeModel: model.HomeModel{
			CWD:           "/tmp/runway",
			ServerMode:    "single",
			ActiveAgent:   "swarm",
			ModelProvider: "openai",
			ModelName:     "gpt-5",
			ThinkingLevel: "medium",
			ContextWindow: 200000,
			Workspaces: []model.Workspace{{
				Name:        "swarm-go",
				Path:        "/tmp/swarm-go",
				Directories: []string{"/tmp/swarm-go", "/tmp/runway"},
				Active:      true,
			}},
			Directories: []model.DirectoryItem{
				{Name: "runway", Path: "/tmp/runway", ResolvedPath: "/tmp/runway", IsWorkspace: false},
				{Name: "swarm-go", Path: "/tmp/swarm-go", ResolvedPath: "/tmp/swarm-go", IsWorkspace: true},
			},
		},
	}
	a.home.SetSessionMode("plan")

	if err := a.openChatSession("", "check cwd"); err != nil {
		t.Fatalf("openChatSession() error = %v", err)
	}

	if got := strings.TrimSpace(createReq.WorkspacePath); got != "/tmp/runway" {
		t.Fatalf("create workspace_path = %q, want /tmp/runway", got)
	}
	if got := strings.TrimSpace(createReq.WorkspaceName); got != "runway" {
		t.Fatalf("create workspace_name = %q, want runway", got)
	}
	if len(a.homeModel.RecentSessions) != 1 {
		t.Fatalf("recent sessions len = %d, want 1", len(a.homeModel.RecentSessions))
	}
	if got := strings.TrimSpace(a.homeModel.RecentSessions[0].WorkspaceName); got != "runway" {
		t.Fatalf("recent session workspace name = %q, want runway", got)
	}
	if a.route != "chat" || a.chat == nil {
		t.Fatalf("expected chat route after opening session")
	}
}

func TestOpenSessionSummaryRepairsStaleWorkspaceNameForLinkedDirectory(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/session-stale/preference":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"provider": "openai",
				"model":    "gpt-5",
				"thinking": "medium",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/session-stale/mode":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": "session-stale", "mode": "plan"})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/sessions/session-stale/messages"):
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": "session-stale", "messages": []any{}})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/sessions/session-stale/permissions"):
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": "session-stale", "permissions": []any{}})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "not found"})
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	a := &App{
		home:          ui.NewHomePage(model.EmptyHome()),
		route:         "home",
		config:        defaultAppConfig(),
		api:           client.New(server.URL),
		startupCWD:    "/tmp/runway",
		activePath:    "/tmp/runway",
		workspacePath: "/tmp/swarm-go",
		homeModel: model.HomeModel{
			CWD: "/tmp/runway",
			Workspaces: []model.Workspace{{
				Name:        "swarm-go",
				Path:        "/tmp/swarm-go",
				Directories: []string{"/tmp/swarm-go", "/tmp/runway"},
				Active:      true,
			}},
			Directories: []model.DirectoryItem{
				{Name: "runway", Path: "/tmp/runway", ResolvedPath: "/tmp/runway", IsWorkspace: false},
				{Name: "swarm-go", Path: "/tmp/swarm-go", ResolvedPath: "/tmp/swarm-go", IsWorkspace: true},
			},
		},
	}

	if err := a.openSessionSummary(model.SessionSummary{
		ID:            "session-stale",
		WorkspacePath: "/tmp/runway",
		WorkspaceName: "swarm-go",
		Title:         "Loaded Session",
		Mode:          "plan",
		Preference: client.ModelPreference{
			Provider: "openai",
			Model:    "gpt-5",
			Thinking: "medium",
		},
	}, ""); err != nil {
		t.Fatalf("openSessionSummary() error = %v", err)
	}

	if got := strings.TrimSpace(a.homeModel.RecentSessions[0].WorkspaceName); got != "runway" {
		t.Fatalf("reloaded session workspace name = %q, want runway", got)
	}
	if got := strings.TrimSpace(a.homeModel.RecentSessions[0].WorkspacePath); got != "/tmp/runway" {
		t.Fatalf("reloaded session workspace path = %q, want /tmp/runway", got)
	}
	if got := strings.TrimSpace(a.workspacePath); got != "/tmp/swarm-go" {
		t.Fatalf("workspacePath = %q, want /tmp/swarm-go", got)
	}
}

func TestRefreshHomeModelMarksLinkedCWDAsDirectoryNotWorkspaceRoot(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/vault/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"enabled": false, "unlocked": false})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/health":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "mode": "single", "bypass_permissions": false})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/worktrees":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "worktrees": map[string]any{"enabled": false}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/workspace/overview":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"current_workspace": map[string]any{
					"workspace_path": "/tmp/swarm-go",
					"resolved_path":  "/tmp/swarm-go",
					"workspace_name": "swarm-go",
				},
				"workspaces": []map[string]any{{
					"path":           "/tmp/swarm-go",
					"workspace_name": "swarm-go",
					"directories":    []string{"/tmp/swarm-go", "/tmp/runway"},
					"active":         true,
				}},
				"directories": []map[string]any{{
					"name": "runway",
					"path": "/tmp/runway",
				}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/providers":
			_ = json.NewEncoder(w).Encode(map[string]any{"providers": []map[string]any{{"id": "openai", "runnable": true}}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/model":
			_ = json.NewEncoder(w).Encode(map[string]any{"provider": "openai", "model": "gpt-5", "thinking": "medium"})
		case r.Method == http.MethodGet && r.URL.Path == "/v2/agents":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "state": map[string]any{"profiles": []map[string]any{{"name": "swarm", "mode": "primary", "enabled": true}}, "active_primary": "swarm"}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/context/sources":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "report": map[string]any{"rules": []any{}, "skills": []any{}}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions":
			_ = json.NewEncoder(w).Encode(map[string]any{"sessions": []any{}})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "not found"})
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	a := &App{
		home:       ui.NewHomePage(model.EmptyHome()),
		route:      "home",
		config:     defaultAppConfig(),
		api:        client.New(server.URL),
		startupCWD: "/tmp/runway",
		activePath: "/tmp/runway",
		reloadCh:   make(chan homeReloadResult, 1),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	next, err := a.refreshHomeModel(ctx)
	if err != nil {
		t.Fatalf("refreshHomeModel() error = %v", err)
	}

	if got := strings.TrimSpace(next.CWD); got != "/tmp/runway" {
		t.Fatalf("next.CWD = %q, want /tmp/runway", got)
	}
	if len(next.Directories) == 0 {
		t.Fatalf("next.Directories = empty, want cwd entry")
	}
	found := false
	for _, directory := range next.Directories {
		if strings.TrimSpace(directory.ResolvedPath) != "/tmp/runway" {
			continue
		}
		found = true
		if directory.IsWorkspace {
			t.Fatalf("cwd directory IsWorkspace = true, want false for linked cwd")
		}
		break
	}
	if !found {
		t.Fatalf("next.Directories missing cwd entry for /tmp/runway: %+v", next.Directories)
	}
}

func TestRefreshHomeModelPrefersStandaloneChildWorkspaceOverUmbrellaLink(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/vault/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"enabled": false, "unlocked": false})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/health":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "mode": "single", "bypass_permissions": false})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/worktrees":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "worktrees": map[string]any{"enabled": false}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/workspace/overview":
			if got := r.URL.Query().Get("cwd"); got != "/tmp/swarm-go" {
				t.Fatalf("workspace overview cwd = %q, want /tmp/swarm-go", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"current_workspace": map[string]any{
					"workspace_path": "/tmp/swarm-go",
					"resolved_path":  "/tmp/swarm-go",
					"workspace_name": "swarm-go",
				},
				"workspaces": []map[string]any{
					{
						"path":           "/tmp/swarm-web",
						"workspace_name": "swarm-web",
						"directories":    []string{"/tmp/swarm-web", "/tmp/swarm-go", "/tmp/swarm-web/ui"},
						"active":         false,
					},
					{
						"path":           "/tmp/swarm-go",
						"workspace_name": "swarm-go",
						"directories":    []string{"/tmp/swarm-go"},
						"active":         true,
					},
				},
				"directories": []map[string]any{{
					"name": "swarm-web-ui",
					"path": "/tmp/swarm-web/ui",
				}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/providers":
			_ = json.NewEncoder(w).Encode(map[string]any{"providers": []map[string]any{{"id": "openai", "runnable": true}}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/model":
			_ = json.NewEncoder(w).Encode(map[string]any{"provider": "openai", "model": "gpt-5", "thinking": "medium"})
		case r.Method == http.MethodGet && r.URL.Path == "/v2/agents":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "state": map[string]any{"profiles": []map[string]any{{"name": "swarm", "mode": "primary", "enabled": true}}, "active_primary": "swarm"}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/context/sources":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "report": map[string]any{"rules": []any{}, "skills": []any{}}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions":
			_ = json.NewEncoder(w).Encode(map[string]any{"sessions": []any{}})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "not found"})
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	a := &App{
		home:       ui.NewHomePage(model.EmptyHome()),
		config:     defaultAppConfig(),
		api:        client.New(server.URL),
		startupCWD: "/tmp/swarm-go",
		activePath: "/tmp/swarm-go",
		homeModel:  model.HomeModel{CWD: "/tmp/swarm-go"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	next, err := a.refreshHomeModel(ctx)
	if err != nil {
		t.Fatalf("refreshHomeModel() error = %v", err)
	}

	if got := strings.TrimSpace(a.workspacePath); got != "/tmp/swarm-go" {
		t.Fatalf("workspacePath = %q, want /tmp/swarm-go", got)
	}
	if len(next.Workspaces) != 2 {
		t.Fatalf("workspaces len = %d, want 2", len(next.Workspaces))
	}
	childFound := false
	for _, ws := range next.Workspaces {
		if strings.TrimSpace(ws.Path) != "/tmp/swarm-go" {
			continue
		}
		childFound = true
		if !ws.Active {
			t.Fatalf("child workspace should be active: %+v", ws)
		}
		if len(ws.Directories) != 1 || ws.Directories[0] != "/tmp/swarm-go" {
			t.Fatalf("child workspace directories = %#v, want only /tmp/swarm-go", ws.Directories)
		}
		if strings.Contains(strings.Join(ws.Directories, ","), "/tmp/swarm-web/ui") {
			t.Fatalf("child workspace inherited umbrella directories: %#v", ws.Directories)
		}
	}
	if !childFound {
		t.Fatalf("missing child workspace in %+v", next.Workspaces)
	}
}
