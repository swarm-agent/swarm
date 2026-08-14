package artifact

import (
	"encoding/json"
	"testing"
)

func TestResolveOutputRequirementsAliasesAndPrecedence(t *testing.T) {
	for alias, canonical := range map[string]string{
		"twitter_header": "x_header", "twitter_banner": "x_header",
		"x_video": "x_video_landscape", "twitter_video": "x_video_landscape",
	} {
		resolved, err := ResolveOutputRequirements(&OutputRequirementsInput{Preset: alias})
		if err != nil {
			t.Fatal(err)
		}
		if resolved.PresetID != canonical {
			t.Fatalf("alias %q resolved to %#v", alias, resolved)
		}
	}
	resolved, err := ResolveOutputRequirements(&OutputRequirementsInput{Preset: "twitter_header"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.PresetID != "x_header" || resolved.Width != 1500 || resolved.Height != 500 || resolved.AspectRatio != "3:1" || resolved.Orientation != "landscape" || resolved.ResolutionSource != "preset" || resolved.RegistryVersion != OutputRequirementsRegistryVersion {
		t.Fatalf("resolved = %#v", resolved)
	}
	resolved, err = ResolveOutputRequirements(&OutputRequirementsInput{Preset: "x_video", Width: 1920, Height: 1080})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ResolutionSource != "dimensions" || resolved.PresetID != "x_video_landscape" { t.Fatalf("precedence = %#v", resolved) }
}

func TestResolveOutputRequirementsValidation(t *testing.T) {
	cases := []*OutputRequirementsInput{
		{},
		{Width: 100},
		{Width: -1, Height: 100},
		{Width: OutputRequirementsMaxDimension + 1, Height: 100},
		{Preset: "missing"},
		{Preset: "square_1080", Width: 1920, Height: 1080},
		{Width: 1920, Height: 1080, AspectRatio: "4:3"},
		{Width: 1920, Height: 1080, AspectRatio: "wide"},
		{Width: 1920, Height: 1080, Orientation: "portrait"},
		{Width: 1920, Height: 1080, Orientation: "diagonal"},
	}
	for _, input := range cases {
		if _, err := ResolveOutputRequirements(input); err == nil {
			t.Fatalf("expected rejection for %#v", input)
		}
	}
	if resolved, err := ResolveOutputRequirements(nil); err != nil || resolved != nil {
		t.Fatalf("omitted = %#v, %v", resolved, err)
	}
}

func TestParseOutputRequirementsAcceptsTypedIntegerMap(t *testing.T) {
	resolved, err := ParseOutputRequirements(map[string]any{"width": json.Number("1080"), "height": int64(1080)})
	if err != nil {
		t.Fatal(err)
	}
	if resolved == nil || resolved.Width != 1080 || resolved.Height != 1080 || resolved.AspectRatio != "1:1" || resolved.Orientation != "square" || resolved.ResolutionSource != "dimensions" {
		t.Fatalf("resolved = %#v", resolved)
	}
}

func TestParseOutputRequirementsRejectsEmptyAndUnknownFields(t *testing.T) {
	for _, raw := range []any{map[string]any{}, map[string]any{"preset": "x_header", "extra": true}, map[string]any{"width": 1080}, map[string]any{"preset": 1}} {
		if _, err := ParseOutputRequirements(raw); err == nil {
			t.Fatalf("expected rejection for %#v", raw)
		}
	}
}

func TestOutputPresetRegistryRejectsDuplicateAlias(t *testing.T) {
	definitions := []outputPresetDefinition{
		{id: "one", aliases: []string{"shared"}, width: 10, height: 10},
		{id: "two", aliases: []string{"shared"}, width: 20, height: 10},
	}
	if err := validateOutputPresetRegistry(definitions); err == nil {
		t.Fatal("expected duplicate alias rejection")
	}
}

func TestOutputPresetRegistryIsUniqueAndVersioned(t *testing.T) {
	expected := map[string][2]int{
		"x_header": {1500, 500}, "x_video_landscape": {1920, 1080}, "x_video_portrait": {1080, 1920},
		"full_hd_landscape": {1920, 1080}, "vertical_video": {1080, 1920}, "square_1080": {1080, 1080},
	}
	seen := map[string]string{}
	for _, preset := range ListOutputPresets() {
		if dimensions, ok := expected[preset.ID]; !ok || preset.Width != dimensions[0] || preset.Height != dimensions[1] {
			t.Fatalf("unexpected preset: %#v", preset)
		}
		delete(expected, preset.ID)
		if preset.RegistryVersion == "" || preset.ReviewedSource == "" || preset.ReviewedDate == "" {
			t.Fatalf("metadata missing: %#v", preset)
		}
		for _, value := range append([]string{preset.ID}, preset.Aliases...) {
			key := normalizePresetName(value)
			if previous := seen[key]; previous != "" {
				t.Fatalf("alias %q shared by %q and %q", key, previous, preset.ID)
			}
			seen[key] = preset.ID
		}
	}
	if len(expected) != 0 {
		t.Fatalf("missing presets: %#v", expected)
	}
}
