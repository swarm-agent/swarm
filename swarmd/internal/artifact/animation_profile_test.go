package artifact

import (
	"reflect"
	"strings"
	"testing"
)

func TestResolveAnimationProfilesClosedRegistry(t *testing.T) {
	expected := []string{"motion_ui", "generative_2d", "spatial_3d", "vector_playback", "final_render"}
	for _, id := range expected {
		resolved, err := ResolveAnimationProfile(&AnimationProfileInput{Profile: id})
		if err != nil {
			t.Fatalf("resolve %s: %v", id, err)
		}
		if resolved.ProfileID != id || resolved.RegistryVersion != AnimationProfileRegistryVersion || resolved.Budgets.NetworkAllowed || !resolved.Budgets.PauseWhenOffscreen || !resolved.Budgets.StopWhenDocumentHidden || resolved.Budgets.ReducedMotionBehavior != "static_first_frame" {
			t.Fatalf("unsafe or incomplete snapshot for %s: %+v", id, resolved)
		}
	}

	pixi, _ := ResolveAnimationProfile(&AnimationProfileInput{Profile: "generative_2d"})
	if pixi.RuntimePackage != "pixi.js" || pixi.RuntimeVersion != "8.19.0" {
		t.Fatalf("unexpected Pixi runtime: %+v", pixi)
	}
	three, _ := ResolveAnimationProfile(&AnimationProfileInput{Profile: "spatial_3d"})
	if three.RuntimePackage != "three" || three.RuntimeVersion != "0.185.1" || !three.Heavy {
		t.Fatalf("unexpected Three runtime: %+v", three)
	}
	vectors, _ := ResolveAnimationProfile(&AnimationProfileInput{Profile: "vector_playback"})
	if vectors.RuntimeVersion != "0.79.0" || vectors.SecondaryRuntimeVersion != "2.39.2" || !vectors.ImportedPlaybackOnly {
		t.Fatalf("unexpected vector runtimes: %+v", vectors)
	}
	final, _ := ResolveAnimationProfile(&AnimationProfileInput{Profile: "final_render"})
	if final.RuntimeKind != "mp4_playback" || !final.EditableSourceRequired {
		t.Fatalf("unexpected final render contract: %+v", final)
	}
}

func TestParseAnimationProfileRejectsOverridesAndUnknowns(t *testing.T) {
	cases := []map[string]any{
		{},
		{"profile": "unknown"},
		{"profile": "generative_2d", "runtime": "evil"},
		{"profile": "motion_ui", "packages": []any{"https://cdn.example/x.js"}},
		{"profile": "spatial_3d", "network_allowed": true},
	}
	for _, input := range cases {
		if _, err := ParseAnimationProfile(input); err == nil {
			t.Fatalf("expected rejection for %#v", input)
		}
	}
	if _, err := ParseAnimationProfile("motion_ui"); err == nil {
		t.Fatal("expected non-object rejection")
	}
}

func TestAnimationProfileToolSchemaIsStrict(t *testing.T) {
	schema := AnimationProfileToolSchema()
	if schema["additionalProperties"] != false {
		t.Fatalf("schema permits extra properties: %#v", schema)
	}
	properties := schema["properties"].(map[string]any)
	profile := properties["profile"].(map[string]any)
	if !reflect.DeepEqual(profile["enum"], []string{"motion_ui", "generative_2d", "spatial_3d", "vector_playback", "final_render"}) {
		t.Fatalf("unexpected enum: %#v", profile["enum"])
	}
	if strings.Contains(strings.ToLower(schema["description"].(string)), "cdn") {
		t.Fatal("schema should describe the local contract without suggesting CDN input")
	}
}
