package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
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

func TestHomeFooterRendersSwarmNotificationBadge(t *testing.T) {
	page := NewHomePage(model.HomeModel{ServerMode: "local"})
	page.SetSwarmName("swarm.name")
	page.SetSwarmNotificationCount(3)

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	w, h := 120, 24
	screen.SetSize(w, h)

	page.Draw(screen)
	text := dumpScreenText(screen, w, h)
	if !strings.Contains(text, "swarm.name !3") {
		t.Fatalf("expected home footer to include notification badge, got:\n%s", text)
	}
}

func TestChatFooterRendersSwarmNotificationBadge(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		SessionMode:    "auto",
		AuthConfigured: true,
		SwarmName:      "swarm.name",
	})
	page.SetSwarmNotificationCount(3)

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	w, h := 120, 24
	screen.SetSize(w, h)

	page.Draw(screen)
	text := dumpScreenText(screen, w, h)
	if !strings.Contains(text, "swarm.name !3") {
		t.Fatalf("expected chat footer to include notification badge, got:\n%s", text)
	}
}

func TestHomeFooterKeepsNotificationCountVisibleForLongSwarmNames(t *testing.T) {
	page := NewHomePage(model.HomeModel{ServerMode: "local"})
	page.SetSwarmName("very.long.swarm.name")
	page.SetSwarmNotificationCount(12)

	tokens := page.homeFooterTokens()
	if len(tokens) == 0 {
		t.Fatal("homeFooterTokens() returned no tokens")
	}
	if !strings.Contains(tokens[0].Text, "!12") {
		t.Fatalf("home footer primary token = %q, want notification count to stay visible", tokens[0].Text)
	}
}

func TestChatFooterKeepsNotificationCountVisibleForLongSwarmNames(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		SessionMode:    "auto",
		AuthConfigured: true,
		SwarmName:      "very.long.swarm.name",
	})
	page.SetSwarmNotificationCount(12)

	tokens := page.footerSettingsTokens()
	if len(tokens) == 0 {
		t.Fatal("footerSettingsTokens() returned no tokens")
	}
	if !strings.Contains(tokens[0].Text, "!12") {
		t.Fatalf("chat footer primary token = %q, want notification count to stay visible", tokens[0].Text)
	}
}
