package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

// UsageLimitRecord defines the daily spending and token limits configured for an account.
type UsageLimitRecord struct {
	AccountScopeID    string  `json:"account_scope_id"`
	DailyCostLimitUSD float64 `json:"daily_cost_limit_usd"`
	DailyTokensLimit  int64   `json:"daily_tokens_limit,omitempty"`
	Enabled           bool    `json:"enabled"`
	UpdatedAt         int64   `json:"updated_at"`
}

// DailyUsageAccumulator tracks aggregated daily spending and token counts in O(1) storage.
type DailyUsageAccumulator struct {
	AccountScopeID      string           `json:"account_scope_id"`
	Date                string           `json:"date"` // Format: YYYY-MM-DD (UTC)
	TotalCostUSD        float64          `json:"total_cost_usd"`
	CodexNominalCostUSD float64          `json:"codex_nominal_cost_usd,omitempty"`
	TotalTokens         int64            `json:"total_tokens"`
	InputTokens         int64            `json:"input_tokens,omitempty"`
	OutputTokens        int64            `json:"output_tokens,omitempty"`
	CachedTokens        int64            `json:"cached_tokens,omitempty"`
	ThinkingTokens      int64            `json:"thinking_tokens,omitempty"`
	TurnCount           int              `json:"turn_count"`
	MediaCalls          int              `json:"media_calls,omitempty"`
	MediaCostUSD        float64          `json:"media_cost_usd,omitempty"`
	ImageCount          int              `json:"image_count,omitempty"`
	ImageCostUSD        float64          `json:"image_cost_usd,omitempty"`
	VideoCount          int              `json:"video_count,omitempty"`
	VideoCostUSD        float64          `json:"video_cost_usd,omitempty"`
	AudioCount          int              `json:"audio_count,omitempty"`
	AudioCostUSD        float64          `json:"audio_cost_usd,omitempty"`
	ModelsUsed          map[string]int64 `json:"models_used,omitempty"`
	UpdatedAt           int64            `json:"updated_at"`
}

// ModelBaselinePricing holds standard per-million token rates for usage estimation.
type ModelBaselinePricing struct {
	InputPricePerMillion       float64
	OutputPricePerMillion      float64
	CachedInputPricePerMillion float64
	HasCached                  bool
}

