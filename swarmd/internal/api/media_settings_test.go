package api

import (
	"encoding/json"
	"testing"

	"swarm/packages/swarmd/internal/imagegen"
	"swarm/packages/swarmd/internal/model"
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
	for _, record := range []pebblestore.ModelCatalogRecord{
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
	server.imageGen = &imagegen.Service{}
	response, err := server.mediaCatalogResponse(imagegen.Capabilities{Providers: []imagegen.ProviderStatus{{ID: imagegen.ProviderGoogleGemini, Ready: true}}})
	if err != nil {
		t.Fatalf("mediaCatalogResponse: %v", err)
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
