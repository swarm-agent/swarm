package ui

type homeSection struct {
	kind string
	h    int
}

type homeResponsiveLayout struct {
	Variant         layoutVariant
	BottomBarHeight int
	CenterStack     bool
	PinMetaTop      bool
}

func resolveHomeResponsiveLayout(width, height int) homeResponsiveLayout {
	profile := homeResponsiveLayout{
		Variant:         homeLayout,
		BottomBarHeight: bottomBarHeight,
		CenterStack:     true,
	}

	switch {
	case width >= 68 && height >= 18:
		// The full V3 home includes an unmistakable launch panel above the
		// composer. Smaller terminals keep the lightweight compact layout.
		profile.Variant.UseSwarmTopBar = true
		profile.Variant.ShowHero = true
		profile.Variant.ShowPresets = false
		profile.Variant.ShowTips = true
		profile.PinMetaTop = false
	case width >= 52 && height >= 14:
		// Compact mode starts only on clearly narrow terminals.
		profile.Variant.UseSwarmTopBar = false
		profile.Variant.ShowDirectory = true
		profile.Variant.ShowPresets = false
		profile.Variant.ShowTips = false
		profile.CenterStack = true
		profile.PinMetaTop = true
	default:
		profile.Variant.UseSwarmTopBar = false
		profile.Variant.ShowDirectory = true
		profile.Variant.ShowPresets = false
		profile.Variant.ShowTips = false
		profile.CenterStack = true
		profile.PinMetaTop = true
	}

	profile.BottomBarHeight = responsiveFooterHeight(width, profile.BottomBarHeight)
	if profile.BottomBarHeight < 1 {
		profile.BottomBarHeight = 1
	}
	if height < 10 {
		profile.CenterStack = false
	}
	return profile
}

func buildHomeSections(variant layoutVariant) []homeSection {
	metaH := 1
	if variant.ShowDirectory {
		metaH = 2
	}

	sections := make([]homeSection, 0, 5)
	if variant.UseSwarmTopBar {
		if variant.ShowHero {
			sections = append(sections, homeSection{kind: "hero", h: 5})
		}
		if variant.ShowPresets {
			sections = append(sections, homeSection{kind: "presets", h: 1})
		}
		sections = append(sections, homeSection{kind: "input", h: 3})
		if variant.ShowTips {
			sections = append(sections, homeSection{kind: "tips", h: 1})
		}
		return sections
	}

	if variant.InputFirst {
		sections = append(sections, homeSection{kind: "input", h: 3})
		sections = append(sections, homeSection{kind: "meta", h: metaH})
	} else {
		sections = append(sections, homeSection{kind: "meta", h: metaH})
		sections = append(sections, homeSection{kind: "input", h: 3})
	}
	if variant.ShowPresets {
		sections = append(sections, homeSection{kind: "presets", h: 1})
	}
	if variant.ShowTips {
		sections = append(sections, homeSection{kind: "tips", h: 1})
	}
	return sections
}

func sectionStackHeight(sections []homeSection) int {
	total := 0
	for _, sec := range sections {
		total += sec.h
	}
	if len(sections) > 1 {
		total += (len(sections) - 1) * sectionGap
	}
	return total
}

func inputSectionOffset(sections []homeSection) (int, bool) {
	offset := 0
	for i, sec := range sections {
		if sec.kind == "input" {
			return offset, true
		}
		offset += sec.h
		if i < len(sections)-1 {
			offset += sectionGap
		}
	}
	return 0, false
}
