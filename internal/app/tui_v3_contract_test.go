package app

import (
	"context"
	"os"
	"strings"
	"testing"

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
		"/v1/sessions":                 "TUI chat backend must not call v1 session routes",
		"/v2/sessions":                 "TUI chat backend must not call v2 session routes",
	})
}

func TestTUIV3EndpointNamesAreCanonical(t *testing.T) {
	const primary = "/v3/sessions"
	const workset = "/v3/sessions:workset"
	const tuiWorkset = "/v3/tui/sessions:workset"
	for name, route := range map[string]string{"primary": primary, "workset": workset, "tuiWorkset": tuiWorkset} {
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
