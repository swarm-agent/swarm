package pebblestore

import (
	"fmt"
	"math"
	"strings"
)

// Speech pricing never treats audio token prices as per-generation prices.
// Missing, ambiguous or conditional rates remain explicitly unknown.
func estimateSpeechBilling(rec ModelCatalogRecord, opts MediaCostEstimateOptions, lines []map[string]any) MediaCostEstimate {
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