var baselinePricingTable = map[string]ModelBaselinePricing{
	"anthropic:claude-3-7-sonnet":   {InputPricePerMillion: 3.0, OutputPricePerMillion: 15.0, CachedInputPricePerMillion: 0.30, HasCached: true},
	"anthropic:claude-3-5-sonnet":   {InputPricePerMillion: 3.0, OutputPricePerMillion: 15.0, CachedInputPricePerMillion: 0.30, HasCached: true},
	"anthropic:claude-3-5-haiku":    {InputPricePerMillion: 0.80, OutputPricePerMillion: 4.0, CachedInputPricePerMillion: 0.08, HasCached: true},
	"anthropic:claude-sonnet-5":     {InputPricePerMillion: 2.0, OutputPricePerMillion: 10.0, CachedInputPricePerMillion: 0.20, HasCached: true},
	"anthropic:claude-opus-5":       {InputPricePerMillion: 5.0, OutputPricePerMillion: 25.0, CachedInputPricePerMillion: 0.50, HasCached: true},
	"anthropic:claude-fable-5-1":    {InputPricePerMillion: 10.0, OutputPricePerMillion: 50.0, CachedInputPricePerMillion: 1.0, HasCached: true},
	"google:gemini-3.8-flash":       {InputPricePerMillion: 0.75, OutputPricePerMillion: 3.75, CachedInputPricePerMillion: 0.075, HasCached: true},
	"google:gemini-3.7-flash":       {InputPricePerMillion: 0.75, OutputPricePerMillion: 3.75, CachedInputPricePerMillion: 0.075, HasCached: true},
	"google:gemini-3.6-flash":       {InputPricePerMillion: 1.50, OutputPricePerMillion: 7.50, CachedInputPricePerMillion: 0.15, HasCached: true},
	"google:gemini-3.5-flash-lite":  {InputPricePerMillion: 0.30, OutputPricePerMillion: 2.50, CachedInputPricePerMillion: 0.03, HasCached: true},
	"google:gemini-omni-1.1-flash":  {InputPricePerMillion: 1.50, OutputPricePerMillion: 9.00, CachedInputPricePerMillion: 0.375, HasCached: true},
	"google:gemini-2.5-flash":       {InputPricePerMillion: 0.075, OutputPricePerMillion: 0.30, CachedInputPricePerMillion: 0.01875, HasCached: true},
	"google:gemini-2.5-pro":         {InputPricePerMillion: 1.25, OutputPricePerMillion: 5.00, CachedInputPricePerMillion: 0.3125, HasCached: true},
	"google:gemini-1.5-flash":       {InputPricePerMillion: 0.075, OutputPricePerMillion: 0.30, CachedInputPricePerMillion: 0.01875, HasCached: true},
	"google:gemini-1.5-pro":         {InputPricePerMillion: 1.25, OutputPricePerMillion: 5.00, CachedInputPricePerMillion: 0.3125, HasCached: true},
	"fireworks:deepseek-v3p2":       {InputPricePerMillion: 0.27, OutputPricePerMillion: 1.10, CachedInputPricePerMillion: 0.135, HasCached: true},
	"fireworks:deepseek-v4p1-flash": {InputPricePerMillion: 0.22, OutputPricePerMillion: 0.66, CachedInputPricePerMillion: 0.11, HasCached: true},
	"fireworks:glm-5p3-flash":       {InputPricePerMillion: 0.15, OutputPricePerMillion: 0.50, CachedInputPricePerMillion: 0.075, HasCached: true},
	"openai:gpt-6-astra":            {InputPricePerMillion: 10.0, OutputPricePerMillion: 50.0, CachedInputPricePerMillion: 5.0, HasCached: true},
	"openai:gpt-5.6-sol":            {InputPricePerMillion: 5.0, OutputPricePerMillion: 30.0, CachedInputPricePerMillion: 2.5, HasCached: true},
	"openai:gpt-5.6-luna":           {InputPricePerMillion: 1.0, OutputPricePerMillion: 6.0, CachedInputPricePerMillion: 0.5, HasCached: true},
	"openai:gpt-5.6-terra":          {InputPricePerMillion: 2.5, OutputPricePerMillion: 15.0, CachedInputPricePerMillion: 1.25, HasCached: true},
	"openai:gpt-5.5":                {InputPricePerMillion: 5.0, OutputPricePerMillion: 30.0, CachedInputPricePerMillion: 2.5, HasCached: true},
	"openai:gpt-5.4":                {InputPricePerMillion: 2.5, OutputPricePerMillion: 15.0, CachedInputPricePerMillion: 1.25, HasCached: true},
	"openai:gpt-5.4-mini":           {InputPricePerMillion: 0.75, OutputPricePerMillion: 4.5, CachedInputPricePerMillion: 0.375, HasCached: true},
	"openai:gpt-4o":                 {InputPricePerMillion: 2.50, OutputPricePerMillion: 10.0, CachedInputPricePerMillion: 1.25, HasCached: true},
	"openai:gpt-4o-mini":            {InputPricePerMillion: 0.15, OutputPricePerMillion: 0.60, CachedInputPricePerMillion: 0.075, HasCached: true},
}

// CalculateBaselineCost computes estimated cost in USD based on model pricing per million tokens.
func CalculateBaselineCost(provider, model string, inputTokens, outputTokens, cacheReadTokens, thinkingTokens int64) float64 {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.ToLower(strings.TrimSpace(model))

	// Look up by provider:model first, then model
	pricing, ok := baselinePricingTable[provider+":"+model]
	if !ok {
		pricing, ok = baselinePricingTable[model]
	}
	if !ok {
		// If absent from snapshot pricing and baseline, do not invent fallback rates.
		return 0.0
	}

	regularInput := inputTokens - cacheReadTokens
	if regularInput < 0 {
		regularInput = 0
	}

	var cost float64
	cost += (float64(regularInput) / 1_000_000.0) * pricing.InputPricePerMillion
	if pricing.HasCached && pricing.CachedInputPricePerMillion > 0 {
		cost += (float64(cacheReadTokens) / 1_000_000.0) * pricing.CachedInputPricePerMillion
	} else {
		cost += (float64(cacheReadTokens) / 1_000_000.0) * pricing.InputPricePerMillion
	}

	totalOutput := outputTokens + thinkingTokens
	cost += (float64(totalOutput) / 1_000_000.0) * pricing.OutputPricePerMillion
	return cost
}

// CalculateCost computes estimated cost in USD based on stored catalog pricing.
// If pricing is absent or unpriced, it returns 0.0 without guessing fallback rates.
func (s *SessionStore) CalculateCost(provider, model string, inputTokens, outputTokens, cacheReadTokens, thinkingTokens int64) float64 {
	cost, _ := s.CalculateCostWithStatus(provider, model, inputTokens, outputTokens, cacheReadTokens, thinkingTokens)
	return cost
}

