package audiogen

import (
	"encoding/json"
	"fmt"
	"strings"
)

// EstimateAudioCost returns the estimated cost in USD and a user-friendly pricing summary.
func EstimateAudioCost(providerID, modelID string, isIteration bool, catalogPricing []byte) (float64, string) {
	if len(catalogPricing) > 0 {
		var p struct {
			AudioOutput float64 `json:"audio_output"`
			MusicOutput float64 `json:"music_output"`
			Prompt      float64 `json:"prompt"`
		}
		if err := json.Unmarshal(catalogPricing, &p); err == nil {
			if p.AudioOutput > 0 {
				return p.AudioOutput, fmt.Sprintf("$%.2f per generation (catalog)", p.AudioOutput)
			}
			if p.MusicOutput > 0 {
				return p.MusicOutput, fmt.Sprintf("$%.2f per generation (catalog)", p.MusicOutput)
			}
			if p.Prompt > 0 {
				return p.Prompt, fmt.Sprintf("$%.2f per generation (catalog)", p.Prompt)
			}
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
