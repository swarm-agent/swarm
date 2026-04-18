package ui

import "testing"

func TestBuildHomeSectionsTopBarPlacesPresetsAboveInput(t *testing.T) {
	variant := layoutVariant{
		UseSwarmTopBar: true,
		ShowPresets:    true,
		ShowTips:       true,
	}

	sections := buildHomeSections(variant)
	if len(sections) != 3 {
		t.Fatalf("len(sections) = %d, want 3", len(sections))
	}
	if sections[0].kind != "presets" {
		t.Fatalf("sections[0].kind = %q, want presets", sections[0].kind)
	}
	if sections[1].kind != "input" {
		t.Fatalf("sections[1].kind = %q, want input", sections[1].kind)
	}
	if sections[2].kind != "tips" {
		t.Fatalf("sections[2].kind = %q, want tips", sections[2].kind)
	}
}

func TestBuildHomeSectionsTopBarWithoutPresetsKeepsInput(t *testing.T) {
	variant := layoutVariant{
		UseSwarmTopBar: true,
		ShowPresets:    false,
		ShowTips:       true,
	}

	sections := buildHomeSections(variant)
	if len(sections) != 2 {
		t.Fatalf("len(sections) = %d, want 2", len(sections))
	}
	if sections[0].kind != "input" {
		t.Fatalf("sections[0].kind = %q, want input", sections[0].kind)
	}
	if sections[1].kind != "tips" {
		t.Fatalf("sections[1].kind = %q, want tips", sections[1].kind)
	}
}

func TestResolveHomeResponsiveLayoutBreakpoints(t *testing.T) {
	full := resolveHomeResponsiveLayout(100, 30)
	if !full.Variant.UseSwarmTopBar {
		t.Fatalf("full layout should keep top bar")
	}
	if full.PinMetaTop {
		t.Fatalf("full layout should not pin meta strip")
	}
	if full.BottomBarHeight != 3 {
		t.Fatalf("full bottom bar = %d, want 3", full.BottomBarHeight)
	}
	if full.MaxRecentRows != recentVisibleRows {
		t.Fatalf("full max recent rows = %d, want %d", full.MaxRecentRows, recentVisibleRows)
	}

	medium := resolveHomeResponsiveLayout(72, 20)
	if !medium.Variant.UseSwarmTopBar {
		t.Fatalf("medium layout should still keep top bar")
	}
	if medium.PinMetaTop {
		t.Fatalf("medium layout should not use pinned meta strip")
	}
	if medium.MaxRecentRows != recentVisibleRows {
		t.Fatalf("medium max recent rows = %d, want %d", medium.MaxRecentRows, recentVisibleRows)
	}

	compact := resolveHomeResponsiveLayout(60, 20)
	if compact.Variant.UseSwarmTopBar {
		t.Fatalf("compact layout should disable top bar only on narrow widths")
	}
	if !compact.CenterStack {
		t.Fatalf("compact layout should center non-meta content")
	}
	if !compact.PinMetaTop {
		t.Fatalf("compact layout should pin meta at top")
	}
	if !compact.Variant.ShowDirectory {
		t.Fatalf("compact layout should keep directory context")
	}
	if !compact.Variant.ShowPresets {
		t.Fatalf("compact layout should keep presets")
	}
	if compact.Variant.ShowTips {
		t.Fatalf("compact layout should hide tips")
	}
	if compact.MaxRecentRows != recentVisibleRows {
		t.Fatalf("compact max recent rows = %d, want %d", compact.MaxRecentRows, recentVisibleRows)
	}

	thin := resolveHomeResponsiveLayout(44, 12)
	if thin.Variant.UseSwarmTopBar {
		t.Fatalf("thin layout should disable top bar")
	}
	if !thin.CenterStack {
		t.Fatalf("thin layout should center non-meta content")
	}
	if !thin.PinMetaTop {
		t.Fatalf("thin layout should pin meta at top")
	}
	if !thin.Variant.ShowDirectory {
		t.Fatalf("thin layout should keep directory context")
	}
	if thin.Variant.ShowPresets {
		t.Fatalf("thin layout should hide presets")
	}
	if thin.BottomBarHeight != 1 {
		t.Fatalf("thin bottom bar = %d, want 1", thin.BottomBarHeight)
	}
	if thin.MaxRecentRows != recentVisibleRows {
		t.Fatalf("thin max recent rows = %d, want %d", thin.MaxRecentRows, recentVisibleRows)
	}

	micro := resolveHomeResponsiveLayout(44, 7)
	if micro.MaxRecentRows != 1 {
		t.Fatalf("micro max recent rows = %d, want 1", micro.MaxRecentRows)
	}
}
