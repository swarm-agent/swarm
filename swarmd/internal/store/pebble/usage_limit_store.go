package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
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
	AccountScopeID string  `json:"account_scope_id"`
	Date           string  `json:"date"` // Format: YYYY-MM-DD (UTC)
	TotalCostUSD   float64 `json:"total_cost_usd"`
	TotalTokens    int64   `json:"total_tokens"`
	TurnCount      int     `json:"turn_count"`
	UpdatedAt      int64   `json:"updated_at"`
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
	"google:gemini-3.8-flash":       {InputPricePerMillion: 0.15, OutputPricePerMillion: 0.60, CachedInputPricePerMillion: 0.0375, HasCached: true},
	"google:gemini-3.7-flash":       {InputPricePerMillion: 0.75, OutputPricePerMillion: 3.75, CachedInputPricePerMillion: 0.075, HasCached: true},
	"google:gemini-3.6-flash":       {InputPricePerMillion: 0.075, OutputPricePerMillion: 0.30, CachedInputPricePerMillion: 0.01875, HasCached: true},
	"google:gemini-3.5-flash-lite":  {InputPricePerMillion: 0.30, OutputPricePerMillion: 2.50, CachedInputPricePerMillion: 0.03, HasCached: true},
	"google:gemini-3.5-flash":       {InputPricePerMillion: 0.30, OutputPricePerMillion: 2.50, CachedInputPricePerMillion: 0.03, HasCached: true},
	"google:gemini-3.1-pro-preview": {InputPricePerMillion: 2.00, OutputPricePerMillion: 12.0, CachedInputPricePerMillion: 0.20, HasCached: true},
	"google:gemini-2.5-flash":       {InputPricePerMillion: 0.075, OutputPricePerMillion: 0.30, CachedInputPricePerMillion: 0.01875, HasCached: true},
	"google:gemini-2.5-pro":         {InputPricePerMillion: 1.25, OutputPricePerMillion: 5.00, CachedInputPricePerMillion: 0.3125, HasCached: true},
	"google:gemini-1.5-flash":       {InputPricePerMillion: 0.075, OutputPricePerMillion: 0.30, CachedInputPricePerMillion: 0.01875, HasCached: true},
	"google:gemini-1.5-pro":         {InputPricePerMillion: 1.25, OutputPricePerMillion: 5.00, CachedInputPricePerMillion: 0.3125, HasCached: true},
	"fireworks:deepseek-v3p2":       {InputPricePerMillion: 0.27, OutputPricePerMillion: 1.10, CachedInputPricePerMillion: 0.135, HasCached: true},
	"fireworks:deepseek-v4p1-flash": {InputPricePerMillion: 0.22, OutputPricePerMillion: 0.66, CachedInputPricePerMillion: 0.11, HasCached: true},
	"fireworks:glm-5p3-flash":       {InputPricePerMillion: 0.15, OutputPricePerMillion: 0.50, CachedInputPricePerMillion: 0.075, HasCached: true},
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
		// Fallback rates for common model families if not in table
		if strings.Contains(model, "flash") {
			pricing = ModelBaselinePricing{InputPricePerMillion: 0.10, OutputPricePerMillion: 0.40, CachedInputPricePerMillion: 0.025, HasCached: true}
		} else if strings.Contains(model, "haiku") {
			pricing = ModelBaselinePricing{InputPricePerMillion: 0.80, OutputPricePerMillion: 4.0, CachedInputPricePerMillion: 0.08, HasCached: true}
		} else if strings.Contains(model, "pro") || strings.Contains(model, "sonnet") {
			pricing = ModelBaselinePricing{InputPricePerMillion: 2.00, OutputPricePerMillion: 10.0, CachedInputPricePerMillion: 0.50, HasCached: true}
		} else {
			// Conservative generic default ($0.50 / $2.00 per MTok)
			pricing = ModelBaselinePricing{InputPricePerMillion: 0.50, OutputPricePerMillion: 2.00}
		}
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

// CalculateBaselineMediaCost computes estimated cost in USD based on media type, model ID, resolution, and duration.
func CalculateBaselineMediaCost(mediaType, modelID, resolution string, durationSeconds int) float64 {
	mt := strings.ToLower(strings.TrimSpace(mediaType))
	model := strings.ToLower(strings.TrimSpace(modelID))
	res := strings.ToLower(strings.TrimSpace(resolution))

	if strings.HasPrefix(mt, "video/") || strings.Contains(model, "veo") || strings.Contains(model, "omni") {
		if durationSeconds <= 0 {
			durationSeconds = 8
		}
		if strings.Contains(model, "fast") {
			if strings.Contains(res, "4k") {
				return float64(durationSeconds) * 0.30
			}
			if strings.Contains(res, "1080") {
				return float64(durationSeconds) * 0.12
			}
			return float64(durationSeconds) * 0.10
		}
		if strings.Contains(model, "lite") {
			if strings.Contains(res, "1080") {
				return float64(durationSeconds) * 0.08
			}
			return float64(durationSeconds) * 0.05
		}
		if strings.Contains(model, "omni") {
			return 0.80
		}
		// Standard Veo 3.1 default
		if strings.Contains(res, "4k") {
			return float64(durationSeconds) * 0.60
		}
		return float64(durationSeconds) * 0.40
	}

	if strings.HasPrefix(mt, "audio/") || strings.Contains(model, "lyria") {
		if strings.Contains(model, "clip") {
			return 0.04
		}
		return 0.08
	}

	if strings.HasPrefix(mt, "image/") || strings.Contains(model, "banana") || strings.Contains(model, "image") {
		if strings.Contains(model, "pro") {
			if strings.Contains(res, "4k") {
				return 0.24
			}
			return 0.134
		}
		if strings.Contains(model, "lite") {
			return 0.0336
		}
		if strings.Contains(model, "2") || strings.Contains(model, "3.1") {
			if strings.Contains(res, "4k") {
				return 0.151
			}
			if strings.Contains(res, "2k") {
				return 0.101
			}
			if strings.Contains(res, "0.5k") {
				return 0.045
			}
			return 0.067
		}
		if strings.Contains(model, "flash") || strings.Contains(model, "banana") {
			return 0.039
		}
		return 0.04
	}

	return 0.0
}

