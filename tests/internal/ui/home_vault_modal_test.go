package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func TestVaultModalDrawsOnNarrowScreen(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.vaultModal.Visible = true
	p.vaultModal.Mode = vaultModalModeStatus
	p.vaultModal.Status = "Vault is configured."

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	w, h := 44, 14
	screen.SetSize(w, h)
	p.drawVaultModal(screen)

	text := dumpScreenText(screen, w, h)
	if !strings.Contains(text, "Vault") {
		t.Fatalf("expected vault modal on narrow screen, got:\n%s", text)
	}
}