// CalculateCostWithStatus evaluates cost and reports whether the pricing is known, subscription, free, or unknown.
func (s *SessionStore) CalculateCostWithStatus(provider, model string, inputTokens, outputTokens, cacheReadTokens, thinkingTokens int64) (float64, string) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.ToLower(strings.TrimSpace(model))
	if provider == "codex" {
		return 0.0, "subscription"
	}
	if s != nil && s.store != nil {
		catalogStore := NewModelCatalogStore(s.store)
		if rec, found, err := catalogStore.GetRecord(provider, model); err == nil && found && len(rec.Pricing) > 0 {
			var p struct {
				InputPricePerMillion       *float64 `json:"input_price_per_million_tokens"`
				OutputPricePerMillion      *float64 `json:"output_price_per_million_tokens"`
				CachedInputPricePerMillion *float64 `json:"cached_input_price_per_million_tokens"`
				IsFree                     *bool    `json:"is_free"`
			}
			if err := json.Unmarshal(rec.Pricing, &p); err == nil {
				if p.IsFree != nil && *p.IsFree {
					return 0.0, "free"
				}
				inp := 0.0
				if p.InputPricePerMillion != nil {
					inp = *p.InputPricePerMillion
				}
				outVal := 0.0
				if p.OutputPricePerMillion != nil {
					outVal = *p.OutputPricePerMillion
				}
				cachedVal := 0.0
				hasCached := false
				if p.CachedInputPricePerMillion != nil {
					cachedVal = *p.CachedInputPricePerMillion
					hasCached = true
				}
				if inp > 0 || outVal > 0 {
					regInput := inputTokens - cacheReadTokens
					if regInput < 0 {
						regInput = 0
					}
					var cost float64
					cost += (float64(regInput) / 1_000_000.0) * inp
					if hasCached && cachedVal > 0 {
						cost += (float64(cacheReadTokens) / 1_000_000.0) * cachedVal
					} else {
						cost += (float64(cacheReadTokens) / 1_000_000.0) * inp
					}
					cost += (float64(outputTokens+thinkingTokens) / 1_000_000.0) * outVal
					return cost, "known"
				}
			}
		}
	}
	return 0.0, "unknown"
}

// MediaCostEstimate captures resolved media pricing without invented fallback rates.
type MediaCostEstimate struct {
	CostUSD         float64 `json:"cost_usd"`
	PriceStatus     string  `json:"price_status"` // "known", "unknown", "subscription", "free"
	PricingSummary  string  `json:"pricing_summary"`
	SnapshotID      string  `json:"snapshot_id,omitempty"`
	SnapshotVersion string  `json:"snapshot_version,omitempty"`
}

func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		if f, err := n.Float64(); err == nil {
			return f, true
		}
	}
	return 0, false
}

// MediaCostEstimateOptions holds concrete dimensions for media pricing without guessing.
type MediaCostEstimateOptions struct {
	Provider        string
	Model           string
	Kind            string // "image", "video", "audio"
	Count           int
	DurationSeconds int
	Resolution      string
	ImageSize       string
	AspectRatio     string
	IncludesAudio   bool
	IsIteration     bool
	OutputTokens    int64
	ServiceTier     string
}

// EstimateMediaCostWithOptions resolves snapshot-backed media pricing for images, videos, and audio.
// If the model is unpriced or absent from the snapshot, it explicitly marks the price status
// as unknown rather than inventing fallback rates.
func (s *SessionStore) EstimateMediaCostWithOptions(opts MediaCostEstimateOptions) MediaCostEstimate {
	if opts.Count <= 0 {
		opts.Count = 1
	}
	provider := strings.ToLower(strings.TrimSpace(opts.Provider))
	model := strings.TrimSpace(opts.Model)

	if provider == "codex" {
		return MediaCostEstimate{
			CostUSD:        0.0,
			PriceStatus:    "subscription",
			PricingSummary: "Codex subscription ($0.00 billed)",
		}
	}

	if s == nil || s.store == nil {
		return MediaCostEstimate{
			CostUSD:        0.0,
			PriceStatus:    "unknown",
			PricingSummary: "unknown pricing (store not configured)",
		}
	}

	catalogStore := NewModelCatalogStore(s.store)
	rec, found, err := catalogStore.GetRecord(provider, model)
	if err != nil || !found {
		cleanModel := strings.TrimPrefix(strings.TrimPrefix(model, provider+"/"), "google/")
		if cleanRec, cleanFound, cleanErr := catalogStore.GetRecord(provider, cleanModel); cleanErr == nil && cleanFound {
			rec = cleanRec
			found = true
		}
	}

	if !found {
		snapID := rec.SourceSnapshotID
		snapVer := rec.SourceSnapshotVersion
		summary := fmt.Sprintf("unknown pricing (model %q unpriced in snapshot)", model)
		if snapID != "" {
			summary = fmt.Sprintf("unknown pricing (model %q unpriced in snapshot %s)", model, snapID)
		}
		return MediaCostEstimate{
			CostUSD:         0.0,
			PriceStatus:     "unknown",
			PricingSummary:  summary,
			SnapshotID:      snapID,
			SnapshotVersion: snapVer,
		}
	}

	return EstimateMediaCostFromRecord(rec, opts)
}

