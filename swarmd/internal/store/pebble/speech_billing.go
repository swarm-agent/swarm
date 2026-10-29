package pebblestore

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// Speech pricing never treats audio token prices as per-generation prices.
// Missing, ambiguous or conditional rates remain explicitly unknown.
func estimateSpeechBilling(rec ModelCatalogRecord, opts MediaCostEstimateOptions, lines []map[string]any) MediaCostEstimate {
	result := estimateSpeechBillingLines(rec, opts, lines)
	if result.PriceStatus == "known" || !opts.Transcription {
		return result
	}
	if cost, ok := transcriptionTokenCost(rec, opts, lines); ok {
		result.CostUSD, result.PriceStatus = cost, "known"
		result.PricingSummary = fmt.Sprintf("$%.8f transcription at model token rates (catalog %s)", cost, rec.SourceSnapshotID)
	}
	return result
}

func estimateSpeechBillingLines(rec ModelCatalogRecord, opts MediaCostEstimateOptions, lines []map[string]any) MediaCostEstimate {
	result := MediaCostEstimate{PriceStatus: "unknown", PricingSummary: "unknown speech/transcription pricing or usage", SnapshotID: rec.SourceSnapshotID, SnapshotVersion: rec.SourceSnapshotVersion}
	seen := map[string]bool{}
	total := 0.0
	wholeGeneration := false
	for _, line := range lines {
		billable, _ := line["billable"].(string)
		unit, _ := line["unit"].(string)
		variant, _ := line["variant"].(string)
		if variant != "" && variant != "standard" {
			continue
		}
		if conditions, ok := line["conditions"].(map[string]any); ok {
			allowed := true
			for key, value := range conditions {
				if !(key == "tier" && value == "paid" || key == "service_tier" && value == "standard") {
					allowed = false
				}
			}
			if !allowed {
				continue
			}
		}
		rate, ok := toFloat64(line["price_usd"])
		if !ok || rate < 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
			continue
		}
		quantity := float64(0)
		component := ""
		switch billable {
		case "text_input", "input_text", "prompt", "input":
			quantity, component = float64(opts.PromptTokens), "input"
		case "audio_output", "output_audio":
			if opts.Speech {
				quantity, component = float64(opts.AudioOutputTokens), "output"
			}
		case "audio_input", "input_audio":
			if opts.Transcription {
				quantity, component = float64(opts.AudioInputTokens), "audio_input"
			}
		case "text_output", "output_text", "completion", "output":
			if opts.Transcription {
				quantity, component = float64(opts.TextOutputTokens), "output"
			}
		}
		if component == "" {
			continue
		}
		switch strings.ToLower(unit) {
		case "million_tokens":
			quantity /= 1e6
		case "token":
		case "character":
			wholeGeneration = true
			if !opts.Speech {
				continue
			}
			quantity = float64(opts.InputCharacters)
		case "million_characters":
			wholeGeneration = true
			if !opts.Speech {
				continue
			}
			quantity = float64(opts.InputCharacters) / 1e6
		case "second":
			wholeGeneration = opts.Speech
			quantity = float64(opts.DurationSeconds)
		default:
			continue
		}
		if quantity <= 0 {
			continue
		}
		if seen[component] {
			return result
		}
		seen[component] = true
		total += rate * quantity
	}
	if (!wholeGeneration && (!seen["input"] || !seen["output"])) || len(seen) == 0 || (opts.Transcription && !seen["audio_input"]) {
		return result
	}
	result.CostUSD, result.PriceStatus = total, "known"
	result.PricingSummary = fmt.Sprintf("$%.8f speech/transcription (catalog %s)", total, rec.SourceSnapshotID)
	return result
}

// transcriptionTokenCost prices a timing transcription the way the provider
// bills it: an ordinary generateContent call with audio input, at the model's
// catalog token rates (the same rates model turns use). Audio input uses the
// highest standard-tier audio_input row the catalog lists for the model, never
// less than the text input rate, so audio is not under-charged at a text rate.
// Missing token rates or missing audio usage stay unknown.
func transcriptionTokenCost(rec ModelCatalogRecord, opts MediaCostEstimateOptions, lines []map[string]any) (float64, bool) {
	var p struct {
		Input  *float64 `json:"input_price_per_million_tokens"`
		Output *float64 `json:"output_price_per_million_tokens"`
	}
	if json.Unmarshal(rec.Pricing, &p) != nil || p.Input == nil || p.Output == nil {
		return 0, false
	}
	input, output := *p.Input, *p.Output
	if !validTokenRate(input) || !validTokenRate(output) || input == 0 || output == 0 {
		return 0, false
	}
	if opts.AudioInputTokens <= 0 || opts.PromptTokens < 0 || opts.TextOutputTokens < 0 {
		return 0, false
	}
	audio := input
	for _, line := range lines {
		billable, _ := line["billable"].(string)
		unit, _ := line["unit"].(string)
		if (billable != "audio_input" && billable != "input_audio") || !strings.EqualFold(unit, "million_tokens") {
			continue
		}
		conditions, _ := line["conditions"].(map[string]any)
		if tier, _ := conditions["service_tier"].(string); tier != "" && tier != "standard" {
			continue
		}
		if cached, _ := conditions["cached"].(bool); cached {
			continue
		}
		if rate, ok := toFloat64(line["price_usd"]); ok && validTokenRate(rate) && rate > audio {
			audio = rate
		}
	}
	cost := (float64(opts.PromptTokens)*input + float64(opts.AudioInputTokens)*audio + float64(opts.TextOutputTokens)*output) / 1e6
	return cost, true
}

func validTokenRate(rate float64) bool {
	return rate >= 0 && !math.IsNaN(rate) && !math.IsInf(rate, 0)
}
