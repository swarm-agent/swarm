package app

import (
	"testing"

	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func TestHandleMouseCommandStatusClearsOverlay(t *testing.T) {
	a := &App{
		home:   ui.NewHomePage(model.EmptyHome()),
		route:  "home",
		config: defaultAppConfig(),
	}
	a.home.SetCommandOverlay([]string{"old overlay"})

	a.handleMouseCommand([]string{"status"})

	if got := a.home.CommandOverlayLines(); len(got) != 0 {
		t.Fatalf("command overlay should be cleared, got %v", got)
	}
}

func TestApplyMouseSettingClearsOverlayOnSaveFailure(t *testing.T) {
	a := &App{
		home:   ui.NewHomePage(model.EmptyHome()),
		route:  "home",
		config: defaultAppConfig(),
	}
	a.home.SetCommandOverlay([]string{"old overlay"})

	a.applyMouseSetting(true)

	if got := a.home.CommandOverlayLines(); len(got) != 0 {
		t.Fatalf("command overlay should be cleared, got %v", got)
	}
}
