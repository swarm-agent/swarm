package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func TestHomeTipsCatalogApprovedContent(t *testing.T) {
	tips := HomeTips()
	if len(tips) != 31 {
		t.Fatalf("home tips count = %d, want 31", len(tips))
	}
	if tips[0] != "Ask Swarm for three theme variants, then apply your favorite." {
		t.Fatalf("first home tip = %q", tips[0])
	}
	if tips[len(tips)-1] != "Type /tips to hide or show these tips." {
		t.Fatalf("last home tip = %q", tips[len(tips)-1])
	}
}

func TestHomeHeroRespectsTipsVisibility(t *testing.T) {
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(100, 30)

	page := NewHomePage(model.EmptyHome())
	page.Draw(screen)
	if text := dumpHomeTestScreen(screen, 100, 30); !strings.Contains(text, "Tip: Ask Swarm for three theme variants") {
		t.Fatalf("enabled hero missing tip:\n%s", text)
	}

	page.SetHomeTipsVisible(false)
	page.Draw(screen)
	if text := dumpHomeTestScreen(screen, 100, 30); strings.Contains(text, "Tip:") {
		t.Fatalf("disabled hero rendered tip:\n%s", text)
	}
}

func TestHomeTipChangesOnlyWhenHomeIsOpened(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	first := page.CurrentHomeTip()
	if page.HandleTick() {
		t.Fatal("idle tick unexpectedly requested redraw")
	}
	if page.CurrentHomeTip() != first {
		t.Fatal("idle tick changed the launch tip")
	}
	page.SelectNextHomeTip()
	if page.CurrentHomeTip() == first {
		t.Fatal("opening home did not select a new tip")
	}
}
