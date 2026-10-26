package model

import "encoding/json"

// supplementMediaPricing fills exact, published standard-tier Gemini rates
// missing from upstream snapshots. Never infer aliases or prices for retired
// models. Keep the evidence on the billing rows, just like snapshot prices.
func supplementMediaPricing(provider, model string, pricing json.RawMessage) json.RawMessage {
	if provider != "google" {
		return cloneRawJSON(pricing)
	}
	rates, ok := map[string][3]float64{
		"gemini-2.5-flash":       {0.30, 1.00, 2.50},
		"gemini-2.5-flash-lite":  {0.10, 0.30, 0.40},
		"gemini-3-flash-preview": {0.50, 1.00, 3.00},
		"gemini-3.1-flash-lite":  {0.25, 0.50, 1.50},
	}[model]
	if !ok {
		return cloneRawJSON(pricing)
	}
	var raw map[string]any
	if json.Unmarshal(pricing, &raw) != nil || raw == nil {
		return cloneRawJSON(pricing)
	}
	if billing, ok := raw["billing"].(map[string]any); ok {
		if lines, ok := billing["lines"].([]any); ok && len(lines) > 0 {
			return cloneRawJSON(pricing)
		}
	}
	lines := []any{}
	for i, billable := range []string{"text_input", "audio_input", "text_output"} {
		lines = append(lines, map[string]any{
			"kind": "billing_rate", "billable": billable, "unit": "million_tokens", "price_usd": rates[i],
			"conditions": map[string]any{"tier": "paid", "service_tier": "standard"},
			"source_ids": []string{"google_gemini_pricing_docs"},
			"source_url": "https://ai.google.dev/gemini-api/docs/pricing", "verified_at": "2026-10-26",
		})
	}
	raw["currency"] = "USD"
	raw["billing"] = map[string]any{"status": "partially_verified", "lines": lines, "notes": "Published standard-tier text/audio token rates; other service tiers and billing components are not supplied by this supplement."}
	out, err := json.Marshal(raw)
	if err != nil {
		return cloneRawJSON(pricing)
	}
	return out
}
