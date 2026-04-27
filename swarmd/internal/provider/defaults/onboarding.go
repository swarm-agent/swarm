package defaults

import (
	"fmt"
	"sort"
	"strings"
)

type ProviderDefaults struct {
	ProviderID       string
	PrimaryModel     string
	PrimaryThinking  string
	UtilityModel     string
	UtilityThinking  string
	UtilitySubagents []string
}

var providerDefaultsByProvider = map[string]ProviderDefaults{
	"anthropic": {
		ProviderID:       "anthropic",
		PrimaryModel:     "claude-opus-4-7",
		PrimaryThinking:  "xhigh",
		UtilityModel:     "claude-sonnet-4-6",
		UtilityThinking:  "xhigh",
		UtilitySubagents: []string{"explorer", "memory", "parallel"},
	},
	"codex": {
		ProviderID:       "codex",
		PrimaryModel:     "gpt-5.5",
		PrimaryThinking:  "high",
		UtilityModel:     "gpt-5.4-mini",
		UtilityThinking:  "medium",
		UtilitySubagents: []string{"explorer", "memory", "parallel"},
	},
	"copilot": {
		ProviderID:       "copilot",
		PrimaryModel:     "gpt-5.4",
		PrimaryThinking:  "high",
		UtilityModel:     "gemini-3-flash-preview",
		UtilityThinking:  "high",
		UtilitySubagents: []string{"explorer", "memory", "parallel"},
	},
	"fireworks": {
		ProviderID:       "fireworks",
		PrimaryModel:     "accounts/fireworks/models/kimi-k2p6",
		PrimaryThinking:  "high",
		UtilityModel:     "accounts/fireworks/models/minimax-m2p7",
		UtilityThinking:  "high",
		UtilitySubagents: []string{"explorer", "memory", "parallel"},
	},
	"google": {
		ProviderID:       "google",
		PrimaryModel:     "gemini-3.1-pro-preview",
		PrimaryThinking:  "high",
		UtilityModel:     "gemini-3-flash-preview",
		UtilityThinking:  "high",
		UtilitySubagents: []string{"explorer", "memory", "parallel"},
	},
	"openrouter": {
		ProviderID:       "openrouter",
		PrimaryModel:     "openai/gpt-5.5",
		PrimaryThinking:  "high",
		UtilityModel:     "google/gemini-3-flash-preview",
		UtilityThinking:  "high",
		UtilitySubagents: []string{"explorer", "memory", "parallel"},
	},
}

func Lookup(providerID string) (ProviderDefaults, bool) {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	defaults, ok := providerDefaultsByProvider[providerID]
	if !ok {
		return ProviderDefaults{}, false
	}
	defaults.ProviderID = providerID
	defaults.PrimaryModel = strings.TrimSpace(defaults.PrimaryModel)
	defaults.PrimaryThinking = normalizeThinking(defaults.PrimaryThinking)
	defaults.UtilityModel = strings.TrimSpace(defaults.UtilityModel)
	defaults.UtilityThinking = normalizeThinking(defaults.UtilityThinking)
	defaults.UtilitySubagents = dedupeNames(defaults.UtilitySubagents)
	return defaults, true
}

func MustLookup(providerID string) ProviderDefaults {
	defaults, ok := Lookup(providerID)
	if ok {
		return defaults
	}
	panic(fmt.Sprintf("provider defaults not configured: %q", strings.TrimSpace(providerID)))
}

func SupportedProviders() []string {
	providers := make([]string, 0, len(providerDefaultsByProvider))
	for providerID := range providerDefaultsByProvider {
		providers = append(providers, providerID)
	}
	sort.Strings(providers)
	return providers
}

func normalizeThinking(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "off", "low", "medium", "high", "xhigh":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func dedupeNames(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
