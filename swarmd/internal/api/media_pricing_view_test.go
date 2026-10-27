package api

import (
	"encoding/json"
	"os"
	"testing"
)

// Purpose: swarm_media_models must expose embedded Lyria song prices and never
// claim verified pricing after filtering every rate. The model-view boundary is
// the narrowest layer proving the actual catalog-to-MCP projection regression.
func TestMediaPricingViewLyria(t *testing.T) {
	data, err := os.ReadFile("../model/snapshotdata/snapshot.json")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Models []struct {
			ID       string          `json:"model_id"`
			Provider string          `json:"provider_id"`
			Pricing  json.RawMessage `json:"pricing"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{"lyria-3.5": .08, "lyria-3-clip-preview": .04, "lyria-3-pro-preview": .08}
	for _, model := range snapshot.Models {
		rate, ok := want[model.ID]
		if !ok || model.Provider != "google" {
			continue
		}
		view := controlMCPMediaModelView(mediaCatalogOption{ID: model.ID, Provider: model.Provider, Pricing: model.Pricing})["pricing"].(map[string]any)
		prices := view["prices"].([]map[string]any)
		if view["billing_status"] != "verified" || len(prices) != 1 || prices[0]["unit"] != "song" || prices[0]["price_usd"] != rate {
			t.Fatalf("%s: %+v", model.ID, view)
		}
		delete(want, model.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing models: %v", want)
	}
	for _, raw := range []string{
		`{"billing":{"status":"verified","lines":[]}}`,
		`{"billing":{"status":"verified","lines":[{"unit":"song","price_usd":0.04,"conditions":{"service_tier":"batch"}}]}}`,
		`{"billing":{"status":"verified","lines":[{"unit":"unsupported","price_usd":1}]}}`,
	} {
		got := conciseMediaPricing(json.RawMessage(raw))
		if got["billing_status"] != "unknown" || len(got["prices"].([]map[string]any)) != 0 {
			t.Fatalf("empty verified pricing: %+v", got)
		}
	}
}