// EstimateMediaCostFromRecord resolves snapshot-backed media pricing for a given catalog record.
// If the model is unpriced or contains unverified status, it explicitly marks the price status
// as unknown rather than inventing fallback rates.
func EstimateMediaCostFromRecord(rec ModelCatalogRecord, opts MediaCostEstimateOptions) MediaCostEstimate {
	if opts.Count <= 0 {
		opts.Count = 1
	}
	provider := strings.ToLower(strings.TrimSpace(opts.Provider))
	if provider == "" {
		provider = strings.ToLower(strings.TrimSpace(rec.Provider))
	}
	model := strings.TrimSpace(opts.Model)
	if model == "" {
		model = strings.TrimSpace(rec.Model)
	}
	kind := strings.ToLower(strings.TrimSpace(opts.Kind))

	if provider == "codex" {
		return MediaCostEstimate{
			CostUSD:        0.0,
			PriceStatus:    "subscription",
			PricingSummary: "Codex subscription ($0.00 billed)",
		}
	}

	snapID := rec.SourceSnapshotID
	snapVer := rec.SourceSnapshotVersion

	if len(rec.Pricing) == 0 {
		summary := fmt.Sprintf("unknown pricing (model %q unpriced in snapshot)", model)
		if snapID != "" {
			summary = fmt.Sprintf("unknown pricing (model %q unpriced in snapshot %s)", model, snapID)
		}
		return MediaCostEstimate{
			CostUSD:         0.0,
			PriceStatus:     "unknown",
			PricingSummary:  summary,
			SnapshotID:      snapID,
			SnapshotVersion: snapVer,
		}
	}

	var raw map[string]any
	if err := json.Unmarshal(rec.Pricing, &raw); err != nil {
		summary := "unknown pricing (invalid pricing in snapshot)"
		if snapID != "" {
			summary = fmt.Sprintf("unknown pricing (invalid pricing in snapshot %s)", snapID)
		}
		return MediaCostEstimate{
			CostUSD:         0.0,
			PriceStatus:     "unknown",
			PricingSummary:  summary,
			SnapshotID:      snapID,
			SnapshotVersion: snapVer,
		}
	}

	// Strict currency check
	if curr, ok := raw["currency"].(string); ok && curr != "" && curr != "USD" {
		return MediaCostEstimate{
			CostUSD:         0.0,
			PriceStatus:     "unknown",
			PricingSummary:  fmt.Sprintf("unknown pricing (unsupported currency %q)", curr),
			SnapshotID:      snapID,
			SnapshotVersion: snapVer,
		}
	}

	if isFree, ok := raw["is_free"].(bool); ok && isFree {
		summary := "Free"
		if snapID != "" {
			summary = fmt.Sprintf("Free (snapshot %s)", snapID)
		}
		return MediaCostEstimate{
			CostUSD:         0.0,
			PriceStatus:     "free",
			PricingSummary:  summary,
			SnapshotID:      snapID,
			SnapshotVersion: snapVer,
		}
	}

	// Extract verified billing lines
	var verifiedLines []map[string]any
	if billing, ok := raw["billing"].(map[string]any); ok {
		status, _ := billing["status"].(string)
		if strings.EqualFold(status, "verified") || strings.EqualFold(status, "partially_verified") {
			if lines, ok := billing["lines"].([]any); ok {
				for _, line := range lines {
					if lMap, ok := line.(map[string]any); ok {
						verifiedLines = append(verifiedLines, lMap)
					}
				}
			}
		} else {
			// Explicit unverified status: MUST stay unknown, no fallback!
			summary := fmt.Sprintf("unknown pricing (catalog pricing status is %q)", status)
			if snapID != "" {
				summary = fmt.Sprintf("unknown pricing (catalog pricing status is %q for %s)", status, snapID)
			}
			return MediaCostEstimate{
				CostUSD:         0.0,
				PriceStatus:     "unknown",
				PricingSummary:  summary,
				SnapshotID:      snapID,
				SnapshotVersion: snapVer,
			}
		}
	}

	unitPrice := 0.0
	foundPrice := false
	summaryText := ""

	switch kind {
	case "video":
		// Check verified billing lines for video
		for _, lineMap := range verifiedLines {
			billable, _ := lineMap["billable"].(string)
			if billable != "video_output" && billable != "video" {
				continue
			}
			pUSD, ok := toFloat64(lineMap["price_usd"])
			if !ok || pUSD < 0 {
				continue
			}
			// Condition matching: resolution, includes_audio, service_tier
			serviceTier := ""
			if conds, ok := lineMap["conditions"].(map[string]any); ok {
				if res, ok := conds["resolution"].(string); ok && res != "" {
					if opts.Resolution == "" || !strings.EqualFold(res, opts.Resolution) {
						continue
					}
				}
				if incAudio, ok := conds["includes_audio"].(bool); ok {
					if incAudio != opts.IncludesAudio {
						continue
					}
				}
				if st, ok := conds["service_tier"].(string); ok && st != "" {
					serviceTier = st
				}
			}
			if serviceTier == "" {
				if st, ok := lineMap["service_tier"].(string); ok && st != "" {
					serviceTier = st
				}
			}
			if serviceTier != "" {
				reqTier := opts.ServiceTier
				if reqTier == "" {
					reqTier = "standard"
				}
				if !strings.EqualFold(serviceTier, reqTier) {
					continue
				}
			}
			catalogTag := "(catalog)"
			if snapID != "" {
				catalogTag = fmt.Sprintf("(catalog %s)", snapID)
			}
			unit, _ := lineMap["unit"].(string)
			switch strings.ToLower(unit) {
			case "second", "sec":
				if opts.DurationSeconds > 0 {
					unitPrice = pUSD * float64(opts.DurationSeconds)
					foundPrice = true
					summaryText = fmt.Sprintf("$%.3f/sec ($%.2f for %ds) %s", pUSD, unitPrice, opts.DurationSeconds, catalogTag)
				}
			case "minute", "min":
				if opts.DurationSeconds > 0 {
					unitPrice = (pUSD / 60.0) * float64(opts.DurationSeconds)
					foundPrice = true
					summaryText = fmt.Sprintf("$%.2f/min ($%.2f for %ds) %s", pUSD, unitPrice, opts.DurationSeconds, catalogTag)
				}
			case "video", "generation":
				unitPrice = pUSD
				foundPrice = true
				summaryText = fmt.Sprintf("$%.2f per video %s", unitPrice, catalogTag)
			}
			if foundPrice {
				break
			}
		}
		if !foundPrice {
			catalogTag := "(catalog)"
			if snapID != "" {
				catalogTag = fmt.Sprintf("(catalog %s)", snapID)
			}
			if vo, ok := toFloat64(raw["video_output"]); ok && vo > 0 {
				unitPrice = vo
				foundPrice = true
				summaryText = fmt.Sprintf("$%.2f per generation %s", vo, catalogTag)
			} else if pv, ok := toFloat64(raw["per_video"]); ok && pv > 0 {
				unitPrice = pv
				foundPrice = true
				summaryText = fmt.Sprintf("$%.2f per video %s", pv, catalogTag)
			}
		}

	case "audio":
		lowerModel := strings.ToLower(model)
		for _, lineMap := range verifiedLines {
			billable, _ := lineMap["billable"].(string)
			if billable != "song" && billable != "clip" && billable != "audio_output" && billable != "music_output" && billable != "audio" {
				continue
			}
			pUSD, ok := toFloat64(lineMap["price_usd"])
			if !ok || pUSD < 0 {
				continue
			}
			variant, _ := lineMap["variant"].(string)
			if strings.Contains(lowerModel, "clip") && variant != "" && !strings.Contains(strings.ToLower(variant), "clip") {
				continue
			}
			if (strings.Contains(lowerModel, "song") || strings.Contains(lowerModel, "3.5")) && variant != "" && strings.Contains(strings.ToLower(variant), "clip") {
				continue
			}
			unit, _ := lineMap["unit"].(string)
			switch strings.ToLower(unit) {
			case "song", "clip", "audio", "generation":
				unitPrice = pUSD
				foundPrice = true
				summaryText = fmt.Sprintf("$%.2f per %s (catalog %s)", unitPrice, unit, snapID)
			case "second", "sec":
				if opts.DurationSeconds > 0 {
					unitPrice = pUSD * float64(opts.DurationSeconds)
					foundPrice = true
					summaryText = fmt.Sprintf("$%.3f/sec ($%.2f for %ds) (catalog %s)", pUSD, unitPrice, opts.DurationSeconds, snapID)
				}
			}
			if foundPrice {
				break
			}
		}
		if !foundPrice {
			if ao, ok := toFloat64(raw["audio_output"]); ok && ao > 0 {
				unitPrice = ao
				foundPrice = true
				summaryText = fmt.Sprintf("$%.2f per generation (catalog %s)", ao, snapID)
			} else if mo, ok := toFloat64(raw["music_output"]); ok && mo > 0 {
				unitPrice = mo
				foundPrice = true
				summaryText = fmt.Sprintf("$%.2f per generation (catalog %s)", mo, snapID)
			} else if aop, ok := raw["audio_output_price"].(map[string]any); ok {
				if amt, ok := toFloat64(aop["amount"]); ok && amt > 0 {
					unitPrice = amt
					foundPrice = true
					summaryText = fmt.Sprintf("$%.2f per audio (catalog %s)", amt, snapID)
				}
			}
		}

	case "image":
		for _, lineMap := range verifiedLines {
			billable, _ := lineMap["billable"].(string)
			if billable != "image_output" && billable != "image" && billable != "image_generation" {
				continue
			}
			pUSD, ok := toFloat64(lineMap["price_usd"])
			if !ok || pUSD < 0 {
				continue
			}
			if conds, ok := lineMap["conditions"].(map[string]any); ok {
				if res, ok := conds["resolution"].(string); ok && res != "" {
					if !strings.EqualFold(res, opts.Resolution) && !strings.EqualFold(res, opts.ImageSize) {
						continue
					}
				}
				if imgSize, ok := conds["image_size"].(string); ok && imgSize != "" {
					if !strings.EqualFold(imgSize, opts.ImageSize) && !strings.EqualFold(imgSize, opts.Resolution) {
						continue
					}
				}
				if ar, ok := conds["aspect_ratio"].(string); ok && ar != "" {
					if opts.AspectRatio == "" || !strings.EqualFold(ar, opts.AspectRatio) {
						continue
					}
				}
			}
			unit, _ := lineMap["unit"].(string)
			switch strings.ToLower(unit) {
			case "token":
				if opts.OutputTokens > 0 {
					unitPrice = pUSD * float64(opts.OutputTokens)
					foundPrice = true
					summaryText = fmt.Sprintf("$%.6f/token (%d tokens: $%.4f) (catalog %s)", pUSD, opts.OutputTokens, unitPrice, snapID)
				}
			case "image", "generation":
				unitPrice = pUSD
				foundPrice = true
				summaryText = fmt.Sprintf("$%.4f per image (catalog %s)", unitPrice, snapID)
			}
			if foundPrice {
				break
			}
		}
	}

	if !foundPrice {
		return MediaCostEstimate{
			CostUSD:         0.0,
			PriceStatus:     "unknown",
			PricingSummary:  fmt.Sprintf("unknown pricing (no verified %s rate for %q in snapshot %s)", kind, model, snapID),
			SnapshotID:      snapID,
			SnapshotVersion: snapVer,
		}
	}

	totalCost := unitPrice * float64(opts.Count)
	return MediaCostEstimate{
		CostUSD:         totalCost,
		PriceStatus:     "known",
		PricingSummary:  summaryText,
		SnapshotID:      snapID,
		SnapshotVersion: snapVer,
	}
}

