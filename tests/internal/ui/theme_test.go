package ui

import "testing"

func TestThemeCatalogIncludesRequiredThemes(t *testing.T) {
	themes := ThemeCatalog()
	if len(themes) < 20 {
		t.Fatalf("ThemeCatalog() len = %d, want at least 20", len(themes))
	}

	hasNord := false
	hasCrimson := false
	for _, item := range themes {
		switch item.ID {
		case "nord":
			hasNord = true
		case "crimson":
			hasCrimson = true
		}
	}
	if !hasNord {
		t.Fatalf("ThemeCatalog() missing nord")
	}
	if !hasCrimson {
		t.Fatalf("ThemeCatalog() missing crimson")
	}
}

func TestResolveTheme_DefaultAndAliases(t *testing.T) {
	defaultTheme, ok := ResolveTheme(DefaultThemeID())
	if !ok {
		t.Fatalf("ResolveTheme(%q) failed", DefaultThemeID())
	}
	if defaultTheme.ID != "nord" {
		t.Fatalf("default theme id = %q, want nord", defaultTheme.ID)
	}

	if _, ok := ResolveTheme("Nord"); !ok {
		t.Fatalf("ResolveTheme(Nord) failed")
	}
	if _, ok := ResolveTheme("solarized_dark"); !ok {
		t.Fatalf("ResolveTheme(solarized_dark) failed")
	}
	if _, ok := ResolveTheme("rose pine"); !ok {
		t.Fatalf("ResolveTheme(rose pine) failed")
	}
}

func TestNormalizeThemeID(t *testing.T) {
	if got := NormalizeThemeID("  Solarized_Dark  "); got != "solarized-dark" {
		t.Fatalf("NormalizeThemeID() = %q, want solarized-dark", got)
	}
	if got := NormalizeThemeID("Rose/Pine"); got != "rose-pine" {
		t.Fatalf("NormalizeThemeID() = %q, want rose-pine", got)
	}
}

func TestSetCustomThemesAndResolve(t *testing.T) {
	SetCustomThemes(nil)
	t.Cleanup(func() {
		SetCustomThemes(nil)
	})

	base, ok := ResolveTheme("nord")
	if !ok {
		t.Fatalf("ResolveTheme(nord) failed")
	}
	custom, err := NewCustomThemeOption("my-custom", "My Custom", base.Palette)
	if err != nil {
		t.Fatalf("NewCustomThemeOption() error = %v", err)
	}

	SetCustomThemes([]ThemeOption{custom})
	resolved, ok := ResolveTheme("my-custom")
	if !ok {
		t.Fatalf("ResolveTheme(my-custom) failed")
	}
	if resolved.ID != "my-custom" {
		t.Fatalf("resolved id = %q, want my-custom", resolved.ID)
	}
	if resolved.Builtin {
		t.Fatalf("resolved.Builtin = true, want false")
	}
}

func TestSetThemePaletteSlot(t *testing.T) {
	palette := ThemePalette{}
	updated, err := SetThemePaletteSlot(palette, "accent", "#ff00aa")
	if err != nil {
		t.Fatalf("SetThemePaletteSlot(accent) error = %v", err)
	}
	if updated.Accent != "#FF00AA" {
		t.Fatalf("updated.Accent = %q, want #FF00AA", updated.Accent)
	}

	if _, err := SetThemePaletteSlot(updated, "unknown-slot", "#123456"); err == nil {
		t.Fatalf("SetThemePaletteSlot(unknown-slot) error = nil, want error")
	}
	if _, err := SetThemePaletteSlot(updated, "accent", "bad-color"); err == nil {
		t.Fatalf("SetThemePaletteSlot(invalid-color) error = nil, want error")
	}

	updated, err = SetThemePaletteSlot(updated, "code-keyword", "#c792ea")
	if err != nil {
		t.Fatalf("SetThemePaletteSlot(code-keyword) error = %v", err)
	}
	if updated.CodeKeyword != "#C792EA" {
		t.Fatalf("updated.CodeKeyword = %q, want #C792EA", updated.CodeKeyword)
	}

	updated, err = SetThemePaletteSlot(updated, "code-bg", "#101820")
	if err != nil {
		t.Fatalf("SetThemePaletteSlot(code-bg) error = %v", err)
	}
	if updated.CodeBackground != "#101820" {
		t.Fatalf("updated.CodeBackground = %q, want #101820", updated.CodeBackground)
	}
}
