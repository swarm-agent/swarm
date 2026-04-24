package codex

import "strings"

const (
	ServiceTierFast           = "fast"
	ServiceTierFlex           = "flex"
	ContextMode1M             = "1m"
	gpt54DefaultContextWindow = 272_000
	gpt54LargeContextWindow   = 1_050_000
	gpt55ContextWindow        = 400_000
)

func NormalizeServiceTier(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ServiceTierFast:
		return ServiceTierFast
	case ServiceTierFlex:
		return ServiceTierFlex
	default:
		return ""
	}
}

func NormalizeContextMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ContextMode1M:
		return ContextMode1M
	case "", "default", "off":
		return ""
	default:
		return ""
	}
}

func EffectiveContextWindow(modelName, contextMode string, baseContextWindow int) int {
	if strings.EqualFold(strings.TrimSpace(modelName), "gpt-5.4") {
		if NormalizeContextMode(contextMode) == ContextMode1M {
			return gpt54LargeContextWindow
		}
		return gpt54DefaultContextWindow
	}
	if strings.EqualFold(strings.TrimSpace(modelName), "gpt-5.5") {
		return gpt55ContextWindow
	}
	if baseContextWindow < 0 {
		return 0
	}
	return baseContextWindow
}
