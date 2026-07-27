package app

import (
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func newUpdateCommandTestApp() *App {
	return &App{
		home:   ui.NewHomePage(model.EmptyHome()),
		route:  "home",
		config: defaultAppConfig(),
	}
}

func TestUpdateHelpLineOmitsRetiredLocalContainerConfirmationCommands(t *testing.T) {
	if got := updateHelpLine(true); got != "/update [dev]   (update Swarm)" {
		t.Fatalf("dev help = %q", got)
	}
	if got := updateHelpLine(false); got != "/update   (update Swarm)" {
		t.Fatalf("release help = %q", got)
	}
}

func TestUpdateUsageOmitsRetiredLocalContainerConfirmationCommands(t *testing.T) {
	if got := updateUsage(true); got != "usage: /update [dev]" {
		t.Fatalf("dev usage = %q", got)
	}
	if got := updateUsage(false); got != "usage: /update" {
		t.Fatalf("release usage = %q", got)
	}
}

func TestUpdateSettingsRoundTripThroughAppConfigPreservesSwarmName(t *testing.T) {
	settings := client.UISettings{Swarm: client.UISwarmSettings{Name: "Primary"}}

	cfg := appConfigFromUISettings(settings)
	if cfg.Swarm.Name != "Primary" {
		t.Fatalf("cfg.Swarm.Name = %q, want Primary", cfg.Swarm.Name)
	}

	saved := uiSettingsFromAppConfig(cfg)
	if saved.Swarm.Name != "Primary" {
		t.Fatalf("saved.Swarm.Name = %q, want Primary", saved.Swarm.Name)
	}
}
