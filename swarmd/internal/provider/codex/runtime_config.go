package codex

import "strings"

const (
	ServiceTierFast     = "fast"
	ServiceTierFlex     = "flex"
	ServiceTierPriority = "priority"
	ContextMode1M       = "1m"
)

func NormalizeServiceTier(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ServiceTierPriority:
		return ServiceTierPriority
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
