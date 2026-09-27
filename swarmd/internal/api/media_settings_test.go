package api

import (
	"encoding/json"
	"testing"

	"swarm/packages/swarmd/internal/imagegen"
	"swarm/packages/swarmd/internal/model"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestMediaCatalogResponseFiltersVideoUnderstandingChoices(t *testing.T) {
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	catalogStore := pebblestore.NewModelCatalogStore(store)
	pricing := json.RawMessage(`{"input_per_million":1.25,"output_per_million":5}`)
	googleProviderSpecific := json.RawMessage(`{"google":{"model_api_surface":"generate_content","image_generation":{"api_surface":"generate_content","status":"verified","settings":{"aspect_ratio":{"status":"verified","supported_values":["1:1"]}}}}}`)
	googleGenerateContentMedia := &pebblestore.ModelCatalogMediaCapabilities{State: pebblestore.ModelCatalogMediaStateSupported, ProviderSurface: provideriface.MediaProviderSurfaceGoogleGenerateContent}
	for _, record := range []pebblestore.ModelCatalogRecord{
		{Provider: "google", Model: "snapshot-image", DisplayName: "Snapshot Image", CatalogModalities: pebblestore.ModelCatalogModalities{Inputs: []string{"text"}, Outputs: []string{"image"}}, Media: googleGenerateContentMedia, ProviderSpecific: googleProviderSpecific, Pricing: pricing},
		{Provider: "google", Model: "predict-image", DisplayName: "Predict Image", CatalogModalities: pebblestore.ModelCatalogModalities{Inputs: []string{"text"}, Outputs: []string{"image"}}, Media: googleGenerateContentMedia, ProviderSpecific: json.RawMessage(`{"google":{"model_api_surface":"predict"}}`)},
		{Provider: "google", Model: "gemini-video-text", DisplayName: "Gemini Video Text", CatalogModalities: pebblestore.ModelCatalogModalities{Inputs: []string{"text", "video"}, Outputs: []string{"text"}}, Pricing: pricing},
		{Provider: "google", Model: "veo-video-generator", DisplayName: "Veo", CatalogModalities: pebblestore.ModelCatalogModalities{Inputs: []string{"video"}, Outputs: []string{"text"}}},
		{Provider: "google", Model: "video-embedding", DisplayName: "Video Embedding", CatalogModalities: pebblestore.ModelCatalogModalities{Inputs: []string{"video"}, Outputs: []string{"text"}}},
		{Provider: "google", Model: "video-no-text", DisplayName: "Video No Text", CatalogModalities: pebblestore.ModelCatalogModalities{Inputs: []string{"video"}, Outputs: []string{"image"}}},
	} {
		if err := catalogStore.SetRecord(record); err != nil {
			t.Fatalf("seed catalog record: %v", err)
		}
	}
	server := NewServer(nil, nil, model.NewService(pebblestore.NewModelStore(store), nil, model.NewCatalogService(catalogStore)), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	server.imageGen = imagegen.NewService(nil, nil, nil, server.model)
	response, err := server.mediaCatalogResponse(imagegen.Capabilities{Providers: []imagegen.ProviderStatus{{ID: imagegen.ProviderGoogleGemini, Ready: true}}})
	if err != nil {
		t.Fatalf("mediaCatalogResponse: %v", err)
	}
	if len(response.ImageModels) != 2 || response.ImageModels[0].ID != imagegen.DefaultModelSelectionID || response.ImageModels[1].Model != "snapshot-image" {
		t.Fatalf("image models = %#v, want hardcoded Codex plus snapshot generateContent image", response.ImageModels)
	}
	if string(response.ImageModels[1].Pricing) != string(pricing) {
		t.Fatalf("image pricing = %s, want %s", response.ImageModels[1].Pricing, pricing)
	}
	if response.ImageModels[1].GenerationOptions == nil || len(response.ImageModels[1].GenerationOptions.AspectRatios) != 1 || response.ImageModels[1].GenerationOptions.AspectRatios[0] != "1:1" {
		t.Fatalf("image generation options = %#v, want aspect_ratio [1:1]", response.ImageModels[1].GenerationOptions)
	}
	if response.ImageModels[0].GenerationOptions != nil {
		t.Fatalf("hardcoded codex image model with no provider specific should have nil generation options, got %#v", response.ImageModels[0].GenerationOptions)
	}
	if len(response.TranscriptionModels) != 1 || response.TranscriptionModels[0].Model != "gemini-video-text" {
		t.Fatalf("transcription models = %#v, want only gemini-video-text", response.TranscriptionModels)
	}
	if string(response.TranscriptionModels[0].Pricing) != string(pricing) {
		t.Fatalf("pricing = %s, want %s", response.TranscriptionModels[0].Pricing, pricing)
	}
	if response.VideoReady || response.VideoStatus != "coming_soon" {
		t.Fatalf("video readiness = %v/%q, want false/coming_soon", response.VideoReady, response.VideoStatus)
	}
}

func TestMediaCatalogResponsePopulatesVideoGenerationAndIterationModels(t *testing.T) {
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	catalogStore := pebblestore.NewModelCatalogStore(store)
	pricing := json.RawMessage(`{"video_output":0.4}`)

	videoProviderSpecific := json.RawMessage(`{"google":{"video_generation":{"settings":{"aspect_ratio":{"status":"verified","default_value":"16:9","supported_values":["16:9","9:16"]},"resolution":{"status":"verified","default_value":"720p","supported_values":["720p","1080p","4k"]},"duration_seconds":{"status":"verified","default_value":8,"supported_values":[4,6,8]}}}}}`)
	veoRecord := pebblestore.ModelCatalogRecord{
		Provider: "google", Model: "veo-3.1-generate-preview", DisplayName: "Veo 3.1",
		CatalogModalities: pebblestore.ModelCatalogModalities{Inputs: []string{"text"}, Outputs: []string{"video"}},
		ProviderSpecific:  videoProviderSpecific,
		Pricing:           pricing,
	}
	omniRecord := pebblestore.ModelCatalogRecord{
		Provider: "google", Model: "gemini-omni-1.1-flash", DisplayName: "Gemini Omni 1.1 Flash",
		CatalogModalities: pebblestore.ModelCatalogModalities{Inputs: []string{"text"}, Outputs: []string{"video"}},
		ProviderSpecific:  json.RawMessage(`{"google":{"video_generation":{"features":{"conversational_editing":{"supported":true}}}}}`),
		Pricing:           pricing,
	}
	openRouterVeoRecord := pebblestore.ModelCatalogRecord{
		Provider: "openrouter", Model: "google/veo-3.1", DisplayName: "Google: Veo 3.1",
		CatalogModalities: pebblestore.ModelCatalogModalities{Inputs: []string{"text"}, Outputs: []string{"video"}},
		Pricing:           pricing,
	}

	for _, rec := range []pebblestore.ModelCatalogRecord{veoRecord, omniRecord, openRouterVeoRecord} {
		if err := catalogStore.SetRecord(rec); err != nil {
			t.Fatalf("seed record: %v", err)
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

	if !response.VideoReady || response.VideoStatus != "ready" {
		t.Fatalf("video readiness = %v/%q, want true/ready", response.VideoReady, response.VideoStatus)
	}
	if len(response.VideoGenerationModels) != 3 {
		t.Fatalf("expected 3 video generation models, got %d: %#v", len(response.VideoGenerationModels), response.VideoGenerationModels)
	}
	if len(response.VideoIterationModels) != 1 || response.VideoIterationModels[0].Model != "gemini-omni-1.1-flash" {
		t.Fatalf("expected 1 video iteration model (gemini-omni-1.1-flash), got: %#v", response.VideoIterationModels)
	}

	// Invariant: generation_options is populated from canonical model metadata when present
	var veoGenOption *mediaCatalogOption
	for i := range response.VideoGenerationModels {
		if response.VideoGenerationModels[i].Model == "veo-3.1-generate-preview" {
			veoGenOption = &response.VideoGenerationModels[i]
			break
		}
	}
	if veoGenOption == nil {
		t.Fatalf("missing veo-3.1-generate-preview in video generation models")
	}
	if veoGenOption.GenerationOptions == nil {
		t.Fatalf("expected generation_options on veo-3.1-generate-preview, got nil")
	}
	if len(veoGenOption.GenerationOptions.AspectRatios) != 2 || veoGenOption.GenerationOptions.DefaultRatio != "16:9" {
		t.Errorf("veo aspect ratios = %#v (default %q), want [16:9 9:16] default 16:9", veoGenOption.GenerationOptions.AspectRatios, veoGenOption.GenerationOptions.DefaultRatio)
	}
	if len(veoGenOption.GenerationOptions.Resolutions) != 3 || veoGenOption.GenerationOptions.DefaultRes != "720p" {
		t.Errorf("veo resolutions = %#v (default %q), want [720p 1080p 4k] default 720p", veoGenOption.GenerationOptions.Resolutions, veoGenOption.GenerationOptions.DefaultRes)
	}
	if len(veoGenOption.GenerationOptions.Durations) != 3 || veoGenOption.GenerationOptions.DefaultDur != 8 {
		t.Errorf("veo durations = %#v (default %d), want [4 6 8] default 8", veoGenOption.GenerationOptions.Durations, veoGenOption.GenerationOptions.DefaultDur)
	}

	// When metadata is absent (e.g. openRouterVeoRecord has no ProviderSpecific), GenerationOptions must be nil (expose absence rather than inventing capabilities)
	var openRouterVeoOption *mediaCatalogOption
	for i := range response.VideoGenerationModels {
		if response.VideoGenerationModels[i].Model == "google/veo-3.1" {
			openRouterVeoOption = &response.VideoGenerationModels[i]
			break
		}
	}
	if openRouterVeoOption == nil {
		t.Fatalf("missing google/veo-3.1 in video generation models")
	}
	if openRouterVeoOption.GenerationOptions != nil {
		t.Errorf("expected nil generation_options for openrouter model without settings metadata, got %#v", openRouterVeoOption.GenerationOptions)
	}
}

func TestMediaCatalogResponsePopulatesAudioGenerationModels(t *testing.T) {
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	catalogStore := pebblestore.NewModelCatalogStore(store)
	pricing := json.RawMessage(`{"audio_output":0.08}`)

	audioProviderSpecific := json.RawMessage(`{"google":{"music_generation":{"settings":{"duration_seconds":{"status":"verified","default_value":30,"supported_values":[10,20,30]}}}}}`)
	lyriaSongRecord := pebblestore.ModelCatalogRecord{
		Provider: "google", Model: "lyria-3.5", DisplayName: "Lyria 3.5",
		CatalogModalities: pebblestore.ModelCatalogModalities{Inputs: []string{"text"}, Outputs: []string{"audio", "text"}, Categories: []string{"audio_generation"}},
		ProviderSpecific:  audioProviderSpecific,
		Pricing:           pricing,
	}
	lyriaClipRecord := pebblestore.ModelCatalogRecord{
		Provider: "google", Model: "lyria-3-clip-preview", DisplayName: "Lyria 3 Clip Preview",
		CatalogModalities: pebblestore.ModelCatalogModalities{Inputs: []string{"text"}, Outputs: []string{"audio", "text"}, Categories: []string{"audio_generation"}},
		Pricing:           json.RawMessage(`{"audio_output":0.04}`),
	}
	openRouterLyriaRecord := pebblestore.ModelCatalogRecord{
		Provider: "openrouter", Model: "google/lyria-3.5", DisplayName: "Google: Lyria 3.5",
		CatalogModalities: pebblestore.ModelCatalogModalities{Inputs: []string{"text"}, Outputs: []string{"audio", "text"}, Categories: []string{"audio_generation"}},
		Pricing:           pricing,
	}

	for _, rec := range []pebblestore.ModelCatalogRecord{lyriaSongRecord, lyriaClipRecord, openRouterLyriaRecord} {
		if err := catalogStore.SetRecord(rec); err != nil {
			t.Fatalf("seed record: %v", err)
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

	if !response.AudioReady || response.AudioStatus != "ready" {
		t.Fatalf("audio readiness = %v/%q, want true/ready", response.AudioReady, response.AudioStatus)
	}
	if len(response.AudioModels) != 3 {
		t.Fatalf("expected 3 audio generation models, got %d: %#v", len(response.AudioModels), response.AudioModels)
	}
	if response.AudioModels[0].Model != "lyria-3.5" {
		t.Errorf("top priority audio model = %q, want lyria-3.5", response.AudioModels[0].Model)
	}
	if response.AudioModels[1].Model != "lyria-3-clip-preview" {
		t.Errorf("second priority audio model = %q, want lyria-3-clip-preview", response.AudioModels[1].Model)
	}

	// Invariant: generation_options is populated from canonical model metadata for audio
	if response.AudioModels[0].GenerationOptions == nil {
		t.Fatalf("expected generation_options on lyria-3.5, got nil")
	}
	if len(response.AudioModels[0].GenerationOptions.Durations) != 3 || response.AudioModels[0].GenerationOptions.DefaultDur != 30 {
		t.Errorf("lyria durations = %#v (default %d), want [10 20 30] default 30", response.AudioModels[0].GenerationOptions.Durations, response.AudioModels[0].GenerationOptions.DefaultDur)
	}
	// Model without provider specific has nil GenerationOptions
	if response.AudioModels[1].GenerationOptions != nil {
		t.Errorf("expected nil generation_options on unconfigured lyria-3-clip-preview, got %#v", response.AudioModels[1].GenerationOptions)
	}
}

func TestMediaCatalogResponse_ExposesResolutionDurationsAndInitialImage(t *testing.T) {
	// Requirement: mediaCatalogResponse must expose exact resolution-dependent durations,
	// initial-image capability, and allowed image MIME types from snapshot metadata without inventing capabilities.
	// Threat/regression: Flat options maps discard conditional durations (e.g. 1080p requiring 8s) or omit initial-image constraints.
	// Boundary/authority: extractModelGenerationOptions and Server.mediaCatalogResponse in media_settings.go.
	// Test layer: API unit test using hermetic Pebble store with realistic snapshot ProviderSpecific fixture.

	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	catalogStore := pebblestore.NewModelCatalogStore(store)

	veoProviderSpecific := json.RawMessage(`{
		"google": {
			"video_generation": {
				"status": "verified",
				"settings": {
					"aspect_ratio": {
						"status": "verified",
						"default_value": "16:9",
						"supported_values": ["16:9", "9:16"]
					},
					"resolution": {
						"status": "verified",
						"default_value": "720p",
						"supported_values": ["720p", "1080p", "4k"],
						"variants": [
							{"mode": "all", "supported_values": ["720p"], "conditions": {}},
							{"mode": "all", "supported_values": ["1080p"], "conditions": {"duration_seconds": 8}},
							{"mode": "all", "supported_values": ["4k"], "conditions": {"duration_seconds": 8}}
						]
					},
					"duration_seconds": {
						"status": "verified",
						"default_value": 8,
						"supported_values": [4, 6, 8],
						"variants": [
							{"mode": "std_720p", "supported_values": [4, 6, 8], "conditions": {"resolution": "720p"}},
							{"mode": "hi_res", "supported_values": [8], "conditions": {"resolution": "1080p_or_4k"}}
						]
					}
				},
				"features": {
					"initial_image": {
						"status": "verified",
						"supported": true,
						"max_inputs": 1,
						"notes": "An initial image can be animated for image-to-video generation."
					}
				}
			}
		}
	}`)

	omniProviderSpecific := json.RawMessage(`{
		"google": {
			"video_generation": {
				"status": "partially_verified",
				"settings": {
					"aspect_ratio": {
						"status": "verified",
						"default_value": "16:9",
						"supported_values": ["16:9", "9:16"]
					},
					"resolution": {
						"status": "verified",
						"default_value": "720p",
						"supported_values": ["360p", "720p", "1080p", "4k"]
					},
					"duration_seconds": {
						"status": "unknown",
						"supported_values": []
					}
				},
				"features": {
					"conversational_editing": {
						"status": "verified",
						"supported": true
					},
					"initial_image": {
						"status": "verified",
						"supported": true,
						"max_inputs": 1
					}
				}
			}
		}
	}`)

	veoRecord := pebblestore.ModelCatalogRecord{
		Provider: "google", Model: "veo-3.1-generate-preview", DisplayName: "Veo 3.1",
		CatalogModalities: pebblestore.ModelCatalogModalities{Inputs: []string{"text", "image"}, Outputs: []string{"video"}},
		ProviderSpecific:  veoProviderSpecific,
	}
	omniRecord := pebblestore.ModelCatalogRecord{
		Provider: "google", Model: "gemini-omni-1.1-flash", DisplayName: "Gemini Omni 1.1 Flash",
		CatalogModalities: pebblestore.ModelCatalogModalities{Inputs: []string{"text", "image"}, Outputs: []string{"video"}},
		ProviderSpecific:  omniProviderSpecific,
	}

	for _, rec := range []pebblestore.ModelCatalogRecord{veoRecord, omniRecord} {
		if err := catalogStore.SetRecord(rec); err != nil {
			t.Fatalf("seed record: %v", err)
		}
	}

	server := NewServer(nil, nil, model.NewService(pebblestore.NewModelStore(store), nil, model.NewCatalogService(catalogStore)), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	server.imageGen = imagegen.NewService(nil, nil, nil, server.model)
	caps := imagegen.Capabilities{Providers: []imagegen.ProviderStatus{{ID: imagegen.ProviderGoogleGemini, Ready: true}}}
	openRouterStatus := imagegen.ProviderStatus{ID: "openrouter", Ready: false}

	response, err := server.mediaCatalogResponse(caps, openRouterStatus)
	if err != nil {
		t.Fatalf("mediaCatalogResponse failed: %v", err)
	}

	// 1. Veo resolution-dependent durations
	var veoOpt *mediaCatalogOption
	for i := range response.VideoGenerationModels {
		if response.VideoGenerationModels[i].Model == "veo-3.1-generate-preview" {
			veoOpt = &response.VideoGenerationModels[i]
			break
		}
	}
	if veoOpt == nil || veoOpt.GenerationOptions == nil {
		t.Fatalf("expected generation_options on veo-3.1-generate-preview")
	}
	rd := veoOpt.GenerationOptions.ResolutionDurations
	if len(rd["720p"]) != 3 || len(rd["1080p"]) != 1 || rd["1080p"][0] != 8 || len(rd["4k"]) != 1 || rd["4k"][0] != 8 {
		t.Errorf("unexpected resolution_durations for veo: %#v", rd)
	}
	if veoOpt.GenerationOptions.InitialImage == nil || !veoOpt.GenerationOptions.InitialImage.Supported || veoOpt.GenerationOptions.InitialImage.MaxInputs != 1 {
		t.Errorf("expected initial_image with max_inputs=1 for veo, got %#v", veoOpt.GenerationOptions.InitialImage)
	}

	// 2. Omni must omit unsupported resolutions and unknown durations (no invented selectors)
	var omniOpt *mediaCatalogOption
	for i := range response.VideoIterationModels {
		if response.VideoIterationModels[i].Model == "gemini-omni-1.1-flash" {
			omniOpt = &response.VideoIterationModels[i]
			break
		}
	}
	if omniOpt == nil || omniOpt.GenerationOptions == nil {
		t.Fatalf("expected generation_options on gemini-omni-1.1-flash")
	}
	if len(omniOpt.GenerationOptions.Resolutions) != 4 {
		t.Errorf("expected 4 resolutions for omni (360p, 720p, 1080p, 4k), got %#v", omniOpt.GenerationOptions.Resolutions)
	}
	if omniOpt.GenerationOptions.DefaultRes != "720p" {
		t.Errorf("expected default resolution 720p for omni, got %q", omniOpt.GenerationOptions.DefaultRes)
	}
	if len(omniOpt.GenerationOptions.Durations) != 0 {
		t.Errorf("expected empty durations for omni, got %#v", omniOpt.GenerationOptions.Durations)
	}
	if omniOpt.GenerationOptions.InitialImage == nil || !omniOpt.GenerationOptions.InitialImage.Supported {
		t.Errorf("expected initial_image supported for omni")
	}
}

func TestMediaCatalogResponse_NeverFallsBackToDifferentProvider(t *testing.T) {
	// Requirement: extractModelGenerationOptions must strictly match record.Provider against ProviderSpecific keys.
	// Threat/regression: An OpenRouter model record containing Google provider-specific payload silently inherits Google settings.
	// Boundary/authority: extractModelGenerationOptions in media_settings.go.
	// Test layer: Unit test verifying absence of fallback to first provider key.

	googlePayload := json.RawMessage(`{"google":{"video_generation":{"settings":{"resolution":{"status":"verified","default_value":"720p","supported_values":["720p"]}}}}}`)
	openRouterRec := pebblestore.ModelCatalogRecord{
		Provider:         "openrouter",
		Model:            "google/veo-3.1",
		ProviderSpecific: googlePayload,
	}
	opts := extractModelGenerationOptions(openRouterRec)
	if opts != nil {
		t.Fatalf("expected nil generation_options when provider 'openrouter' does not match payload key 'google', got %#v", opts)
	}
}
