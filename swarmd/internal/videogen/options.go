package videogen

import (
	"encoding/json"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// ParsedVideoOptions holds parsed and normalized capability options from a model's catalog record.
type ParsedVideoOptions struct {
	AspectRatios                   []string
	Resolutions                    []string
	Durations                      []int
	DefaultRatio                   string
	DefaultRes                     string
	DefaultDur                     int // 0 if omitted / unknown
	ResolutionDurations            map[string][]int
	InitialImageSupported          bool
	InitialImageMaxInputs          int
	ConversationalEditingSupported bool
	VideoExtensionSupported        bool
	HasVideoOutput                 bool
	Status                         string
}

// IsOmniModel returns true if the model is an Omni model that uses the Interactions API.
func IsOmniModel(modelID string) bool {
	lower := strings.ToLower(modelID)
	return strings.Contains(lower, "omni")
}

// IsVeoModel returns true if the model is a Google Veo model.
func IsVeoModel(modelID string) bool {
	lower := strings.ToLower(modelID)
	return strings.Contains(lower, "veo")
}

// IsVeo31Model returns true if the model is a Veo 3.1 standard or fast model.
func IsVeo31Model(modelID string) bool {
	lower := strings.ToLower(modelID)
	return (strings.Contains(lower, "veo-3.1") || strings.Contains(lower, "veo-3-1") || strings.Contains(lower, "veo_3_1")) && !strings.Contains(lower, "lite")
}

// IsVeoLiteModel returns true if the model is a Veo Lite model.
func IsVeoLiteModel(modelID string) bool {
	lower := strings.ToLower(modelID)
	return strings.Contains(lower, "veo") && strings.Contains(lower, "lite")
}

// IsStableOmniModel returns true if the model is the stable Google Gemini Omni model.
func IsStableOmniModel(modelID string) bool {
	clean := strings.ToLower(strings.TrimSpace(modelID))
	clean = strings.TrimPrefix(clean, "google:")
	clean = strings.TrimPrefix(clean, "google/")
	return clean == "gemini-omni-1.1-flash"
}

// IsEquivalentAspectRatio checks if the requested aspect ratio has a supported equivalent.
func IsEquivalentAspectRatio(supported []string, requested string) bool {
	reqLower := strings.ToLower(strings.TrimSpace(requested))
	for _, s := range supported {
		sLower := strings.ToLower(strings.TrimSpace(s))
		if (reqLower == "landscape" && sLower == "16:9") ||
			(reqLower == "portrait" && sLower == "9:16") ||
			(reqLower == "16:9" && sLower == "landscape") ||
			(reqLower == "9:16" && sLower == "portrait") {
			return true
		}
	}
	return false
}

// NormalizeAspectRatio maps portrait/landscape to numeric aspect ratios.
func NormalizeAspectRatio(aspectRatio string) string {
	switch strings.TrimSpace(aspectRatio) {
	case "9:16", "portrait":
		return "9:16"
	case "16:9", "landscape":
		return "16:9"
	default:
		return strings.TrimSpace(aspectRatio)
	}
}

// NormalizeResolution maps common resolution strings to lowercase canonical form.
func NormalizeResolution(resolution string) string {
	switch strings.ToLower(strings.TrimSpace(resolution)) {
	case "360p":
		return "360p"
	case "720p":
		return "720p"
	case "1080p":
		return "1080p"
	case "4k":
		return "4k"
	default:
		return strings.ToLower(strings.TrimSpace(resolution))
	}
}

// ContainsStringFold checks case-insensitive string containment.
func ContainsStringFold(slice []string, val string) bool {
	for _, s := range slice {
		if strings.EqualFold(strings.TrimSpace(s), strings.TrimSpace(val)) {
			return true
		}
	}
	return false
}

// ExtractVideoOptions extracts snapshot-backed video generation settings and feature options.
// It fails closed: absent metadata leaves fields omitted/empty rather than inventing fallbacks.
func ExtractVideoOptions(rec pebblestore.ModelCatalogRecord) *ParsedVideoOptions {
	hasVideoOutput := ContainsStringFold(rec.CatalogModalities.Outputs, "video") ||
		ContainsStringFold(rec.CatalogModalities.Categories, "video_generation") ||
		ContainsStringFold(rec.CatalogModalities.Categories, "video_iteration")
	if !hasVideoOutput {
		return nil
	}

	opts := &ParsedVideoOptions{
		HasVideoOutput:      true,
		ResolutionDurations: make(map[string][]int),
	}

	if len(rec.ProviderSpecific) == 0 {
		return opts
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.ProviderSpecific, &raw); err != nil {
		return opts
	}

	providerKey := strings.ToLower(strings.TrimSpace(rec.Provider))
	provData, ok := raw[providerKey]
	if !ok || len(provData) == 0 {
		return opts
	}

	type rawSetting struct {
		Status          string `json:"status"`
		DefaultValue    any    `json:"default_value"`
		SupportedValues []any  `json:"supported_values"`
		Variants        []struct {
			Mode            string         `json:"mode"`
			SupportedValues []any          `json:"supported_values"`
			Conditions      map[string]any `json:"conditions"`
		} `json:"variants"`
	}

	type rawFeature struct {
		Status     string         `json:"status"`
		Supported  bool           `json:"supported"`
		MaxInputs  *int           `json:"max_inputs"`
		Conditions map[string]any `json:"conditions"`
	}

	var parsed struct {
		VideoGeneration *struct {
			Status   string                `json:"status"`
			Settings map[string]rawSetting `json:"settings"`
			Features map[string]rawFeature `json:"features"`
		} `json:"video_generation"`
		Settings map[string]rawSetting `json:"settings"`
		Features map[string]rawFeature `json:"features"`
	}
	if err := json.Unmarshal(provData, &parsed); err != nil {
		return opts
	}

	settingsMap := parsed.Settings
	featuresMap := parsed.Features
	if parsed.VideoGeneration != nil {
		if parsed.VideoGeneration.Status != "" {
			opts.Status = parsed.VideoGeneration.Status
		}
		if len(parsed.VideoGeneration.Settings) > 0 {
			settingsMap = parsed.VideoGeneration.Settings
		}
		if len(parsed.VideoGeneration.Features) > 0 {
			featuresMap = parsed.VideoGeneration.Features
		}
	}

	// 1. Aspect Ratio
	if ar, ok := settingsMap["aspect_ratio"]; ok && strings.EqualFold(ar.Status, "verified") {
		for _, v := range ar.SupportedValues {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				opts.AspectRatios = append(opts.AspectRatios, strings.TrimSpace(s))
			}
		}
		if s, ok := ar.DefaultValue.(string); ok {
			opts.DefaultRatio = strings.TrimSpace(s)
		}
	}

	// 2. Resolution
	if res, ok := settingsMap["resolution"]; ok && strings.EqualFold(res.Status, "verified") {
		for _, v := range res.SupportedValues {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				opts.Resolutions = append(opts.Resolutions, strings.TrimSpace(s))
			}
		}
		if s, ok := res.DefaultValue.(string); ok {
			opts.DefaultRes = strings.TrimSpace(s)
		}
	}

	// 3. Duration seconds
	if dur, ok := settingsMap["duration_seconds"]; ok && strings.EqualFold(dur.Status, "verified") {
		for _, v := range dur.SupportedValues {
			switch n := v.(type) {
			case float64:
				opts.Durations = append(opts.Durations, int(n))
			case int:
				opts.Durations = append(opts.Durations, n)
			}
		}
		switch n := dur.DefaultValue.(type) {
		case float64:
			opts.DefaultDur = int(n)
		case int:
			opts.DefaultDur = n
		}
	}

	// 4. ResolutionDurations
	if len(opts.Resolutions) > 0 && len(opts.Durations) > 0 {
		for _, r := range opts.Resolutions {
			rLower := strings.ToLower(r)
			var rDurs []int

			// Check variants in duration_seconds
			if durSetting, ok := settingsMap["duration_seconds"]; ok && len(durSetting.Variants) > 0 {
				for _, v := range durSetting.Variants {
					if matchResolutionCondition(v.Conditions, rLower) {
						for _, val := range v.SupportedValues {
							switch n := val.(type) {
							case float64:
								rDurs = append(rDurs, int(n))
							case int:
								rDurs = append(rDurs, n)
							}
						}
					}
				}
			}

			// Also check variants in resolution
			if len(rDurs) == 0 {
				if resSetting, ok := settingsMap["resolution"]; ok && len(resSetting.Variants) > 0 {
					for _, v := range resSetting.Variants {
						if variantContainsResolution(v.SupportedValues, rLower) {
							if d := extractDurationCondition(v.Conditions); d > 0 {
								rDurs = append(rDurs, d)
							}
						}
					}
				}
			}

			if len(rDurs) > 0 {
				opts.ResolutionDurations[rLower] = rDurs
			} else {
				opts.ResolutionDurations[rLower] = opts.Durations
			}
		}
	}

	// 5. Features - strict metadata-only, no modality inference!
	if feat, ok := featuresMap["initial_image"]; ok && strings.EqualFold(feat.Status, "verified") && feat.Supported {
		opts.InitialImageSupported = true
		if feat.MaxInputs != nil && *feat.MaxInputs > 0 {
			opts.InitialImageMaxInputs = *feat.MaxInputs
		}
	}

	if feat, ok := featuresMap["conversational_editing"]; ok && strings.EqualFold(feat.Status, "verified") && feat.Supported {
		opts.ConversationalEditingSupported = true
	}

	if feat, ok := featuresMap["video_extension"]; ok && strings.EqualFold(feat.Status, "verified") && feat.Supported {
		opts.VideoExtensionSupported = true
	}

	return opts
}

