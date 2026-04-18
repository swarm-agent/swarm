package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func TestMCPModalDrawsOnNarrowScreen(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowMCPModal()
	p.SetMCPModalData([]MCPModalServer{{ID: "srv-1", Name: "Local MCP", Transport: "stdio", Command: "uvx", Enabled: true}})

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	w, h := 54, 16
	screen.SetSize(w, h)
	p.drawMCPModal(screen)

	text := dumpScreenText(screen, w, h)
	if !strings.Contains(text, "MCP Servers") {
		t.Fatalf("expected mcp modal on narrow screen, got:\n%s", text)
	}
}
