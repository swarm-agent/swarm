package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestWorkspaceScopeModalDrawsOnNarrowScreen(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})
	page.workspaceScopeVisible = true
	page.workspaceScopePermission = "perm_scope_narrow"
	page.workspaceScopeTitle = "Allow read access?"
	page.workspaceScopeSummary = "Allow read access for this chat session only."
	page.workspaceScopeToolName = "read"
	page.workspaceScopeAccessLabel = "read access"
	page.workspaceScopeRequestedPath = "/tmp/demo"
	page.workspaceScopeResolvedPath = "/tmp/demo"
	page.workspaceScopeDirectory = "/tmp/demo"
	page.workspaceScopeWorkspaceSaved = false

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	width, height := 44, 14
	screen.SetSize(width, height)
	page.Draw(screen)

	text := dumpScreenText(screen, width, height)
	if !strings.Contains(text, "Allow read access?") {
		t.Fatalf("expected workspace scope modal header on narrow screen, got:\n%s", text)
	}
	if !strings.Contains(text, "Session") && !strings.Contains(text, "confirms") {
		t.Fatalf("expected workspace scope modal controls on narrow screen, got:\n%s", text)
	}
}
