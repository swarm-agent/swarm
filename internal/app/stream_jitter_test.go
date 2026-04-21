package app

import (
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func newStreamTestApp() *App {
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		panic(err)
	}
	cfg := defaultAppConfig()
	cfg.Swarm.Name = "swarm"
	return &App{
		screen:             screen,
		home:               ui.NewHomePage(model.EmptyHome()),
		chat:               ui.NewChatPage(ui.ChatPageOptions{SessionID: "session-test", SessionMode: "auto", AuthConfigured: true, SwarmName: cfg.Swarm.Name}),
		route:              "chat",
		config:             cfg,
		pendingChatRender:  make(chan struct{}, 1),
		pendingStreamReady: make(chan struct{}, 1),
		streamEvents:       make(chan client.StreamEventEnvelope, 256),
	}
}

func TestRequestStreamReadyInterruptCoalescesBurst(t *testing.T) {
	a := newStreamTestApp()
	defer a.screen.Fini()

	for i := 0; i < 20; i++ {
		a.requestStreamReadyInterrupt()
	}
	if got := len(a.pendingStreamReady); got != 1 {
		t.Fatalf("pending stream ready len = %d, want 1", got)
	}

	a.consumePendingStreamReady()
	if got := len(a.pendingStreamReady); got != 0 {
		t.Fatalf("pending stream ready len after consume = %d, want 0", got)
	}
}

func TestConsumeSessionStreamEventsDrainsBurst(t *testing.T) {
	a := newStreamTestApp()
	defer a.screen.Fini()

	for i := 0; i < 32; i++ {
		a.streamEvents <- client.StreamEventEnvelope{EventType: "session.title.updated", Payload: []byte(`{"session_id":"session-test","title":"burst title"}`)}
	}
	if changed := a.consumeSessionStreamEvents(); !changed {
		t.Fatal("expected burst stream drain to report changes")
	}
	if got := len(a.streamEvents); got != 0 {
		t.Fatalf("expected stream queue drained, remaining=%d", got)
	}
}
