package api

import (
	"encoding/json"
	"testing"

	"swarm/packages/swarmd/internal/imagegen"
	"swarm/packages/swarmd/internal/model"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Written purpose / invariant:
// /v1/media/settings/catalog must project exact operation constraints on video models,
// include Veo 3.1 standard/fast and Stable Omni in video_iteration_models, and strictly
// exclude Veo Lite, Preview Omni, and OpenRouter from video_iteration_models without
// reducing video_generation_models visibility.
//
// Threat/regression: Media viewer opening an existing video fell back to video_iteration_models,
// which previously excluded Veo models and admitted Preview Omni, silently resetting the model
// to Omni and presenting incompatible options.
//
// Authority: media_settings.go (mediaCatalogResponse, isVideoIterationCatalogRecord),
// videogen/constraints.go (BuildVideoOperationConstraints, SupportsVideoIteration).

func TestMediaCatalogResponse_VideoIterationFilteringAndConstraints(t *testing.T) {
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	catalogStore := pebblestore.NewModelCatalogStore(store)
	pricing := json.RawMessage(`{"video_output":0.4}`)

	veo31StdSpecific := json.RawMessage(`{"google":{"video_generation":{"settings":{"aspect_ratio":{"status":"verified","default_value":"16:9","supported_values":["16:9","9:16"]},"resolution":{"status":"verified","default_value":"720p","supported_values":["720p","1080p"]},"duration_seconds":{"status":"verified","default_value":8,"supported_values":[4,6,8]}},"features":{"video_extension":{"status":"verified","supported":true},"initial_image":{"status":"verified","supported":true}}}}}`)
	veo31FastSpecific := json.RawMessage(`{"google":{"video_generation":{"settings":{"aspect_ratio":{"status":"verified","default_value":"16:9","supported_values":["16:9","9:16"]},"resolution":{"status":"verified","default_value":"720p","supported_values":["720p"]},"duration_seconds":{"status":"verified","default_value":8,"supported_values":[4,6,8]}},"features":{"video_extension":{"status":"verified","supported":true}}}}}`)
	veo31LiteSpecific := json.RawMessage(`{"google":{"video_generation":{"settings":{"aspect_ratio":{"status":"verified","default_value":"16:9","supported_values":["16:9","9:16"]},"resolution":{"status":"verified","default_value":"720p","supported_values":["720p","1080p"]},"duration_seconds":{"status":"verified","default_value":8,"supported_values":[4,6,8]}},"features":{"video_extension":{"status":"verified","supported":true}}}}}`)
	stableOmniSpecific := json.RawMessage(`{"google":{"video_generation":{"settings":{"aspect_ratio":{"status":"verified","default_value":"16:9","supported_values":["16:9","9:16"]},"resolution":{"status":"verified","default_value":"720p","supported_values":["720p"]}},"features":{"conversational_editing":{"status":"verified","supported":true},"video_extension":{"status":"verified","supported":true}}}}}`)
	previewOmniSpecific := json.RawMessage(`{"google":{"video_generation":{"settings":{"aspect_ratio":{"status":"verified","default_value":"16:9","supported_values":["16:9","9:16"]},"resolution":{"status":"verified","default_value":"720p","supported_values":["720p"]}},"features":{"conversational_editing":{"status":"verified","supported":true},"video_extension":{"status":"verified","supported":true}}}}}`)

	records := []pebblestore.ModelCatalogRecord{
		{Provider: "google", Model: "veo-3.1-generate-preview", DisplayName: "Veo 3.1 Standard", CatalogModalities: pebblestore.ModelCatalogModalities{Outputs: []string{"video"}}, ProviderSpecific: veo31StdSpecific, Pricing: pricing},
		{Provider: "google", Model: "veo-3.1-fast-generate-preview", DisplayName: "Veo 3.1 Fast", CatalogModalities: pebblestore.ModelCatalogModalities{Outputs: []string{"video"}}, ProviderSpecific: veo31FastSpecific, Pricing: pricing},
		{Provider: "google", Model: "veo-3.1-lite-generate-preview", DisplayName: "Veo 3.1 Lite", CatalogModalities: pebblestore.ModelCatalogModalities{Outputs: []string{"video"}}, ProviderSpecific: veo31LiteSpecific, Pricing: pricing},
		{Provider: "google", Model: "gemini-omni-1.1-flash", DisplayName: "Gemini Omni 1.1 Flash", CatalogModalities: pebblestore.ModelCatalogModalities{Outputs: []string{"video"}}, ProviderSpecific: stableOmniSpecific, Pricing: pricing},
		{Provider: "google", Model: "gemini-omni-flash-preview", DisplayName: "Gemini Omni Flash Preview", CatalogModalities: pebblestore.ModelCatalogModalities{Outputs: []string{"video"}}, ProviderSpecific: previewOmniSpecific, Pricing: pricing},
		{Provider: "openrouter", Model: "google/veo-3.1", DisplayName: "Google: Veo 3.1", CatalogModalities: pebblestore.ModelCatalogModalities{Outputs: []string{"video"}}, Pricing: pricing},
	}

	for _, rec := range records {
		if err := catalogStore.SetRecord(rec); err != nil {
			t.Fatalf("seed record %s: %v", rec.Model, err)
		}
	}

	server := NewServer(nil, nil, model.NewService(pebblestore.NewModelStore(store), nil, model.NewCatalogService(catalogStore)), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	server.imageGen = imagegen.NewService(nil, nil, nil, server.model)

	caps := imagegen.Capabilities{Providers: []imagegen.ProviderStatus{{ID: imagegen.ProviderGoogleGemini, Ready: true}}}
	openRouterStatus := imagegen.ProviderStatus{ID: "openrouter", Ready: true}

	response, err := server.mediaCatalogResponse(caps, openRouterStatus)
	if err != nil {
		t.Fatalf("mediaCatalogResponse: %v", err)
	}

	// 1. Generation models must preserve all models (6 models)
	if len(response.VideoGenerationModels) != 6 {
		t.Fatalf("expected 6 video generation models, got %d", len(response.VideoGenerationModels))
	}

	// 2. Iteration models must contain exactly 3: veo-3.1-generate-preview, veo-3.1-fast-generate-preview, gemini-omni-1.1-flash
	if len(response.VideoIterationModels) != 3 {
		var names []string
		for _, m := range response.VideoIterationModels {
			names = append(names, m.Model)
		}
		t.Fatalf("expected 3 video iteration models, got %d: %v", len(response.VideoIterationModels), names)
	}

	iterModelMap := make(map[string]mediaCatalogOption)
	for _, m := range response.VideoIterationModels {
		iterModelMap[m.Model] = m
	}

	if _, ok := iterModelMap["veo-3.1-generate-preview"]; !ok {
		t.Errorf("veo-3.1-generate-preview must be in video_iteration_models")
	}
	if _, ok := iterModelMap["veo-3.1-fast-generate-preview"]; !ok {
		t.Errorf("veo-3.1-fast-generate-preview must be in video_iteration_models")
	}
	if _, ok := iterModelMap["gemini-omni-1.1-flash"]; !ok {
		t.Errorf("gemini-omni-1.1-flash must be in video_iteration_models")
	}

	// 3. Excluded models from iteration
	if _, ok := iterModelMap["veo-3.1-lite-generate-preview"]; ok {
		t.Errorf("veo-3.1-lite-generate-preview MUST NOT be in video_iteration_models")
	}
	if _, ok := iterModelMap["gemini-omni-flash-preview"]; ok {
		t.Errorf("gemini-omni-flash-preview MUST NOT be in video_iteration_models")
	}
	if _, ok := iterModelMap["google/veo-3.1"]; ok {
		t.Errorf("openrouter google/veo-3.1 MUST NOT be in video_iteration_models")
	}

	// 4. Verify constraints on Veo 3.1 Standard
	veoOpt := iterModelMap["veo-3.1-generate-preview"]
	if veoOpt.Constraints == nil {
		t.Fatalf("expected non-nil Constraints on veo-3.1-generate-preview")
	}
	if veoOpt.Constraints.Edit.Supported {
		t.Errorf("Veo 3.1 edit constraint must be supported=false")
	}
	if !veoOpt.Constraints.Extend.Supported {
		t.Errorf("Veo 3.1 extend constraint must be supported=true")
	}
	if veoOpt.Constraints.Extend.LockedDurationSeconds != 8 {
		t.Errorf("Veo 3.1 locked duration = %d, want 8", veoOpt.Constraints.Extend.LockedDurationSeconds)
	}
	if veoOpt.Constraints.Extend.LockedResolution != "720p" {
		t.Errorf("Veo 3.1 locked resolution = %q, want 720p", veoOpt.Constraints.Extend.LockedResolution)
	}
	if !veoOpt.Constraints.Extend.LockedAspectRatioMatchesSource {
		t.Errorf("Veo 3.1 locked aspect ratio matches source must be true")
	}

	// 5. Verify constraints on Veo Lite in generation models
	var veoLiteOpt *mediaCatalogOption
	for i := range response.VideoGenerationModels {
		if response.VideoGenerationModels[i].Model == "veo-3.1-lite-generate-preview" {
			veoLiteOpt = &response.VideoGenerationModels[i]
			break
		}
	}
	if veoLiteOpt == nil || veoLiteOpt.Constraints == nil {
		t.Fatalf("missing veo-3.1-lite-generate-preview with constraints in generation models")
	}
	if veoLiteOpt.Constraints.Extend.Supported {
		t.Errorf("Veo Lite extend must be supported=false")
	}
	if veoLiteOpt.Constraints.Edit.Supported {
		t.Errorf("Veo Lite edit must be supported=false")
	}

	// 6. Verify constraints on Stable Omni
	omniOpt := iterModelMap["gemini-omni-1.1-flash"]
	if omniOpt.Constraints == nil {
		t.Fatalf("expected non-nil Constraints on gemini-omni-1.1-flash")
	}
	if !omniOpt.Constraints.Edit.Supported {
		t.Errorf("Stable Omni edit must be supported=true")
	}
	if !omniOpt.Constraints.Extend.Supported {
		t.Errorf("Stable Omni extend must be supported=true")
	}
	if omniOpt.Constraints.Extend.SupportsDuration {
		t.Errorf("Stable Omni extend duration selection must be supported=false")
	}
	if omniOpt.Constraints.Extend.LockedDurationSeconds != 0 {
		t.Errorf("Stable Omni locked duration = %d, want 0", omniOpt.Constraints.Extend.LockedDurationSeconds)
	}
}
