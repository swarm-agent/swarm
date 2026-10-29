package model

import (
	"encoding/json"
	"math"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: catalog materialization must supply published audio/text rates to
// EstimateMediaCostFromRecord without inventing rates for aliases or other
// providers. This boundary test proves token arithmetic and unknown failure
// behavior without provider calls or mutable remote snapshots.
func TestMediaPricingTranscriptionSupplement(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  float64
	}{
		{"gemini-2.5-flash", .00353}, {"gemini-2.5-flash-lite", .00071},
		{"gemini-3-flash-preview", .00405}, {"gemini-3.1-flash-lite", .002025},
		{"gemini-3.5-flash-lite", .00283},
	} {
		raw := supplementMediaPricing("google", tc.model, json.RawMessage(`{"currency":"unknown","billing":{"status":"unknown","lines":[]}}`))
		rec := pebblestore.ModelCatalogRecord{Provider: "google", Model: tc.model, Pricing: raw}
		opts := pebblestore.MediaCostEstimateOptions{Kind: "audio", Transcription: true, PromptTokens: 100, AudioInputTokens: 1000, TextOutputTokens: 1000}
		got := pebblestore.EstimateMediaCostFromRecord(rec, opts)
		if got.PriceStatus != "known" || math.Abs(got.CostUSD-tc.want) > 1e-12 {
			t.Fatalf("%s: %+v", tc.model, got)
		}
		opts.AudioInputTokens = 0
		if got := pebblestore.EstimateMediaCostFromRecord(rec, opts); got.PriceStatus != "unknown" {
			t.Fatalf("missing audio usage: %+v", got)
		}
	}
	unknown := json.RawMessage(`{"billing":{"status":"unknown","lines":[]}}`)
	for _, pair := range [][2]string{{"google", "unpublished"}, {"vertex", "gemini-2.5-flash"}} {
		if got := supplementMediaPricing(pair[0], pair[1], unknown); string(got) != string(unknown) {
			t.Fatalf("invented rate: %s", got)
		}
	}
	existing := json.RawMessage(`{"billing":{"status":"verified","lines":[{"price_usd":7}]}}`)
	if got := supplementMediaPricing("google", "gemini-2.5-flash", existing); string(got) != string(existing) {
		t.Fatal("overwrote upstream prices")
	}
}

// Purpose: real embedded music, speech, image and video billing rows must be
// consumable by the receipt estimator. This catalog/estimator boundary catches
// unit mismatches without live generation; it does not prove worker admission.
// gemini-3.8-flash timing transcription lists only introductory speech rows, so
// it must be priced at the model's token rates rather than recorded as unknown.
func TestMediaPricingEmbeddedKinds(t *testing.T) {
	var snapshot struct {
		Models []struct {
			Provider string          `json:"provider_id"`
			Model    string          `json:"model_id"`
			Pricing  json.RawMessage `json:"pricing"`
		} `json:"models"`
	}
	if err := json.Unmarshal(pinnedSwarmSnapshotJSON, &snapshot); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		model string
		opts  pebblestore.MediaCostEstimateOptions
		want  float64
	}{
		{"lyria-3.5", pebblestore.MediaCostEstimateOptions{Kind: "audio"}, .08},
		{"lyria-3-clip-preview", pebblestore.MediaCostEstimateOptions{Kind: "audio"}, .04},
		{"lyria-3-pro-preview", pebblestore.MediaCostEstimateOptions{Kind: "audio"}, .08},
		{"gemini-3.8-flash-tts", pebblestore.MediaCostEstimateOptions{Kind: "audio", Speech: true, PromptTokens: 100, AudioOutputTokens: 100}, .00095},
		{"gemini-3.8-flash", pebblestore.MediaCostEstimateOptions{Kind: "audio", Transcription: true, PromptTokens: 100, AudioInputTokens: 1000, TextOutputTokens: 1000}, .004575},
		{"gemini-3.1-flash-lite-image", pebblestore.MediaCostEstimateOptions{Kind: "image", OutputTokens: 1120}, .0336},
		{"veo-3.1-lite-generate-preview", pebblestore.MediaCostEstimateOptions{Kind: "video", DurationSeconds: 8, Resolution: "720p", IncludesAudio: true}, .4},
	} {
		found := false
		for _, m := range snapshot.Models {
			if m.Provider != "google" || m.Model != tc.model {
				continue
			}
			found = true
			got := pebblestore.EstimateMediaCostFromRecord(pebblestore.ModelCatalogRecord{Provider: m.Provider, Model: m.Model, Pricing: m.Pricing}, tc.opts)
			if got.PriceStatus != "known" || math.Abs(got.CostUSD-tc.want) > 1e-10 {
				t.Fatalf("%s: %+v", tc.model, got)
			}
		}
		if !found {
			t.Fatalf("missing %s", tc.model)
		}
	}
}

// Purpose: published but conditional rates must not become unconditional prices.
// supplementMediaPricing and EstimateMediaCostFromRecord are the narrowest
// boundary proving that unsupported expiry/context tiers remain fail-closed.
func TestMediaPricingUnsupportedConditions(t *testing.T) {
	for _, model := range []string{"gemini-3.6-flash", "gemini-2.5-pro", "gemini-3.1-pro-preview", "gemini-3.1-pro-preview-customtools"} {
		raw := json.RawMessage(`{"billing":{"status":"unknown","lines":[]}}`)
		got := supplementMediaPricing("google", model, raw)
		if string(got) != string(raw) {
			t.Fatalf("invented unconditional rate for %s", model)
		}
		cost := pebblestore.EstimateMediaCostFromRecord(pebblestore.ModelCatalogRecord{Provider: "google", Model: model, Pricing: got}, pebblestore.MediaCostEstimateOptions{Transcription: true, PromptTokens: 100, AudioInputTokens: 1000, TextOutputTokens: 1000})
		if cost.PriceStatus != "unknown" || cost.CostUSD != 0 {
			t.Fatalf("%s: %+v", model, cost)
		}
	}
}
