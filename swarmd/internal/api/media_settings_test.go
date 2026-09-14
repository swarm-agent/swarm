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

	veoRecord := pebblestore.ModelCatalogRecord{
		Provider: "google", Model: "veo-3.1-generate-preview", DisplayName: "Veo 3.1",
		CatalogModalities: pebblestore.ModelCatalogModalities{Inputs: []string{"text"}, Outputs: []string{"video"}},
		Pricing: pricing,
	}
	omniRecord := pebblestore.ModelCatalogRecord{
		Provider: "google", Model: "gemini-omni-1.1-flash", DisplayName: "Gemini Omni 1.1 Flash",
		CatalogModalities: pebblestore.ModelCatalogModalities{Inputs: []string{"text"}, Outputs: []string{"video"}},
		ProviderSpecific: json.RawMessage(`{"google":{"video_generation":{"features":{"conversational_editing":{"supported":true}}}}}`),
		Pricing: pricing,
	}
	openRouterVeoRecord := pebblestore.ModelCatalogRecord{
		Provider: "openrouter", Model: "google/veo-3.1", DisplayName: "Google: Veo 3.1",
		CatalogModalities: pebblestore.ModelCatalogModalities{Inputs: []string{"text"}, Outputs: []string{"video"}},
		Pricing: pricing,
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
}
