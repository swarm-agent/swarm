package app

import (
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestAppProcessEventBatchOrdering(t *testing.T) {
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()

	app := &App{
		screen: screen,
		route:  "home",
	}
	dirty := false

	// Resize event sets dirty
	quit, err := app.processAppEvent(tcell.NewEventResize(80, 24), &dirty)
	if quit || err != nil {
		t.Fatalf("unexpected quit/err: %v, %v", quit, err)
	}
	if !dirty {
		t.Fatal("expected dirty after resize")
	}

	// Unknown interrupt does not quit
	dirty = false
	quit, err = app.processAppEvent(tcell.NewEventInterrupt("unknown"), &dirty)
	if quit || err != nil {
		t.Fatalf("unexpected quit/err: %v, %v", quit, err)
	}

	// Quit interrupt signals quit
	quit, err = app.processAppEvent(tcell.NewEventInterrupt(interruptQuit), &dirty)
	if !quit || err != nil {
		t.Fatalf("expected quit=true, got %v, %v", quit, err)
	}
}
