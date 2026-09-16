package audiogen

import (
	"fmt"
	"strings"
)

// RouteModel resolves the target audio model and provider based on duration and optional model override.
func RouteModel(durationSeconds int, requestedModel string) (string, string) {
	req := strings.TrimSpace(requestedModel)
	if req != "" {
		clean := strings.TrimPrefix(req, "google/")
		if strings.Contains(clean, "/") {
			return clean, "openrouter"
		}
		lower := strings.ToLower(clean)
		if strings.Contains(lower, "clip") {
			return ModelLyriaClip, ProviderGoogleGemini
		}
		if strings.Contains(lower, "3.5") || strings.Contains(lower, "song") {
			return ModelLyriaSong, ProviderGoogleGemini
		}
		return clean, ProviderGoogleGemini
	}

	if durationSeconds > 0 && durationSeconds <= ClipDurationLimitSeconds {
		return ModelLyriaClip, ProviderGoogleGemini
	}
	if durationSeconds > ClipDurationLimitSeconds {
		return ModelLyriaSong, ProviderGoogleGemini
	}
	return DefaultAudioClipModel, ProviderGoogleGemini
}

// NormalizeDuration normalizes the target duration in seconds for the selected model.
func NormalizeDuration(durationSeconds int, modelID string) int {
	isClip := strings.Contains(strings.ToLower(modelID), "clip")
	if isClip {
		if durationSeconds <= 0 {
			return DefaultClipDurationSeconds
		}
		if durationSeconds > ClipDurationLimitSeconds {
			return ClipDurationLimitSeconds
		}
		return durationSeconds
	}

	// Full song model (lyria-3.5)
	if durationSeconds <= 0 {
		return DefaultSongDurationSeconds
	}
	if durationSeconds > 300 {
		return 300 // Bound song duration to 5 minutes
	}
	return durationSeconds
}

// ShapePromptWithDuration shapes the arrangement prompt with timestamp cues to guide model cadence.
func ShapePromptWithDuration(prompt string, targetSeconds int) string {
	p := strings.TrimSpace(prompt)
	if targetSeconds <= 0 {
		return p
	}
	if hasTimestampCues(p) {
		return p
	}

	mins := targetSeconds / 60
	secs := targetSeconds % 60
	endTime := fmt.Sprintf("%02d:%02d", mins, secs)

	if targetSeconds <= ClipDurationLimitSeconds {
		return fmt.Sprintf("%s\n\n[Arrangement: 00:00 start, dynamic development, concluding and resolving cleanly by %s]", p, endTime)
	}
	return fmt.Sprintf("%s\n\n[Arrangement: 00:00 intro, dynamic verses and chorus, concluding and resolving cleanly by %s]", p, endTime)
}

func hasTimestampCues(s string) bool {
	// Look for timestamp markers like [00: or [0: or [1:
	return strings.Contains(s, "[00:") || strings.Contains(s, "[0:") || strings.Contains(s, "[1:") || strings.Contains(s, "[2:")
}
