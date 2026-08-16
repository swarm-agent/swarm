package artifact

import (
	"errors"
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const AnimationProfileRegistryVersion = "2026-08-16.v1"

type AnimationProfileInput struct {
	Profile string `json:"profile"`
}

type animationProfileDefinition struct {
	id                      string
	runtimeKind             string
	runtimePackage          string
	runtimeVersion          string
	secondaryRuntimePackage string
	secondaryRuntimeVersion string
	heavy                   bool
	importedPlaybackOnly    bool
	editableSourceRequired  bool
	budgets                 pebblestore.SessionArtifactAnimationBudgets
}

var animationProfileDefinitions = []animationProfileDefinition{
	{
		id: "motion_ui", runtimeKind: "native_css_waapi_svg",
		budgets: animationBudgets(3, 0, 2, 4_194_304, 0, 400),
	},
	{
		id: "generative_2d", runtimeKind: "canvas_2d_pixi",
		runtimePackage: "pixi.js", runtimeVersion: "8.19.0",
		budgets: animationBudgets(2, 1, 2, 4_194_304, 5_000, 500),
	},
	{
		id: "spatial_3d", runtimeKind: "three_webgl", runtimePackage: "three", runtimeVersion: "0.185.1", heavy: true,
		budgets: animationBudgets(1, 1, 1.5, 2_073_600, 2_000, 200),
	},
	{
		id: "vector_playback", runtimeKind: "imported_vector_playback",
		runtimePackage: "@lottiefiles/dotlottie-web", runtimeVersion: "0.79.0",
		secondaryRuntimePackage: "@rive-app/canvas", secondaryRuntimeVersion: "2.39.2", importedPlaybackOnly: true,
		budgets: animationBudgets(3, 0, 2, 4_194_304, 0, 300),
	},
	{
		id: "final_render", runtimeKind: "mp4_playback", editableSourceRequired: true,
		budgets: animationBudgets(3, 0, 2, 8_294_400, 0, 0),
	},
}

func animationBudgets(live, webgl int, dpr float64, pixels, particles, drawCalls int) pebblestore.SessionArtifactAnimationBudgets {
	return pebblestore.SessionArtifactAnimationBudgets{
		MaxSimultaneousLivePreviews: live,
		MaxWebGLContexts:            webgl,
		MaxDevicePixelRatio:         dpr,
		MaxCanvasPixels:             pixels,
		MaxParticles:                particles,
		MaxDrawCallsPerFrame:        drawCalls,
		PauseWhenOffscreen:          true,
		StopWhenDocumentHidden:      true,
		ReducedMotionBehavior:       "static_first_frame",
		NetworkAllowed:              false,
	}
}

func init() {
	if err := validateAnimationProfileRegistry(animationProfileDefinitions); err != nil {
		panic(err)
	}
}

func ResolveAnimationProfile(input *AnimationProfileInput) (*pebblestore.SessionArtifactAnimationProfile, error) {
	if input == nil {
		return nil, nil
	}
	profileID := strings.TrimSpace(input.Profile)
	if profileID == "" {
		return nil, errors.New("animation_profile profile is required")
	}
	for _, definition := range animationProfileDefinitions {
		if definition.id == profileID {
			return &pebblestore.SessionArtifactAnimationProfile{
				ProfileID:               definition.id,
				RegistryVersion:         AnimationProfileRegistryVersion,
				RuntimeKind:             definition.runtimeKind,
				RuntimePackage:          definition.runtimePackage,
				RuntimeVersion:          definition.runtimeVersion,
				SecondaryRuntimePackage: definition.secondaryRuntimePackage,
				SecondaryRuntimeVersion: definition.secondaryRuntimeVersion,
				Heavy:                   definition.heavy,
				ImportedPlaybackOnly:    definition.importedPlaybackOnly,
				EditableSourceRequired:  definition.editableSourceRequired,
				Budgets:                 definition.budgets,
			}, nil
		}
	}
	return nil, fmt.Errorf("animation_profile profile %q is unknown", profileID)
}

func ParseAnimationProfile(raw any) (*pebblestore.SessionArtifactAnimationProfile, error) {
	if raw == nil {
		return nil, nil
	}
	value, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("animation_profile must be an object")
	}
	if len(value) != 1 {
		return nil, errors.New("animation_profile must contain only profile")
	}
	rawProfile, exists := value["profile"]
	if !exists {
		return nil, errors.New("animation_profile profile is required")
	}
	profile, ok := rawProfile.(string)
	if !ok || strings.TrimSpace(profile) == "" {
		return nil, errors.New("animation_profile profile must be a non-empty string")
	}
	return ResolveAnimationProfile(&AnimationProfileInput{Profile: profile})
}

func AnimationProfileToolSchema() map[string]any {
	return map[string]any{
		"type":        "object",
		"description": "Closed, server-resolved animation execution profile. Runtimes are exact local packages; network and runtime overrides are prohibited.",
		"properties": map[string]any{
			"profile": map[string]any{
				"type": "string",
				"enum": []string{"motion_ui", "generative_2d", "spatial_3d", "vector_playback", "final_render"},
			},
		},
		"required":             []string{"profile"},
		"additionalProperties": false,
	}
}

func validateAnimationProfileRegistry(definitions []animationProfileDefinition) error {
	if len(definitions) != 5 {
		return errors.New("animation profile registry must contain exactly five launch profiles")
	}
	seen := map[string]struct{}{}
	for _, definition := range definitions {
		if definition.id == "" || definition.runtimeKind == "" {
			return errors.New("animation profile registry entry is incomplete")
		}
		if _, exists := seen[definition.id]; exists {
			return fmt.Errorf("animation profile %q is duplicated", definition.id)
		}
		seen[definition.id] = struct{}{}
		if (definition.runtimePackage == "") != (definition.runtimeVersion == "") || (definition.secondaryRuntimePackage == "") != (definition.secondaryRuntimeVersion == "") {
			return fmt.Errorf("animation profile %q runtime package and version must be paired", definition.id)
		}
		budgets := definition.budgets
		if budgets.MaxSimultaneousLivePreviews < 1 || budgets.MaxWebGLContexts < 0 || budgets.MaxDevicePixelRatio <= 0 || budgets.MaxCanvasPixels < 1 || budgets.MaxParticles < 0 || budgets.MaxDrawCallsPerFrame < 0 || !budgets.PauseWhenOffscreen || !budgets.StopWhenDocumentHidden || budgets.ReducedMotionBehavior != "static_first_frame" || budgets.NetworkAllowed {
			return fmt.Errorf("animation profile %q budgets are unsafe", definition.id)
		}
	}
	return nil
}
