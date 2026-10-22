package pebblestore

import (
	"fmt"
	"math"
	"strings"
)

func estimateImageBilling(rec ModelCatalogRecord, opts MediaCostEstimateOptions, lines []map[string]any) MediaCostEstimate {
	result := MediaCostEstimate{PriceStatus: "unknown", PricingSummary: fmt.Sprintf("unknown pricing (no matching standard image rate for size %q)", opts.ImageSize), SnapshotID: rec.SourceSnapshotID, SnapshotVersion: rec.SourceSnapshotVersion}
	size := opts.ImageSize
	if size == "" {
		size = opts.Resolution
	}
	rates := map[string]float64{}
	ambiguous := map[string]bool{}
	var prices []float64
	for _, line := range lines {
		variant, _ := line["variant"].(string)
		lower := strings.ToLower(variant)
		if strings.HasPrefix(lower, "batch") || strings.HasPrefix(lower, "flex") || strings.HasPrefix(lower, "priority") {
			continue
		}
		conds, _ := line["conditions"].(map[string]any)
		matches := true
		for key, value := range conds {
			v, _ := value.(string)
			switch key {
			case "service_tier":
				matches = v == "" || strings.EqualFold(v, "standard")
			case "tier":
				matches = v == "" || strings.EqualFold(v, "paid")
			case "resolution", "image_size":
				matches = strings.EqualFold(v, size)
			case "aspect_ratio":
				matches = strings.EqualFold(v, opts.AspectRatio)
			default:
				matches = false
			}
			if !matches {
				break
			}
		}
		if !matches {
			continue
		}
		price, ok := toFloat64(line["price_usd"])
		if !ok || price < 0 || math.IsNaN(price) || math.IsInf(price, 0) {
			continue
		}
		unit, _ := line["unit"].(string)
		billable, _ := line["billable"].(string)
		kind, _ := line["kind"].(string)
		if (unit == "million_tokens" || unit == "token") && kind != "equivalent_cost" {
			if unit == "token" {
				price *= 1e6
			}
			if previous, exists := rates[billable]; exists && previous != price {
				ambiguous[billable] = true
			}
			rates[billable] = price
		}
		if (unit == "image" || unit == "generation") && (billable == "image_output" || billable == "image" || billable == "image_generation") {
			for _, candidate := range strings.Split(lower, "_or_") {
				if size != "" && strings.EqualFold(candidate, size) {
					prices = append(prices, price)
					break
				}
			}
		}
	}
	if opts.OutputTokens > 0 {
		rate, ok := rates["image_output"]
		if !ok || ambiguous["image_output"] {
			return result
		}
		cost := float64(opts.OutputTokens) * rate / 1e6
		parts := []string{fmt.Sprintf("%d image tok × $%g/M", opts.OutputTokens, rate)}
		for _, item := range []struct {
			key, label string
			count      int64
		}{{"text_output", "text", opts.TextOutputTokens + opts.ThinkingTokens}, {"text_input", "prompt", opts.PromptTokens}, {"image_input", "input image", opts.InputImageTokens}} {
			if ambiguous[item.key] {
				return result
			}
			if r, exists := rates[item.key]; exists {
				cost += float64(item.count) * r / 1e6
				parts = append(parts, fmt.Sprintf("%d %s tok × $%g/M", item.count, item.label, r))
			}
		}
		result.CostUSD = cost * float64(opts.Count)
		result.PriceStatus = "known"
		result.PricingSummary = fmt.Sprintf("%s = $%g (catalog %s)", strings.Join(parts, " + "), result.CostUSD, rec.SourceSnapshotID)
		return result
	}
	if len(prices) == 0 {
		return result
	}
	for _, price := range prices {
		if price != prices[0] {
			result.PricingSummary = "unknown pricing (ambiguous image rates)"
			return result
		}
	}
	result.CostUSD = prices[0] * float64(opts.Count)
	result.PriceStatus = "known"
	result.PricingSummary = fmt.Sprintf("$%g per %s image (catalog %s)", prices[0], size, rec.SourceSnapshotID)
	return result
}
