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

func dumpScreenText(screen tcell.Screen, width, height int) string {
	var out strings.Builder
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			main, _, _, _ := screen.GetContent(x, y)
			if main == 0 {
				main = ' '
			}
			out.WriteRune(main)
		}
		out.WriteByte('\n')
	}
	return out.String()
}

func TestHandleAuthModalRefreshPreservesCopilotHint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/providers":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"providers":[{"id":"copilot","ready":true,"runnable":true,"reason":"authenticated as swarm-agent on github.com. New Copilot runs inherit this sidecar user until changed via copilot login.","run_reason":"","default_model":"","default_thinking":"","auth_methods":[{"id":"sidecar","label":"Copilot CLI login","credential_type":"sidecar","description":"Run copilot login in terminal, then refresh to verify auth.getStatus."}]}]}`))
			return
		case "/v1/auth/credentials":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"provider":"","query":"","total":0,"records":[],"providers":[]}`))
			return
		default:
			http.NotFound(w, r)
			return
		}
	}))
	defer srv.Close()

	a := &App{
		home:   ui.NewHomePage(model.EmptyHome()),
		route:  "home",
		api:    client.New(srv.URL),
		config: defaultAppConfig(),
	}
	a.home.ShowAuthModal()

	a.handleAuthModalAction(ui.AuthModalAction{
		Kind:       ui.AuthModalActionRefresh,
		StatusHint: "Checking Copilot sidecar auth status (auth.getStatus).",
	})

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()

	screen.SetSize(120, 28)
	a.home.Draw(screen)
	text := dumpScreenText(screen, 120, 28)
	if !strings.Contains(text, "Copilot auth status:") {
		t.Fatalf("expected copilot status line, got:\n%s", text)
	}
	if !strings.Contains(text, "swarm-agent") {
		t.Fatalf("expected authenticated login in status line, got:\n%s", text)
	}
}
