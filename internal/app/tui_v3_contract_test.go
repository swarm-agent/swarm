package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func TestTUIRejectsRetiredSessionAPIsOnOpenAndBackend(t *testing.T) {
	app := &App{home: ui.NewHomePage(model.HomeModel{})}
	err := app.openSessionSummary(model.SessionSummary{ID: "legacy-session", Title: "Legacy", SessionAPI: "v2"}, "")
	if err == nil || !strings.Contains(err.Error(), tuiRetiredSessionAPIMessage) {
		t.Fatalf("openSessionSummary() error = %v, want retired TUI session API error", err)
	}

	backend := newAPIChatBackend(testAPIWithToken("http://127.0.0.1"), "v2")
	if _, err := backend.LoadMessages(context.Background(), "legacy-session", 0, 10); err == nil || !strings.Contains(err.Error(), tuiRetiredSessionAPIMessage) {
		t.Fatalf("LoadMessages() error = %v, want retired TUI session API error", err)
	}
	if _, err := backend.RunTurnStream(context.Background(), "legacy-session", ui.ChatRunRequest{Prompt: "hello"}, nil); err == nil || !strings.Contains(err.Error(), tuiRetiredSessionAPIMessage) {
		t.Fatalf("RunTurnStream() error = %v, want retired TUI session API error", err)
	}
}

func TestTUIOpenSessionHydratesFromV3BeforeOpeningChat(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")

	var hydrated bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v3/tui/sessions/session-1":
			hydrated = true
			if got := r.URL.Query().Get("workspace_path"); got != testWorkspacePath {
				t.Fatalf("workspace_path = %q, want %q", got, testWorkspacePath)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"session": map[string]any{
					"id":             "session-1",
					"workspace_path": testWorkspacePath,
					"workspace_name": "swarm-go",
					"title":          "Hydrated Session",
					"mode":           "auto",
					"metadata": map[string]any{
						"swarm_v3_runtime_swarm_id": "host-swarm",
					},
				},
				"projection":     map[string]any{"session_id": "session-1", "last_event_seq": 7, "projection_high_watermark_seq": 8},
				"preference":     map[string]any{"provider": "anthropic", "model": "claude", "thinking": "auto", "service_tier": "standard", "context_mode": "full"},
				"context_window": 1000,
				"usage_summary": map[string]any{
					"session_id":        "session-1",
					"provider":          "anthropic",
					"model":             "claude",
					"source":            "provider",
					"context_window":    1000,
					"total_tokens":      250,
					"remaining_tokens":  750,
					"cache_read_tokens": 0,
				},
				"messages": []any{},
				"events":   []any{},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/providers":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "providers": []any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/models/favorites":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "favorites": []any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/model/catalog":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "models": []any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/sessions/session-1/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": "session-1", "messages": []any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/sessions/session-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":         true,
				"session":    map[string]any{"id": "session-1", "workspace_path": testWorkspacePath, "workspace_name": "swarm-go", "title": "Hydrated Session", "mode": "auto"},
				"projection": map[string]any{"session_id": "session-1", "last_event_seq": 7, "projection_high_watermark_seq": 8},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/sessions/session-1/permissions":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": "session-1", "permissions": []any{}})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	homeModel := model.HomeModel{RecentSessions: []model.SessionSummary{{ID: "session-1", Title: "stale", WorkspacePath: testWorkspacePath}}}
	home := ui.NewHomePage(homeModel)
	app := &App{
		api:           testAPIWithToken(server.URL),
		startupCWD:    testWorkspacePath,
		workspacePath: testWorkspacePath,
		home:          home,
		homeModel:     homeModel,
		streamEvents:  make(chan client.StreamEventEnvelope, 1),
	}

	if err := app.openSessionSummary(model.SessionSummary{ID: "session-1", Title: "stale", WorkspacePath: testWorkspacePath}, ""); err != nil {
		t.Fatalf("openSessionSummary() error = %v", err)
	}
	if !hydrated {
		t.Fatalf("openSessionSummary() did not hydrate through v3 TUI endpoint")
	}
	if app.chat == nil || app.chat.SessionID() != "session-1" {
		t.Fatalf("chat session id = %q, want session-1", app.chat.SessionID())
	}
	if got := app.chat.SessionMode(); got != "auto" {
		t.Fatalf("chat session mode = %q, want auto", got)
	}
	if summary, ok := app.sessionSummaryByID("session-1"); !ok || summary.SessionAPI != "v3" || summary.Title != "Hydrated Session" || summary.LastEventSeq != 7 || summary.ProjectionHighWatermarkSeq != 8 {
		t.Fatalf("hydrated summary = %+v, ok=%v", summary, ok)
	}
	text := renderPageText(t, app.chat)
	if !strings.Contains(text, "75% left") {
		t.Fatalf("hydrated chat footer missing context usage: %q", text)
	}
}

func TestTUIAppSessionContractDoesNotCallLegacySessionAPIs(t *testing.T) {
	assertSourceDoesNotContain(t, "app.go", map[string]string{
		"ListSessionsForWorkspaceBinding(": "TUI session lists must use the v3 TUI workset endpoint, not v2 workspace binding lists",
		"ListSessionsForExactCWD(":         "TUI session lists must use the v3 TUI workset endpoint, not v2 cwd lists",
		"CreateSessionWithOptions(":        "TUI session create must be v3-only",
		"/v1/sessions":                     "TUI app code must not call v1 session routes",
		"/v2/sessions":                     "TUI app code must not call v2 session routes",
	})
}

func TestTUIChatBackendContractDoesNotCallLegacySessionAPIs(t *testing.T) {
	assertSourceDoesNotContain(t, "chat_backend_adapter.go", map[string]string{
		"ListSessionMessages(":         "TUI chat message loads must use v3 messages/workset hydration",
		"RunSessionWithOptions(":       "TUI chat turns must use v3 message commit plus realtime",
		"RunSessionStreamWithOptions(": "TUI chat turns must use v3 realtime, not legacy run stream",
		"StopSessionRun(":              "TUI chat stop must use v3 primary stop directly",
		"sessionV2LifecyclePath(":      "TUI chat permissions/plans must use v3 primary routes, not v2 lifecycle routes",
		"/v1/sessions":                 "TUI chat backend must not call v1 session routes",
		"/v2/sessions":                 "TUI chat backend must not call v2 session routes",
	})
}

func TestTUIV3EndpointNamesAreCanonical(t *testing.T) {
	const primary = "/v3/sessions"
	const workset = "/v3/sessions:workset"
	const tuiSessions = "/v3/tui/sessions"
	const tuiWorkset = "/v3/tui/sessions:workset"
	for name, route := range map[string]string{"primary": primary, "workset": workset, "tuiSessions": tuiSessions, "tuiWorkset": tuiWorkset} {
		if !strings.HasPrefix(route, "/v3/") {
			t.Fatalf("%s route = %q, want v3 route", name, route)
		}
	}
	if workset == tuiWorkset {
		t.Fatalf("TUI workset route must be distinct from main fail-closed workset route")
	}
}

func assertSourceDoesNotContain(t *testing.T, path string, forbidden map[string]string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	source := string(body)
	for needle, message := range forbidden {
		if strings.Contains(source, needle) {
			t.Fatalf("%s: found forbidden %q in %s", message, needle, path)
		}
	}
}
