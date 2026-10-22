package pebblestore

import (
	"encoding/json"
	"math"
	"testing"
)

// Purpose: EstimateMediaCostFromRecord must match standard catalog variants and
// measured modalities, never a cheaper batch or first-size line. This pure unit
// boundary is the narrowest layer exercising the shared billing formula.
func TestMediaCostCatalogImages(t *testing.T) {
	for _, tc := range []struct {
		name, model, size, variant string
		tokens                     int64
		price, want                float64
	}{
		{"nano measured", "gemini-nano-banana-2.1", "1K", "1K", 1120, .0336, .0336},
		{"nano 4K", "gemini-nano-banana-2.1", "4K", "4K", 0, .113, .113},
		{"flash 1K", "gemini-3.1-flash-image", "1K", "1K", 0, .067, .067},
		{"pro 2K", "gemini-3-pro-image", "2K", "1K_or_2K", 0, .134, .134},
		{"unknown size", "gemini-nano-banana-2.1", "8K", "4K", 0, .113, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := []any{
				map[string]any{"kind": "equivalent_cost", "billable": "image_output", "unit": "image", "price_usd": .001, "variant": tc.variant, "conditions": map[string]any{"service_tier": "batch"}},
				map[string]any{"kind": "equivalent_cost", "billable": "image_output", "unit": "image", "price_usd": .045, "variant": "0.5K"},
				map[string]any{"kind": "equivalent_cost", "billable": "image_output", "unit": "image", "price_usd": tc.price, "variant": tc.variant},
				map[string]any{"kind": "billing_rate", "billable": "image_output", "unit": "million_tokens", "price_usd": 15, "variant": "batch", "conditions": map[string]any{"tier": "paid", "service_tier": "batch"}},
				map[string]any{"kind": "billing_rate", "billable": "image_output", "unit": "million_tokens", "price_usd": 30, "variant": "standard", "conditions": map[string]any{"tier": "paid", "service_tier": "standard"}},
			}
			raw, _ := json.Marshal(map[string]any{"billing": map[string]any{"status": "verified", "lines": lines}})
			rec := ModelCatalogRecord{Provider: "google", Model: tc.model, Pricing: raw}
			opts := MediaCostEstimateOptions{Kind: "image", ImageSize: tc.size, OutputTokens: tc.tokens}
			got := EstimateMediaCostFromRecord(rec, opts)
			if math.Abs(got.CostUSD-tc.want) > 1e-10 {
				t.Fatalf("got %+v want %g", got, tc.want)
			}
			wantStatus := "known"
			if tc.want == 0 {
				wantStatus = "unknown"
			}
			if got.PriceStatus != wantStatus {
				t.Fatalf("got %+v", got)
			}
			raw, _ = json.Marshal(map[string]any{"billing": map[string]any{"status": "unverified", "lines": lines}})
			rec.Pricing = raw
			if got := EstimateMediaCostFromRecord(rec, opts); got.PriceStatus != "unknown" || got.CostUSD != 0 {
				t.Fatalf("unverified: %+v", got)
			}
		})
	}
}

// Purpose: the shared formula must charge prompt images separately from text
// and include thinking in text output; a unit test isolates arithmetic authority.
func TestMediaCostTokenBreakdown(t *testing.T) {
	lines := []any{}
	for key, rate := range map[string]float64{"image_output": 30, "text_output": 2, "text_input": 1, "image_input": 3} {
		lines = append(lines, map[string]any{"kind": "billing_rate", "billable": key, "unit": "million_tokens", "price_usd": rate, "conditions": map[string]any{"service_tier": "standard"}})
	}
	raw, _ := json.Marshal(map[string]any{"billing": map[string]any{"status": "verified", "lines": lines}})
	got := EstimateMediaCostFromRecord(ModelCatalogRecord{Pricing: raw}, MediaCostEstimateOptions{Kind: "image", OutputTokens: 1120, TextOutputTokens: 10, ThinkingTokens: 20, PromptTokens: 100, InputImageTokens: 200})
	if got.PriceStatus != "known" || math.Abs(got.CostUSD-.03436) > 1e-10 {
		t.Fatalf("%+v", got)
	}
}
