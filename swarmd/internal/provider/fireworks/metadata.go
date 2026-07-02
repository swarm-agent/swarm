package fireworks

import (
	"encoding/json"
	"strings"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	ServiceTierStandard = "standard"
	ServiceTierPriority = "priority"
	ServiceTierFast     = "fast"
)

type ServingTier struct {
	Tier              string
	ProviderParameter string
	ProviderValue     string
	Pricing           ServingTierPricing
}

type ServingTierPricing struct {
	UncachedInputPerMillion float64
	CachedInputPerMillion   float64
	OutputPerMillion        float64
}

type ServingConfig struct {
	SupportedTiers []string
	DefaultTier    string
	Tiers          map[string]ServingTier
}

type requestServingResolution struct {
	ModelID         string
	ServiceTier     string
	RequestedTier   string
	EffectiveTier   string
	ServingTier     ServingTier
	SessionAffinity string
}

type rawProviderSpecific map[string]struct {
	Serving rawServing `json:"serving"`
}

type rawServing struct {
	SupportedTiers []string           `json:"supported_tiers"`
	DefaultTier    string             `json:"default_tier"`
	Standard       *rawTier           `json:"standard"`
	Priority       *rawTier           `json:"priority"`
	Fast           *rawTier           `json:"fast"`
	Tiers          map[string]rawTier `json:"tiers"`
}

type rawTier struct {
	Tier              string      `json:"tier"`
	ProviderParameter string      `json:"provider_parameter"`
	ProviderValue     string      `json:"provider_value"`
	Pricing           *rawPricing `json:"pricing"`
}

type rawPricing struct {
	UncachedInputPerMillion float64 `json:"uncached_input_price_per_million_tokens"`
	CachedInputPerMillion   float64 `json:"cached_input_price_per_million_tokens"`
	OutputPerMillion        float64 `json:"output_price_per_million_tokens"`
}

func NormalizeServiceTier(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ServiceTierPriority:
		return ServiceTierPriority
	case ServiceTierFast:
		return ServiceTierFast
	default:
		return ""
	}
}

func ServingConfigFromCatalog(record any) ServingConfig {
	catalog, ok := record.(pebblestore.ModelCatalogRecord)
	if !ok {
		return ServingConfig{}
	}
	var decoded rawProviderSpecific
	if len(catalog.ProviderSpecific) == 0 || json.Unmarshal(catalog.ProviderSpecific, &decoded) != nil {
		return ServingConfig{}
	}
	provider, ok := decoded["fireworks"]
	if !ok {
		return ServingConfig{}
	}
	return servingConfigFromRaw(provider.Serving)
}

func servingConfigFromRaw(raw rawServing) ServingConfig {
	out := ServingConfig{
		SupportedTiers: normalizeServingTiers(raw.SupportedTiers),
		DefaultTier:    strings.ToLower(strings.TrimSpace(raw.DefaultTier)),
		Tiers:          make(map[string]ServingTier),
	}
	if raw.Standard != nil {
		out.add(raw.Standard)
	}
	if raw.Priority != nil {
		out.add(raw.Priority)
	}
	if raw.Fast != nil {
		out.add(raw.Fast)
	}
	for key, tier := range raw.Tiers {
		copy := tier
		if strings.TrimSpace(copy.Tier) == "" {
			copy.Tier = key
		}
		out.add(&copy)
	}
	if out.DefaultTier == "" {
		out.DefaultTier = ServiceTierStandard
	}
	return out
}

func (c *ServingConfig) add(raw *rawTier) {
	if c == nil || raw == nil {
		return
	}
	tierID := strings.ToLower(strings.TrimSpace(raw.Tier))
	if tierID == "" {
		return
	}
	entry := ServingTier{
		Tier:              tierID,
		ProviderParameter: strings.ToLower(strings.TrimSpace(raw.ProviderParameter)),
		ProviderValue:     strings.TrimSpace(raw.ProviderValue),
	}
	if raw.Pricing != nil {
		entry.Pricing = ServingTierPricing{
			UncachedInputPerMillion: raw.Pricing.UncachedInputPerMillion,
			CachedInputPerMillion:   raw.Pricing.CachedInputPerMillion,
			OutputPerMillion:        raw.Pricing.OutputPerMillion,
		}
	}
	c.Tiers[tierID] = entry
}

func normalizeServingTiers(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func ResolveServingTier(req provideriface.Request, cfg ServingConfig) requestServingResolution {
	modelID := strings.TrimSpace(req.Model)
	requestedTier := NormalizeServiceTier(req.ServiceTier)
	resolution := requestServingResolution{
		ModelID:         modelID,
		RequestedTier:   requestedTier,
		EffectiveTier:   ServiceTierStandard,
		SessionAffinity: stableSessionAffinity(req.SessionID),
	}
	if cfg.DefaultTier != "" {
		resolution.EffectiveTier = cfg.DefaultTier
	}
	if requestedTier != "" && servingTierSupported(cfg, requestedTier) {
		resolution.EffectiveTier = requestedTier
	}
	if tier, ok := cfg.Tiers[resolution.EffectiveTier]; ok {
		resolution.ServingTier = tier
		switch tier.ProviderParameter {
		case "service_tier":
			resolution.ServiceTier = strings.TrimSpace(tier.ProviderValue)
		case "model":
			if strings.TrimSpace(tier.ProviderValue) != "" {
				resolution.ModelID = strings.TrimSpace(tier.ProviderValue)
			}
		}
	}
	return resolution
}

func servingTierSupported(cfg ServingConfig, tier string) bool {
	tier = strings.ToLower(strings.TrimSpace(tier))
	if tier == "" {
		return false
	}
	if len(cfg.SupportedTiers) == 0 {
		_, ok := cfg.Tiers[tier]
		return ok
	}
	for _, supported := range cfg.SupportedTiers {
		if strings.EqualFold(supported, tier) {
			return true
		}
	}
	return false
}

func stableSessionAffinity(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	return "swarm-session-" + sessionID
}

func EstimateCostUSD(usage provideriface.TokenUsage, tier ServingTier) float64 {
	uncachedInput := usage.InputTokens - usage.CacheReadTokens
	if uncachedInput < 0 {
		uncachedInput = 0
	}
	cachedInput := usage.CacheReadTokens
	if cachedInput < 0 {
		cachedInput = 0
	}
	output := usage.OutputTokens
	if output < 0 {
		output = 0
	}
	return (float64(uncachedInput)*tier.Pricing.UncachedInputPerMillion + float64(cachedInput)*tier.Pricing.CachedInputPerMillion + float64(output)*tier.Pricing.OutputPerMillion) / 1_000_000
}