// EstimateMediaCost resolves snapshot-backed media pricing for images, videos, and audio.
func (s *SessionStore) EstimateMediaCost(provider, model, kind string, count int, durationSeconds int, isIteration bool) MediaCostEstimate {
	return s.EstimateMediaCostWithOptions(MediaCostEstimateOptions{
		Provider:        provider,
		Model:           model,
		Kind:            kind,
		Count:           count,
		DurationSeconds: durationSeconds,
		IsIteration:     isIteration,
	})
}

// GetUsageLimit retrieves the configured daily usage limit for an account.
func (s *SessionStore) GetUsageLimit(accountScopeID string) (UsageLimitRecord, bool, error) {
	if s == nil || s.store == nil {
		return UsageLimitRecord{}, false, errors.New("store is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	key := KeyUsageLimit(accountScopeID)
	var record UsageLimitRecord
	ok, err := s.store.GetJSON(key, &record)
	if err != nil {
		return UsageLimitRecord{}, false, err
	}
	if !ok {
		return UsageLimitRecord{}, false, nil
	}
	return record, true, nil
}

// PutUsageLimit writes the daily usage limit for an account.
func (s *SessionStore) PutUsageLimit(record UsageLimitRecord) error {
	if s == nil || s.store == nil {
		return errors.New("store is not configured")
	}
	record.AccountScopeID = strings.TrimSpace(record.AccountScopeID)
	if record.UpdatedAt <= 0 {
		record.UpdatedAt = time.Now().UnixMilli()
	}
	key := KeyUsageLimit(record.AccountScopeID)
	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal usage limit %q: %w", record.AccountScopeID, err)
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(key), payload, nil); err != nil {
		return err
	}
	return batch.Commit(pebble.Sync)
}

// GetDailyUsageAccumulator retrieves the cached daily spending and token count for a date.
func (s *SessionStore) GetDailyUsageAccumulator(accountScopeID, date string) (DailyUsageAccumulator, bool, error) {
	if s == nil || s.store == nil {
		return DailyUsageAccumulator{}, false, errors.New("store is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	date = strings.TrimSpace(date)
	key := KeyDailyUsageAccumulator(accountScopeID, date)
	var acc DailyUsageAccumulator
	ok, err := s.store.GetJSON(key, &acc)
	if err != nil {
		return DailyUsageAccumulator{}, false, err
	}
	if !ok {
		return DailyUsageAccumulator{}, false, nil
	}
	return acc, true, nil
}

// PutDailyUsageAccumulator writes the daily accumulator record.
func (s *SessionStore) PutDailyUsageAccumulator(acc DailyUsageAccumulator) error {
	if s == nil || s.store == nil {
		return errors.New("store is not configured")
	}
	acc.AccountScopeID = strings.TrimSpace(acc.AccountScopeID)
	acc.Date = strings.TrimSpace(acc.Date)
	if acc.UpdatedAt <= 0 {
		acc.UpdatedAt = time.Now().UnixMilli()
	}
	key := KeyDailyUsageAccumulator(acc.AccountScopeID, acc.Date)
	payload, err := json.Marshal(acc)
	if err != nil {
		return fmt.Errorf("marshal daily usage accumulator %q/%q: %w", acc.AccountScopeID, acc.Date, err)
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(key), payload, nil); err != nil {
		return err
	}
	return batch.Commit(pebble.Sync)
}

// IncrementDailyUsage atomically updates the day's total cost and token consumption.
func (s *SessionStore) IncrementDailyUsage(accountScopeID, date string, costUSD float64, tokens int64) (DailyUsageAccumulator, error) {
	if s == nil || s.store == nil {
		return DailyUsageAccumulator{}, errors.New("store is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return DailyUsageAccumulator{}, errors.New("account_scope_id is required")
	}
	unlock := s.store.sessionMutations.lockSessions("account:" + accountScopeID)
	defer unlock()

	date = strings.TrimSpace(date)
	if date == "" {
		date = time.Now().UTC().Format("2006-01-02")
	}

	acc, found, err := s.GetDailyUsageAccumulator(accountScopeID, date)
	if err != nil {
		return DailyUsageAccumulator{}, err
	}
	if !found {
		acc = DailyUsageAccumulator{
			AccountScopeID: accountScopeID,
			Date:           date,
		}
	}
	acc.TotalCostUSD += costUSD
	acc.TotalTokens += tokens
	acc.TurnCount++
	acc.UpdatedAt = time.Now().UnixMilli()

	if err := s.PutDailyUsageAccumulator(acc); err != nil {
		return DailyUsageAccumulator{}, err
	}
	return acc, nil
}

// IncrementDailyMediaUsage atomically updates the day's media spending and media call count.
func (s *SessionStore) IncrementDailyMediaUsage(accountScopeID, date string, costUSD float64) (DailyUsageAccumulator, error) {
	if s == nil || s.store == nil {
		return DailyUsageAccumulator{}, errors.New("store is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return DailyUsageAccumulator{}, errors.New("account_scope_id is required")
	}
	unlock := s.store.sessionMutations.lockSessions("account:" + accountScopeID)
	defer unlock()

	date = strings.TrimSpace(date)
	if date == "" {
		date = time.Now().UTC().Format("2006-01-02")
	}
	acc, found, err := s.GetDailyUsageAccumulator(accountScopeID, date)
	if err != nil {
		return DailyUsageAccumulator{}, err
	}
	if !found {
		acc = DailyUsageAccumulator{
			AccountScopeID: accountScopeID,
			Date:           date,
		}
	}
	acc.TotalCostUSD += costUSD
	acc.MediaCostUSD += costUSD
	acc.MediaCalls++
	acc.UpdatedAt = time.Now().UnixMilli()
	if err := s.PutDailyUsageAccumulator(acc); err != nil {
		return DailyUsageAccumulator{}, err
	}
	return acc, nil
}

// ListDailyUsageAccumulators returns all daily usage accumulators for an account in chronological order.
func (s *SessionStore) ListDailyUsageAccumulators(accountScopeID string) ([]DailyUsageAccumulator, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("store is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return nil, errors.New("account_scope_id is required")
	}
	prefix := DailyUsageAccumulatorPrefix(accountScopeID)
	out := make([]DailyUsageAccumulator, 0, 32)
	const iterateAll = int(^uint(0) >> 1)
	err := s.store.IteratePrefix(prefix, iterateAll, func(_ string, value []byte) error {
		var acc DailyUsageAccumulator
		if err := json.Unmarshal(value, &acc); err != nil {
			return err
		}
		if acc.Date == "" {
			return nil
		}
		out = append(out, acc)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Date < out[j].Date
	})
	return out, nil
}

// AccountUsageRollup persists aggregated telemetry keyed by account/day/session/provider/model on write.
type AccountUsageRollup struct {
	AccountScopeID      string  `json:"account_scope_id"`
	Date                string  `json:"date"` // Format: YYYY-MM-DD (UTC)
	SessionID           string  `json:"session_id"`
	Provider            string  `json:"provider"`
	Model               string  `json:"model"`
	TotalTokens         int64   `json:"total_tokens"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CachedTokens        int64   `json:"cached_tokens"`
	ThinkingTokens      int64   `json:"thinking_tokens"`
	TokenCostUSD        float64 `json:"token_cost_usd"`
	CodexNominalCostUSD float64 `json:"codex_nominal_cost_usd"`
	Turns               int     `json:"turns"`
	MediaCalls          int     `json:"media_calls"`
	MediaCostUSD        float64 `json:"media_cost_usd"`
	ImageCount          int     `json:"image_count"`
	ImageCostUSD        float64 `json:"image_cost_usd"`
	VideoCount          int     `json:"video_count"`
	VideoCostUSD        float64 `json:"video_cost_usd"`
	AudioCount          int     `json:"audio_count"`
	AudioCostUSD        float64 `json:"audio_cost_usd"`
	UnknownCount        int     `json:"unknown_count"`
	LastActiveAt        int64   `json:"last_active_at"`
	UpdatedAt           int64   `json:"updated_at"`
}

func (s *SessionStore) GetAccountUsageRollup(accountScopeID, date, sessionID, provider, model string) (AccountUsageRollup, bool, error) {
	if s == nil || s.store == nil {
		return AccountUsageRollup{}, false, errors.New("store is not configured")
	}
	var rollup AccountUsageRollup
	ok, err := s.store.GetJSON(KeyAccountUsageRollup(accountScopeID, date, sessionID, provider, model), &rollup)
	return rollup, ok, err
}

func (s *SessionStore) ListAccountUsageRollups(accountScopeID string) ([]AccountUsageRollup, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("store is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return nil, errors.New("account_scope_id is required")
	}
	prefix := AccountUsageRollupPrefix(accountScopeID)
	out := make([]AccountUsageRollup, 0, 64)
	const iterateAll = int(^uint(0) >> 1)
	err := s.store.IteratePrefix(prefix, iterateAll, func(_ string, value []byte) error {
		var rollup AccountUsageRollup
		if err := json.Unmarshal(value, &rollup); err != nil {
			return err
		}
		if rollup.SessionID == "" {
			return nil
		}
		out = append(out, rollup)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetTodayUsageTotal returns the total cost in USD and total tokens for today (UTC).
// If the accumulator exists, it returns in O(1); otherwise, it aggregates today's records and caches the result.
func (s *SessionStore) GetTodayUsageTotal(accountScopeID string) (float64, int64, error) {
	if s == nil || s.store == nil {
		return 0, 0, errors.New("store is not configured")
	}
	now := time.Now().UTC()
	todayDate := now.Format("2006-01-02")
	accountScopeID = strings.TrimSpace(accountScopeID)

	acc, found, err := s.GetDailyUsageAccumulator(accountScopeID, todayDate)
	if err == nil && found {
		return acc.TotalCostUSD, acc.TotalTokens, nil
	}

	// Recompute from turns for today if accumulator not yet present
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).UnixMilli()
	turns, err := s.ListAllTurnUsage(accountScopeID, 10000)
	if err != nil {
		return 0, 0, err
	}

	var totalCost float64
	var totalTokens int64
	turnCount := 0
	for _, rec := range turns {
		ts := rec.CreatedAt
		if ts <= 0 {
			ts = rec.UpdatedAt
		}
		if ts < startOfDay {
			continue
		}
		cost := rec.EstimatedCostUSD
		if cost <= 0 {
			cost = CalculateBaselineCost(rec.Provider, rec.Model, rec.InputTokens, rec.OutputTokens, rec.CacheReadTokens, rec.ThinkingTokens)
		}
		totalCost += cost
		totalTokens += rec.TotalTokens
		turnCount++
	}

	mediaRecords, _ := s.ListMediaUsage(accountScopeID, 2000)
	mediaCost := 0.0
	mediaCalls := 0
	for _, m := range mediaRecords {
		ts := m.CreatedAt
		if ts < startOfDay {
			continue
		}
		mediaCost += m.CostUSD
		mediaCalls++
	}
	totalCost += mediaCost

	acc = DailyUsageAccumulator{
		AccountScopeID: accountScopeID,
		Date:           todayDate,
		TotalCostUSD:   totalCost,
		TotalTokens:    totalTokens,
		TurnCount:      turnCount,
		MediaCalls:     mediaCalls,
		MediaCostUSD:   mediaCost,
		UpdatedAt:      now.UnixMilli(),
	}
	_ = s.PutDailyUsageAccumulator(acc)
	return totalCost, totalTokens, nil
}
