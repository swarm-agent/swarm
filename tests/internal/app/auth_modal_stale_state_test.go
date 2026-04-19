package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func TestRefreshAuthModalDataClearsStaleStateBeforeReload(t *testing.T) {
	responses := map[string]string{
		"/v1/providers":        `{"providers":[{"id":"codex","ready":true,"runnable":true,"reason":"authenticated"}]}`,
		"/v1/auth/credentials": `{"provider":"","query":"","total":1,"records":[{"id":"cred-1","provider":"codex","active":true,"auth_type":"api_key","label":"primary"}],"providers":["codex"]}`,
		"/v1/agents":           `{"profiles":[{"name":"alpha","provider":"codex"}]}`,
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, ok := responses[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	a := &App{
		home:   ui.NewHomePage(model.EmptyHome()),
		route:  "home",
		api:    client.New(srv.URL),
		config: defaultAppConfig(),
	}
	a.home.ShowAuthModal()
	a.refreshAuthModalData("initial load")

	a.home.SetAuthModalStatus("stale delete warning")
	a.home.SetAuthModalAgentProfiles([]ui.AgentModalProfile{{Name: "stale", Provider: "codex"}})

	responses["/v1/providers"] = `{"providers":[]}`
	responses["/v1/auth/credentials"] = `{"provider":"","query":"","total":0,"records":[],"providers":[]}`
	responses["/v1/agents"] = `{"profiles":[]}`

	a.refreshAuthModalData("after delete")

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(120, 28)
	a.home.Draw(screen)
	text := dumpScreenText(screen, 120, 28)

	if strings.Contains(text, "Delete Credential?") {
		t.Fatalf("did not expect stale delete overlay after refresh, got:\n%s", text)
	}
	if !strings.Contains(text, "no providers") {
		t.Fatalf("expected empty auth provider state after refresh, got:\n%s", text)
	}
}

func TestAuthDeleteFlowClearsTUIStateEndToEnd(t *testing.T) {
	var deleteCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/providers":
			if deleteCalls == 0 {
				_, _ = w.Write([]byte(`{"providers":[{"id":"codex","ready":true,"runnable":true,"reason":"authenticated"}]}`))
			} else {
				_, _ = w.Write([]byte(`{"providers":[]}`))
			}
		case "/v1/auth/credentials":
			if deleteCalls == 0 {
				_, _ = w.Write([]byte(`{"provider":"","query":"","total":1,"records":[{"id":"cred-1","provider":"codex","active":true,"auth_type":"api_key","label":"primary"}],"providers":["codex"]}`))
			} else {
				_, _ = w.Write([]byte(`{"provider":"","query":"","total":0,"records":[],"providers":[]}`))
			}
		case "/v1/agents":
			if deleteCalls == 0 {
				_, _ = w.Write([]byte(`{"profiles":[{"name":"alpha","provider":"codex"}]}`))
			} else {
				_, _ = w.Write([]byte(`{"profiles":[]}`))
			}
		case "/v1/auth/credentials/delete":
			deleteCalls++
			_, _ = w.Write([]byte(`{"ok":true,"deleted":true,"provider":"codex","id":"cred-1","cleanup":{"provider_unavailable":true,"cleared_global_preference":true,"reset_agents":["alpha"]}}`))
		case "/v1/home":
			_, _ = w.Write([]byte(`{"model":{"recent_sessions":[],"active_sessions":[],"saved_workspaces":[],"worktrees":[],"recent_workspaces":[]}}`))
		case "/v1/model":
			_, _ = w.Write([]byte(`{"preference":{"provider":"","model":"","thinking":"","updated_at":0},"context_window":0,"max_output_tokens":0,"catalog_source":"","catalog_fetched_at":0,"catalog_expires_at":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := &App{
		home:     ui.NewHomePage(model.EmptyHome()),
		route:    "home",
		api:      client.New(srv.URL),
		config:   defaultAppConfig(),
		reloadCh: make(chan homeReloadResult, 1),
	}
	a.home.ShowAuthModal()
	a.refreshAuthModalData("Loading auth manager...")
	a.home.SetAuthModalStatus("stale delete warning")
	a.handleAuthModalAction(ui.AuthModalAction{Kind: ui.AuthModalActionDelete, Provider: "codex", ID: "cred-1"})

	if deleteCalls != 1 {
		t.Fatalf("delete calls = %d, want 1", deleteCalls)
	}
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(120, 28)
	a.home.Draw(screen)
	text := dumpScreenText(screen, 120, 28)

	if strings.Contains(text, "Delete Credential?") {
		t.Fatalf("did not expect stale delete overlay after delete flow, got:\n%s", text)
	}
	if !strings.Contains(text, "no providers") {
		t.Fatalf("expected empty auth provider state after delete flow, got:\n%s", text)
	}
}
