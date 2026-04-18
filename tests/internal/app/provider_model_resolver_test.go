package app

import (
	"sort"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
)

func TestSearchOnlyProvidersBecomeAuthOnlyInModelResolver(t *testing.T) {
	result := providerModelResolverResult{
		ProviderStatuses: map[string]client.ProviderStatus{
			"exa": {
				ID:        "exa",
				Ready:     true,
				Runnable:  false,
				RunReason: "search-only provider (no model runner)",
			},
			"codex": {
				ID:       "codex",
				Ready:    true,
				Runnable: true,
			},
		},
	}
	providerSet := map[string]struct{}{"exa": {}, "codex": {}}
	providerIDs := make([]string, 0, len(providerSet))
	authOnlyProviderIDs := make([]string, 0, 4)
	for providerID := range providerSet {
		providerIDs = append(providerIDs, providerID)
	}
	sort.Strings(providerIDs)
	for _, providerID := range providerIDs {
		status, ok := result.ProviderStatuses[providerID]
		if ok && !status.Runnable && strings.Contains(strings.ToLower(strings.TrimSpace(status.RunReason)), "no model runner") {
			authOnlyProviderIDs = append(authOnlyProviderIDs, providerID)
			continue
		}
		result.ProviderIDs = append(result.ProviderIDs, providerID)
	}
	result.AuthOnlyProviderIDs = authOnlyProviderIDs

	if len(result.ProviderIDs) != 1 || result.ProviderIDs[0] != "codex" {
		t.Fatalf("ProviderIDs = %v, want [codex]", result.ProviderIDs)
	}
	if len(result.AuthOnlyProviderIDs) != 1 || result.AuthOnlyProviderIDs[0] != "exa" {
		t.Fatalf("AuthOnlyProviderIDs = %v, want [exa]", result.AuthOnlyProviderIDs)
	}
}
