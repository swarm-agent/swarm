package pebblestore

import (
	"encoding/json"
	"math"
	"strings"
	"swarm/packages/swarmd/internal/store/pebble/pricingtest"
	"testing"
)

// Purpose: EstimateMediaCostFromRecord must bill measured video seconds, not
// a missing request option. This pure pricing boundary proves fractional billing,
// request fallback and fail-closed duration/resolution matching without providers.
func TestVideoMeasuredDurationPricing(t *testing.T) {
	pricing, _ := pricingtest.Record(t, "gemini-omni-1.1-flash")
	rec := ModelCatalogRecord{Model: "gemini-omni-1.1-flash", Pricing: pricing}
	for _, tc := range []struct {
		name       string
		requested  int
		measured   float64
		resolution string
		want       float64
		known      bool
	}{
		{"omni", 0, 4.01, "720p", .401, true},
		{"measured wins", 8, 4.01, "720p", .401, true},
		{"request fallback", 8, 0, "720p", .8, true},
		{"missing", 0, 0, "720p", 0, false},
		{"invalid measured fallback", 8, math.NaN(), "720p", .8, true},
		{"wrong resolution", 0, 4.01, "1080p", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := EstimateMediaCostFromRecord(rec, MediaCostEstimateOptions{Kind: "video", DurationSeconds: tc.requested, MeasuredDurationSeconds: tc.measured, Resolution: tc.resolution})
			if math.Abs(got.CostUSD-tc.want) > 1e-10 || (got.PriceStatus == "known") != tc.known {
				t.Fatalf("%+v", got)
			}
			if tc.name == "missing" && !strings.Contains(got.PricingSummary, "duration unavailable") {
				t.Fatalf("missing reason: %+v", got)
			}
		})
	}
}

// Purpose: the equivalent-rate exception in EstimateMediaCostFromRecord must
// fail closed without conversion metadata or matching tier, and identify its
// approximate nature. Mutating the shipped record is the narrowest negative test.
func TestVideoEquivalentRateConditions(t *testing.T) {
	pricing, _ := pricingtest.Record(t, "gemini-omni-1.1-flash")
	for _, value := range []any{nil, 0, -1, "5792"} {
		var p map[string]any
		if err := json.Unmarshal(pricing, &p); err != nil {
			t.Fatal(err)
		}
		for _, raw := range p["billing"].(map[string]any)["lines"].([]any) {
			line := raw.(map[string]any)
			if line["kind"] == "equivalent_cost" {
				line["conditions"].(map[string]any)["output_tokens_per_second"] = value
			}
		}
		data, _ := json.Marshal(p)
		got := EstimateMediaCostFromRecord(ModelCatalogRecord{Pricing: data}, MediaCostEstimateOptions{Kind: "video", Resolution: "720p", MeasuredDurationSeconds: 4.01})
		if got.PriceStatus != "unknown" || got.CostUSD != 0 {
			t.Fatalf("invalid rate %v: %+v", value, got)
		}
	}
	for _, tier := range []string{"standard", "priority"} {
		got := EstimateMediaCostFromRecord(ModelCatalogRecord{Pricing: pricing}, MediaCostEstimateOptions{Kind: "video", Resolution: "720p", MeasuredDurationSeconds: 4.01, ServiceTier: tier})
		if tier == "standard" {
			if !strings.Contains(got.PricingSummary, "approximate published equivalent") {
				t.Fatalf("%+v", got)
			}
		} else if got.PriceStatus != "unknown" || got.CostUSD != 0 {
			t.Fatalf("%+v", got)
		}
	}
}

// Purpose: accepting equivalent video rates must not change Veo's canonical
// per-second billing. The shipped record and pure pricing boundary prove the
// requested eight-second fallback still uses the matching audio/resolution rate.
func TestVeoRequestedDurationPricing(t *testing.T) {
	pricing, _ := pricingtest.Record(t, "veo-3.1-generate-preview")
	got := EstimateMediaCostFromRecord(ModelCatalogRecord{Pricing: pricing}, MediaCostEstimateOptions{Kind: "video", Resolution: "720p", DurationSeconds: 8, IncludesAudio: true})
	if got.PriceStatus != "known" || math.Abs(got.CostUSD-3.2) > 1e-10 {
		t.Fatalf("%+v", got)
	}
}
