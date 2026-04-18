package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func newNotificationTestApp(apiURL string) *App {
	cfg := defaultAppConfig()
	cfg.Swarm.Name = "swarm.name"
	a := &App{
		api:    client.New(apiURL),
		home:   ui.NewHomePage(model.EmptyHome()),
		chat:   ui.NewChatPage(ui.ChatPageOptions{SessionID: "session-1", AuthConfigured: true, SessionMode: "auto", SwarmName: cfg.Swarm.Name}),
		config: cfg,
	}
	a.home.SetSwarmName(cfg.Swarm.Name)
	return a
}

func TestLoadSwarmNotificationCountUsesUnreadSummary(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/notifications/summary" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"summary": map[string]any{
				"swarm_id":     "swarm-1",
				"total_count":  8,
				"unread_count": 5,
				"active_count": 3,
				"updated_at":   int64(42),
			},
		})
	}))
	defer server.Close()

	a := newNotificationTestApp(server.URL)
	count, err := a.loadSwarmNotificationCount(context.Background())
	if err != nil {
		t.Fatalf("loadSwarmNotificationCount() error = %v", err)
	}
	if count != 5 {
		t.Fatalf("count = %d, want 5", count)
	}
}

func TestApplySwarmStreamEventRefreshesCountsFromNotificationSummary(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")

	var summaryCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/notifications/summary" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		summaryCalls++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"summary": map[string]any{
				"swarm_id":     "swarm-1",
				"total_count":  8,
				"unread_count": 5,
				"active_count": 3,
				"updated_at":   int64(42),
			},
		})
	}))
	defer server.Close()

	a := newNotificationTestApp(server.URL)
	if changed := a.applySwarmStreamEvent(client.StreamEventEnvelope{EventType: "notification.created"}); !changed {
		t.Fatal("applySwarmStreamEvent(notification.created) = false, want true")
	}
	if summaryCalls != 1 {
		t.Fatalf("summary calls = %d, want 1", summaryCalls)
	}
	if a.swarmNotificationCount != 5 {
		t.Fatalf("app swarmNotificationCount = %d, want 5", a.swarmNotificationCount)
	}
	if text := renderPageText(t, a.home); !strings.Contains(text, "swarm.name !5") {
		t.Fatalf("home render missing notification badge:\n%s", text)
	}
	if text := renderPageText(t, a.chat); !strings.Contains(text, "swarm.name !5") {
		t.Fatalf("chat render missing notification badge:\n%s", text)
	}
}

func TestApplySwarmStreamEventEnrollmentCountersStayInSync(t *testing.T) {
	a := newNotificationTestApp("http://127.0.0.1:7781")

	if changed := a.applySwarmStreamEvent(client.StreamEventEnvelope{EventType: "swarm.enrollment.pending"}); !changed {
		t.Fatal("pending event changed = false, want true")
	}
	if a.swarmNotificationCount != 1 {
		t.Fatalf("count after pending = %d, want 1", a.swarmNotificationCount)
	}
	if changed := a.applySwarmStreamEvent(client.StreamEventEnvelope{EventType: "swarm.enrollment.approved"}); !changed {
		t.Fatal("approved event changed = false, want true")
	}
	if a.swarmNotificationCount != 0 {
		t.Fatalf("count after approved = %d, want 0", a.swarmNotificationCount)
	}
	if changed := a.applySwarmStreamEvent(client.StreamEventEnvelope{EventType: "swarm.enrollment.rejected"}); !changed {
		t.Fatal("rejected event changed = false, want true")
	}
	if a.swarmNotificationCount != 0 {
		t.Fatalf("count after rejected = %d, want 0", a.swarmNotificationCount)
	}
}

type drawablePage interface {
	Draw(tcell.Screen)
}

func renderPageText(t *testing.T, page drawablePage) string {
	t.Helper()
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	const width, height = 120, 24
	screen.SetSize(width, height)
	page.Draw(screen)
	return dumpScreenText(screen, width, height)
}

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
