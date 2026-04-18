package app

import (
	"testing"

	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func TestResolveThemeTarget(t *testing.T) {
	a := &App{}

	first, ok := a.resolveThemeTarget("#1")
	if !ok {
		t.Fatalf("resolveThemeTarget(#1) failed")
	}
	if first.ID != "black" {
		t.Fatalf("resolveThemeTarget(#1) id = %q, want black", first.ID)
	}

	crimson, ok := a.resolveThemeTarget("crimson")
	if !ok {
		t.Fatalf("resolveThemeTarget(crimson) failed")
	}
	if crimson.ID != "crimson" {
		t.Fatalf("resolveThemeTarget(crimson) id = %q, want crimson", crimson.ID)
	}

	tokyo, ok := a.resolveThemeTarget("tokyo")
	if !ok {
		t.Fatalf("resolveThemeTarget(tokyo) failed")
	}
	if tokyo.ID != "tokyo-night" {
		t.Fatalf("resolveThemeTarget(tokyo) id = %q, want tokyo-night", tokyo.ID)
	}

	if _, ok := a.resolveThemeTarget("#999"); ok {
		t.Fatalf("resolveThemeTarget(#999) = ok, want false")
	}
	if _, ok := a.resolveThemeTarget("does-not-exist"); ok {
		t.Fatalf("resolveThemeTarget(does-not-exist) = ok, want false")
	}
}

func TestResolveThemeTarget_CustomTheme(t *testing.T) {
	ui.SetCustomThemes(nil)
	t.Cleanup(func() {
		ui.SetCustomThemes(nil)
	})

	base, ok := ui.ResolveTheme("nord")
	if !ok {
		t.Fatalf("ResolveTheme(nord) failed")
	}
	custom, err := ui.NewCustomThemeOption("my-theme", "My Theme", base.Palette)
	if err != nil {
		t.Fatalf("NewCustomThemeOption() error = %v", err)
	}

	a := &App{
		config: defaultAppConfig(),
		home:   ui.NewHomePage(model.EmptyHome()),
	}
	a.config.UI.CustomThemes = []CustomThemeConfig{{
		ID:      custom.ID,
		Name:    custom.Name,
		Palette: custom.Palette,
	}}
	a.syncConfiguredCustomThemes()

	got, ok := a.resolveThemeTarget("my-theme")
	if !ok {
		t.Fatalf("resolveThemeTarget(my-theme) failed")
	}
	if got.ID != "my-theme" {
		t.Fatalf("resolveThemeTarget(my-theme) id = %q, want my-theme", got.ID)
	}
}

func TestEditCustomTheme_UpdatesPaletteEvenWhenSaveFails(t *testing.T) {
	ui.SetCustomThemes(nil)
	t.Cleanup(func() {
		ui.SetCustomThemes(nil)
	})

	base, ok := ui.ResolveTheme("nord")
	if !ok {
		t.Fatalf("ResolveTheme(nord) failed")
	}
	custom, err := ui.NewCustomThemeOption("my-theme", "My Theme", base.Palette)
	if err != nil {
		t.Fatalf("NewCustomThemeOption() error = %v", err)
	}

	a := &App{
		config: defaultAppConfig(),
		home:   ui.NewHomePage(model.EmptyHome()),
	}
	a.config.UI.Theme = custom.ID
	a.config.UI.CustomThemes = []CustomThemeConfig{{
		ID:      custom.ID,
		Name:    custom.Name,
		Palette: custom.Palette,
	}}
	a.syncConfiguredCustomThemes()

	a.editCustomTheme("my-theme", "accent", "#ff00aa")
	if len(a.config.UI.CustomThemes) != 1 {
		t.Fatalf("len(a.config.UI.CustomThemes) = %d, want 1", len(a.config.UI.CustomThemes))
	}
	if a.config.UI.CustomThemes[0].Palette.Accent != "#FF00AA" {
		t.Fatalf("custom accent = %q, want #FF00AA", a.config.UI.CustomThemes[0].Palette.Accent)
	}
}
