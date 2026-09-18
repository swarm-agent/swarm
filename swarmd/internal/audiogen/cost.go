package audiogen

import (
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// EstimateAudioCost returns the estimated cost in USD and a user-friendly pricing summary.
func EstimateAudioCost(providerID, modelID string, isIteration bool, catalogPricing []byte) (float64, string) {
	if len(catalogPricing) > 0 {
		if cost, ok := pebblestore.ExtractMediaPricingFromCatalog(catalogPricing, "audio", modelID, "", 30); ok && cost > 0 {
			return cost, fmt.Sprintf("$%.2f per generation (%s catalog)", cost, modelID)
		}
	}

	lowerModel := strings.ToLower(modelID)
	if strings.Contains(lowerModel, "clip") {
		return 0.04, "$0.04 per clip generation (Google Lyria Clip)"
	}
	if strings.Contains(lowerModel, "3.5") || strings.Contains(lowerModel, "song") {
		return 0.08, "$0.08 per song generation (Google Lyria 3.5)"
	}
	if isIteration {
		return 0.04, "$0.04 per audio iteration (Google Lyria)"
	}
	return 0.04, "$0.04 per audio generation (estimated)"
}
