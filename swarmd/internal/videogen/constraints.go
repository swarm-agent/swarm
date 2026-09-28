package videogen

import (
	"errors"
	"fmt"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// VideoOperationConstraints projects operation-specific capabilities, option locks,
// and source compatibility rules for a video model in the catalog.
type VideoOperationConstraints struct {
	Model    string                `json:"model"`
	Provider string                `json:"provider"`
	Create   VideoCreateConstraint `json:"create"`
	Edit     VideoEditConstraint   `json:"edit"`
	Extend   VideoExtendConstraint `json:"extend"`
}

// VideoCreateConstraint defines the supported options and limits for video creation.
type VideoCreateConstraint struct {
	Supported             bool             `json:"supported"`
	Reason                string           `json:"reason,omitempty"`
	AspectRatios          []string         `json:"aspect_ratios,omitempty"`
	Resolutions           []string         `json:"resolutions,omitempty"`
	Durations             []int            `json:"durations,omitempty"`
	DefaultRatio          string           `json:"default_ratio,omitempty"`
	DefaultResolution     string           `json:"default_resolution,omitempty"`
	DefaultDuration       int              `json:"default_duration,omitempty"`
	ResolutionDurations   map[string][]int `json:"resolution_durations,omitempty"`
	InitialImageSupported bool             `json:"initial_image_supported"`
	InitialImageMaxInputs int              `json:"initial_image_max_inputs,omitempty"`
	SupportsDuration      bool             `json:"supports_duration"`
}

// VideoEditConstraint defines the rules and restrictions for conversational video editing.
type VideoEditConstraint struct {
	Supported            bool     `json:"supported"`
	Reason               string   `json:"reason,omitempty"`
	SupportsDuration     bool     `json:"supports_duration"`
	MaxSourceDurationSec float64  `json:"max_source_duration_sec,omitempty"` // 10.0 for external uploads
	RequiresHandleMatch  bool     `json:"requires_handle_match"`             // true for conversational Omni
	SupportedProviders   []string `json:"supported_providers,omitempty"`      // ["google"]
	SourceModelMatch     string   `json:"source_model_match,omitempty"`       // "gemini-omni-1.1-flash"
}

// VideoExtendConstraint defines the locked parameters and source rules for video extension (next scene).
type VideoExtendConstraint struct {
	Supported                      bool     `json:"supported"`
	Reason                         string   `json:"reason,omitempty"`
	LockedDurationSeconds          int      `json:"locked_duration_seconds,omitempty"`          // 8 for Veo, 0 for Omni
	LockedResolution               string   `json:"locked_resolution,omitempty"`                // "720p" for Veo
	LockedAspectRatioMatchesSource bool     `json:"locked_aspect_ratio_matches_source,omitempty"` // true for Veo
	SupportedAspectRatios          []string `json:"supported_aspect_ratios,omitempty"`          // ["16:9", "9:16"] for Veo
	SupportsDuration               bool     `json:"supports_duration"`                          // false for both
	MaxSourceDurationSec           float64  `json:"max_source_duration_sec,omitempty"`          // 141.0 for Veo, 37.0 for Omni
	MaxTotalDurationSec            float64  `json:"max_total_duration_sec,omitempty"`           // 148.0 for Veo, 40.0 for Omni
	MaxExtensionCount              int      `json:"max_extension_count,omitempty"`              // 20 for Veo
	RequiresVeoSource              bool     `json:"requires_veo_source,omitempty"`              // true for Veo
	RequiresOmniSource             bool     `json:"requires_omni_source,omitempty"`             // true for Omni
	DisallowsVeoLiteSource         bool     `json:"disallows_veo_lite_source,omitempty"`          // true for Veo
	SourceObservedResolutions      []string `json:"source_observed_resolutions,omitempty"`       // ["720p", "1280x720", "720x1280"]
	RequiresSourceProvenance       bool     `json:"requires_source_provenance,omitempty"`        // true for both
}

// SourceCompatibilityResult indicates whether a given source video is compatible with a target model and operation.
type SourceCompatibilityResult struct {
	Compatible bool   `json:"compatible"`
	Reason     string `json:"reason,omitempty"`
}

// CheckVideoEditSupport validates whether targetModel on providerID is eligible for video editing.
func CheckVideoEditSupport(providerID, modelID string, opts *ParsedVideoOptions) error {
	trimmedModel := strings.TrimSpace(modelID)
	pID := strings.ToLower(strings.TrimSpace(providerID))
	if IsVeoModel(trimmedModel) {
		return errors.New("Veo models do not support video editing; select an iteration model such as Gemini Omni")
	}
	if !IsOmniModel(trimmedModel) {
		return fmt.Errorf("selected model %q cannot edit a source video; select a video iteration model", trimmedModel)
	}
	if pID != ProviderGoogleGemini {
		return fmt.Errorf("video editing is not supported on provider %q; use Google Gemini Omni", providerID)
	}
	if !IsStableOmniModel(trimmedModel) {
		return fmt.Errorf("video editing is only supported on stable Gemini Omni (%s); %q is not supported", DefaultVideoIterationModel, trimmedModel)
	}
	if opts == nil || !opts.ConversationalEditingSupported {
		return fmt.Errorf("model %q does not support conversational video editing", trimmedModel)
	}
	return nil
}

// CheckVideoExtendSupport validates whether targetModel on providerID is eligible for video extension.
func CheckVideoExtendSupport(providerID, modelID string, opts *ParsedVideoOptions) error {
	trimmedModel := strings.TrimSpace(modelID)
	pID := strings.ToLower(strings.TrimSpace(providerID))
	if IsVeoModel(trimmedModel) {
		if pID != ProviderGoogleGemini {
			return errors.New("Veo native extension is only supported directly via Google Gemini, not OpenRouter")
		}
		if !IsVeo31Model(trimmedModel) || IsVeoLiteModel(trimmedModel) {
			return fmt.Errorf("Veo extension is only supported on Veo 3.1 standard or fast models; %q is not eligible", trimmedModel)
		}
		if opts == nil || !opts.VideoExtensionSupported {
			return fmt.Errorf("model %q does not support video extension in catalog metadata", trimmedModel)
		}
		return nil
	}
	if IsOmniModel(trimmedModel) {
		if !IsStableOmniModel(trimmedModel) {
			return fmt.Errorf("Omni video extension is only supported on stable model %s; %q is not eligible", DefaultVideoIterationModel, trimmedModel)
		}
		if pID != ProviderGoogleGemini {
			return fmt.Errorf("video extension is not supported on provider %q; use Google Gemini Omni", providerID)
		}
		if opts == nil || !opts.VideoExtensionSupported {
			return fmt.Errorf("model %q does not support video extension in catalog metadata", trimmedModel)
		}
		return nil
	}
	return fmt.Errorf("model %q does not support video extension; only Google Veo 3.1 and Gemini Omni support extension", trimmedModel)
}

// SupportsVideoEdit checks if the model supports editing and returns the rejection reason if not.
func SupportsVideoEdit(providerID, modelID string, opts *ParsedVideoOptions) (bool, string) {
	if err := CheckVideoEditSupport(providerID, modelID, opts); err != nil {
		return false, err.Error()
	}
	return true, ""
}

// SupportsVideoExtend checks if the model supports extension and returns the rejection reason if not.
func SupportsVideoExtend(providerID, modelID string, opts *ParsedVideoOptions) (bool, string) {
	if err := CheckVideoExtendSupport(providerID, modelID, opts); err != nil {
		return false, err.Error()
	}
	return true, ""
}

// SupportsVideoIteration returns true if the model supports either video editing or extension.
func SupportsVideoIteration(providerID, modelID string, opts *ParsedVideoOptions) bool {
	canEdit, _ := SupportsVideoEdit(providerID, modelID, opts)
	canExtend, _ := SupportsVideoExtend(providerID, modelID, opts)
	return canEdit || canExtend
}

// BuildVideoOperationConstraints constructs the typed operation-constraint projection for a model catalog record.
func BuildVideoOperationConstraints(providerID, modelID string, record pebblestore.ModelCatalogRecord) *VideoOperationConstraints {
	opts := ExtractVideoOptions(record)
	hasVideoOutput := ContainsStringFold(record.CatalogModalities.Outputs, "video") ||
		ContainsStringFold(record.CatalogModalities.Categories, "video_generation") ||
		ContainsStringFold(record.CatalogModalities.Categories, "video_iteration")

	if !hasVideoOutput {
		return nil
	}

	pID := strings.ToLower(strings.TrimSpace(providerID))
	if pID == "" {
		pID = strings.ToLower(strings.TrimSpace(record.Provider))
	}
	mID := strings.TrimSpace(modelID)
	if mID == "" {
		mID = strings.TrimSpace(record.Model)
	}

	// 1. Create constraint
	createC := VideoCreateConstraint{
		Supported: true,
	}
	if opts != nil {
		createC.AspectRatios = opts.AspectRatios
		createC.Resolutions = opts.Resolutions
		createC.DefaultRatio = opts.DefaultRatio
		createC.DefaultResolution = opts.DefaultRes
		createC.ResolutionDurations = opts.ResolutionDurations
		createC.InitialImageSupported = opts.InitialImageSupported
		createC.InitialImageMaxInputs = opts.InitialImageMaxInputs

		if IsOmniModel(mID) {
			createC.SupportsDuration = false
			createC.Durations = nil
			createC.DefaultDuration = 0
		} else {
			createC.SupportsDuration = len(opts.Durations) > 0
			createC.Durations = opts.Durations
			createC.DefaultDuration = opts.DefaultDur
		}
	}

	// 2. Edit constraint
	canEdit, editReason := SupportsVideoEdit(pID, mID, opts)
	editC := VideoEditConstraint{
		Supported:        canEdit,
		Reason:           editReason,
		SupportsDuration: false,
	}
	if canEdit {
		editC.MaxSourceDurationSec = 10.0
		editC.RequiresHandleMatch = true
		editC.SupportedProviders = []string{ProviderGoogleGemini}
		editC.SourceModelMatch = DefaultVideoIterationModel
	}

	// 3. Extend constraint
	canExtend, extendReason := SupportsVideoExtend(pID, mID, opts)
	extendC := VideoExtendConstraint{
		Supported:                canExtend,
		Reason:                   extendReason,
		SupportsDuration:         false,
		RequiresSourceProvenance: true,
	}
	if canExtend {
		if IsVeoModel(mID) {
			extendC.LockedDurationSeconds = 8
			extendC.LockedResolution = "720p"
			extendC.LockedAspectRatioMatchesSource = true
			extendC.SupportedAspectRatios = []string{"16:9", "9:16"}
			extendC.MaxSourceDurationSec = 141.0
			extendC.MaxTotalDurationSec = 148.0
			extendC.MaxExtensionCount = 20
			extendC.RequiresVeoSource = true
			extendC.DisallowsVeoLiteSource = true
			extendC.SourceObservedResolutions = []string{"720p", "1280x720", "720x1280"}
		} else if IsOmniModel(mID) {
			extendC.LockedDurationSeconds = 0
			extendC.MaxSourceDurationSec = 37.0
			extendC.MaxTotalDurationSec = 40.0
			extendC.RequiresOmniSource = true
		}
	}

	return &VideoOperationConstraints{
		Model:    mID,
		Provider: pID,
		Create:   createC,
		Edit:     editC,
		Extend:   extendC,
	}
}

// CheckSourceCompatibility determines whether a specific source video is compatible with targetModel and operation.
func CheckSourceCompatibility(
	targetProvider, targetModel, op string,
	srcProv *pebblestore.VideoProvenance,
	sourceWidth, sourceHeight int,
	sourceDurationSec float64,
) SourceCompatibilityResult {
	opLower := strings.ToLower(strings.TrimSpace(op))
	pID := strings.ToLower(strings.TrimSpace(targetProvider))
	mID := strings.TrimSpace(targetModel)

	switch opLower {
	case pebblestore.VideoOperationEdit:
		if IsVeoModel(mID) {
			return SourceCompatibilityResult{Compatible: false, Reason: "Veo models do not support video editing; select an iteration model such as Gemini Omni"}
		}
		if !IsOmniModel(mID) {
			return SourceCompatibilityResult{Compatible: false, Reason: fmt.Sprintf("selected model %q cannot edit a source video; select a video iteration model", mID)}
		}
		if pID != ProviderGoogleGemini {
			return SourceCompatibilityResult{Compatible: false, Reason: fmt.Sprintf("video editing is not supported on provider %q; use Google Gemini Omni", targetProvider)}
		}
		if !IsStableOmniModel(mID) {
			return SourceCompatibilityResult{Compatible: false, Reason: fmt.Sprintf("video editing is only supported on stable Gemini Omni (%s); %q is not supported", DefaultVideoIterationModel, mID)}
		}
		if srcProv != nil && strings.TrimSpace(srcProv.InteractionID) != "" {
			if srcProv.Model != "" && !strings.EqualFold(srcProv.Model, mID) {
				return SourceCompatibilityResult{Compatible: false, Reason: fmt.Sprintf("video interaction model mismatch: source was created with %q, cannot continue with %q", srcProv.Model, mID)}
			}
		} else if sourceDurationSec > 10.0 {
			return SourceCompatibilityResult{Compatible: false, Reason: fmt.Sprintf("source video duration (%.1fs) exceeds maximum allowed for external video editing (10s)", sourceDurationSec)}
		}
		return SourceCompatibilityResult{Compatible: true}

	case pebblestore.VideoOperationExtend:
		if IsVeoModel(mID) {
			if pID != ProviderGoogleGemini {
				return SourceCompatibilityResult{Compatible: false, Reason: "Veo native extension is only supported directly via Google Gemini, not OpenRouter"}
			}
			if !IsVeo31Model(mID) || IsVeoLiteModel(mID) {
				return SourceCompatibilityResult{Compatible: false, Reason: fmt.Sprintf("Veo extension is only supported on Veo 3.1 standard or fast models; %q is not eligible", mID)}
			}
			if srcProv == nil {
				return SourceCompatibilityResult{Compatible: false, Reason: "Veo video extension requires trusted source provenance from a previous Veo generation"}
			}
			if srcProv.Provider != ProviderGoogleGemini || !IsVeoModel(srcProv.Model) || IsOmniModel(srcProv.Model) {
				return SourceCompatibilityResult{Compatible: false, Reason: fmt.Sprintf("Veo extension requires a source generated by Google Veo; source was %s/%s", srcProv.Provider, srcProv.Model)}
			}
			if IsVeoLiteModel(srcProv.Model) {
				return SourceCompatibilityResult{Compatible: false, Reason: "Veo extension cannot extend videos generated by Veo Lite"}
			}
			if srcProv.Transport != pebblestore.VideoTransportGooglePredictLongRunning {
				return SourceCompatibilityResult{Compatible: false, Reason: fmt.Sprintf("Veo source requires google_predict_long_running transport; got %q", srcProv.Transport)}
			}
			if strings.TrimSpace(srcProv.ProviderResource) == "" {
				return SourceCompatibilityResult{Compatible: false, Reason: "Veo source requires valid provider resource URI"}
			}
			if strings.TrimSpace(srcProv.OutputDigestSHA256) == "" {
				return SourceCompatibilityResult{Compatible: false, Reason: "Veo source requires non-empty output digest"}
			}
			if srcProv.CreatedAt <= 0 || srcProv.ExpiresAt <= 0 {
				return SourceCompatibilityResult{Compatible: false, Reason: "Veo source provenance missing timestamp metadata"}
			}
			now := time.Now().UnixMilli()
			if now > srcProv.ExpiresAt || time.Since(time.UnixMilli(srcProv.CreatedAt)) > 48*time.Hour {
				return SourceCompatibilityResult{Compatible: false, Reason: "Veo source video reference has expired (exceeds 48-hour validity period)"}
			}
			if !srcProv.ExtensionCountKnown {
				return SourceCompatibilityResult{Compatible: false, Reason: "Veo source extension count must be known"}
			}
			if srcProv.ExtensionCount < 0 || srcProv.ExtensionCount >= 20 {
				return SourceCompatibilityResult{Compatible: false, Reason: "video extension limit reached (20 extensions maximum)"}
			}
			if sourceDurationSec > 141.0 {
				return SourceCompatibilityResult{Compatible: false, Reason: fmt.Sprintf("source video duration (%.1fs) exceeds maximum allowed for Veo extension (141s)", sourceDurationSec)}
			}
			if sourceDurationSec+7.0 > 148.0 {
				return SourceCompatibilityResult{Compatible: false, Reason: fmt.Sprintf("extending source video (%.1fs) would exceed maximum output duration (148s)", sourceDurationSec)}
			}
			isSource16x9 := (sourceWidth == 1280 && sourceHeight == 720) && (srcProv.ObservedWidth == 1280 && srcProv.ObservedHeight == 720)
			isSource9x16 := (sourceWidth == 720 && sourceHeight == 1280) && (srcProv.ObservedWidth == 720 && srcProv.ObservedHeight == 1280)
			if !isSource16x9 && !isSource9x16 {
				return SourceCompatibilityResult{Compatible: false, Reason: fmt.Sprintf("Veo source requires observed 720p dimensions (1280x720 or 720x1280); got source=%dx%d observed=%dx%d", sourceWidth, sourceHeight, srcProv.ObservedWidth, srcProv.ObservedHeight)}
			}
			return SourceCompatibilityResult{Compatible: true}

		} else if IsOmniModel(mID) {
			if !IsStableOmniModel(mID) {
				return SourceCompatibilityResult{Compatible: false, Reason: fmt.Sprintf("Omni video extension is only supported on stable model %s; %q is not eligible", DefaultVideoIterationModel, mID)}
			}
			if pID != ProviderGoogleGemini {
				return SourceCompatibilityResult{Compatible: false, Reason: fmt.Sprintf("video extension is not supported on provider %q; use Google Gemini Omni", targetProvider)}
			}
			if srcProv == nil {
				return SourceCompatibilityResult{Compatible: false, Reason: "Omni video extension requires verified source provenance"}
			}
			if srcProv.Provider != ProviderGoogleGemini || !IsStableOmniModel(srcProv.Model) {
				return SourceCompatibilityResult{Compatible: false, Reason: fmt.Sprintf("Omni extension requires source generated by stable Gemini Omni; got %s/%s", srcProv.Provider, srcProv.Model)}
			}
			if srcProv.Transport != pebblestore.VideoTransportGoogleInteractions {
				return SourceCompatibilityResult{Compatible: false, Reason: fmt.Sprintf("Omni extension requires google_interactions transport; got %q", srcProv.Transport)}
			}
			if strings.TrimSpace(srcProv.InteractionID) == "" {
				return SourceCompatibilityResult{Compatible: false, Reason: "Omni source provenance is missing interaction handle; cannot extend"}
			}
			if !srcProv.ExtensionCountKnown {
				return SourceCompatibilityResult{Compatible: false, Reason: "Omni source extension count must be known"}
			}
			if sourceDurationSec > 37.0 {
				return SourceCompatibilityResult{Compatible: false, Reason: fmt.Sprintf("source video duration (%.1fs) exceeds maximum allowed for Omni video extension (37s input, max 40s total)", sourceDurationSec)}
			}
			return SourceCompatibilityResult{Compatible: true}
		}

		return SourceCompatibilityResult{Compatible: false, Reason: fmt.Sprintf("model %q does not support video extension; only Google Veo 3.1 and Gemini Omni support extension", mID)}

	case pebblestore.VideoOperationCreate:
		return SourceCompatibilityResult{Compatible: true}

	default:
		return SourceCompatibilityResult{Compatible: false, Reason: fmt.Sprintf("unknown video operation %q", op)}
	}
}