// ExtractMediaPricingFromCatalog extracts estimated cost from raw catalog pricing JSON.
func ExtractMediaPricingFromCatalog(catalogPricing []byte, mediaType, modelID, resolution string, durationSeconds int) (float64, bool) {
	if len(catalogPricing) == 0 {
		return 0, false
	}
	var p struct {
		Billing struct {
			Lines []struct {
				Kind       string         `json:"kind"`
				Billable   string         `json:"billable"`
				Unit       string         `json:"unit"`
				PriceUSD   float64        `json:"price_usd"`
				Variant    string         `json:"variant"`
				Conditions map[string]any `json:"conditions"`
			} `json:"lines"`
		} `json:"billing"`
		VideoOutput float64 `json:"video_output"`
		MusicOutput float64 `json:"music_output"`
		AudioOutput float64 `json:"audio_output"`
		Prompt      float64 `json:"prompt"`
	}
	if err := json.Unmarshal(catalogPricing, &p); err != nil {
		return 0, false
	}

	mt := strings.ToLower(strings.TrimSpace(mediaType))
	res := strings.ToLower(strings.TrimSpace(resolution))
	model := strings.ToLower(strings.TrimSpace(modelID))

	if strings.HasPrefix(mt, "video/") || mt == "video" {
		if durationSeconds <= 0 {
			durationSeconds = 8
		}
		for _, line := range p.Billing.Lines {
			if strings.EqualFold(line.Billable, "video_output") && strings.EqualFold(line.Unit, "second") {
				if res != "" && strings.Contains(strings.ToLower(line.Variant), res) {
					return line.PriceUSD * float64(durationSeconds), true
				}
			}
		}
		for _, line := range p.Billing.Lines {
			if strings.EqualFold(line.Billable, "video_output") && strings.EqualFold(line.Unit, "second") {
				return line.PriceUSD * float64(durationSeconds), true
			}
		}
		if p.VideoOutput > 0 {
			return p.VideoOutput, true
		}
	}

	if strings.HasPrefix(mt, "audio/") || mt == "audio" {
		for _, line := range p.Billing.Lines {
			if strings.EqualFold(line.Billable, "song") || strings.EqualFold(line.Billable, "audio_output") || strings.EqualFold(line.Billable, "music_output") {
				if strings.EqualFold(line.Unit, "song") || strings.EqualFold(line.Unit, "generation") {
					if strings.Contains(model, "clip") && strings.Contains(strings.ToLower(line.Variant), "clip") {
						return line.PriceUSD, true
					}
					if !strings.Contains(model, "clip") && !strings.Contains(strings.ToLower(line.Variant), "clip") {
						return line.PriceUSD, true
					}
				}
			}
		}
		for _, line := range p.Billing.Lines {
			if (strings.EqualFold(line.Billable, "song") || strings.EqualFold(line.Billable, "audio_output")) && line.PriceUSD > 0 {
				return line.PriceUSD, true
			}
		}
		if p.MusicOutput > 0 {
			return p.MusicOutput, true
		}
		if p.AudioOutput > 0 {
			return p.AudioOutput, true
		}
	}

	if strings.HasPrefix(mt, "image/") || mt == "image" {
		for _, line := range p.Billing.Lines {
			if strings.EqualFold(line.Billable, "image_output") {
				if line.Kind == "equivalent_cost" || strings.EqualFold(line.Unit, "image") {
					if res != "" && strings.Contains(strings.ToLower(line.Variant), res) {
						return line.PriceUSD, true
					}
				}
			}
		}
		for _, line := range p.Billing.Lines {
			if strings.EqualFold(line.Billable, "image_output") {
				if (line.Kind == "equivalent_cost" || strings.EqualFold(line.Unit, "image")) && line.PriceUSD > 0 {
					return line.PriceUSD, true
				}
				if strings.EqualFold(line.Unit, "megapixel") && line.PriceUSD > 0 {
					return line.PriceUSD, true
				}
			}
		}
	}

	return 0, false
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

	// Also aggregate media generation artifacts created today
	media, err := s.ListAllMediaArtifactVariants(accountScopeID, 2000)
	if err == nil {
		for _, v := range media {
			ts := v.CreatedAt
			if ts < startOfDay {
				continue
			}
			cost := v.EstimatedCostUSD
			if cost <= 0 {
				res := ""
				if v.OutputRequirements != nil {
					res = v.OutputRequirements.PresetID
					if res == "" && v.OutputRequirements.Width > 0 {
						if v.OutputRequirements.Width >= 3840 {
							res = "4k"
						} else if v.OutputRequirements.Width >= 1920 {
							res = "1080p"
						} else {
							res = "720p"
						}
					}
				}
				cost = CalculateBaselineMediaCost(v.MediaType, v.ModelID, res, 8)
			}
			totalCost += cost
		}
	}

	acc = DailyUsageAccumulator{
		AccountScopeID: accountScopeID,
		Date:           todayDate,
		TotalCostUSD:   totalCost,
		TotalTokens:    totalTokens,
		TurnCount:      turnCount,
		UpdatedAt:      now.UnixMilli(),
	}
	_ = s.PutDailyUsageAccumulator(acc)
	return totalCost, totalTokens, nil
}