func matchResolutionCondition(conds map[string]any, rLower string) bool {
	if conds == nil {
		return false
	}
	condVal, ok := conds["resolution"]
	if !ok || len(conds) != 1 {
		return false
	}
	switch v := condVal.(type) {
	case string:
		vLower := strings.ToLower(strings.TrimSpace(v))
		if vLower == rLower {
			return true
		}
		if (rLower == "1080p" || rLower == "4k") && vLower == "1080p_or_4k" {
			return true
		}
		for _, part := range strings.FieldsFunc(vLower, func(c rune) bool { return c == ',' || c == '/' || c == ' ' }) {
			if part == rLower {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				sLower := strings.ToLower(strings.TrimSpace(s))
				if sLower == rLower || ((rLower == "1080p" || rLower == "4k") && sLower == "1080p_or_4k") {
					return true
				}
			}
		}
	case []string:
		for _, s := range v {
			sLower := strings.ToLower(strings.TrimSpace(s))
			if sLower == rLower || ((rLower == "1080p" || rLower == "4k") && sLower == "1080p_or_4k") {
				return true
			}
		}
	}
	return false
}

func variantContainsResolution(vals []any, rLower string) bool {
	for _, v := range vals {
		if s, ok := v.(string); ok && strings.EqualFold(strings.TrimSpace(s), rLower) {
			return true
		}
	}
	return false
}

func extractDurationCondition(conds map[string]any) int {
	if conds == nil {
		return 0
	}
	if v, ok := conds["duration_seconds"]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case []any:
			if len(n) > 0 {
				if d, ok := n[0].(float64); ok {
					return int(d)
				}
				if d, ok := n[0].(int); ok {
					return d
				}
			}
		case []int:
			if len(n) > 0 {
				return n[0]
			}
		}
	}
	return 0
}
