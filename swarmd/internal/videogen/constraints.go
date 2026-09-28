package videogen

import (
	"errors"
	"fmt"
	"math"
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
	Supported                  bool     `json:"supported"`
	Reason                     string   `json:"reason,omitempty"`
	SupportsDuration           bool     `json:"supports_duration"`
	MaxExternalDurationSec     float64  `json:"max_external_duration_sec,omitempty"`   // 10.0 for external uploads
	MaxSourceDurationSec       float64  `json:"max_source_duration_sec,omitempty"`     // 10.0 for backwards-compatibility
	RequiresHandleMatch        bool     `json:"requires_handle_match"`                 // true for conversational Omni
	RequiresHandleForLongVideo bool     `json:"requires_handle_for_long_video,omitempty"` // true: uploads > 10s require handle
	RequiresInteractionHandle  bool     `json:"requires_interaction_handle,omitempty"` // conversational continuation requires handle
	SupportedProviders         []string `json:"supported_providers,omitempty"`        // ["google"]
	RequiredSourceProvider     string   `json:"required_source_provider,omitempty"`    // "google"
	RequiredSourceTransport    string   `json:"required_source_transport,omitempty"`   // "google_interactions"
	SourceModelMatch           string   `json:"source_model_match,omitempty"`         // "gemini-omni-1.1-flash"
}

// VideoExtendConstraint defines the locked parameters and source rules for video extension (next scene).
type VideoExtendConstraint struct {
	Supported                      bool       `json:"supported"`
	Reason                         string     `json:"reason,omitempty"`
	LockedDurationSeconds          int        `json:"locked_duration_seconds,omitempty"`            // 8 for Veo, 0 for Omni
	LockedResolution               string     `json:"locked_resolution,omitempty"`                  // "720p" for Veo
	LockedAspectRatioMatchesSource bool       `json:"locked_aspect_ratio_matches_source,omitempty"` // true for Veo
	SupportedAspectRatios          []string   `json:"supported_aspect_ratios,omitempty"`            // ["16:9", "9:16"] for Veo
	SupportsDuration               bool       `json:"supports_duration"`                            // false for both
	MaxSourceDurationSec           float64    `json:"max_source_duration_sec,omitempty"`            // 141.0 for Veo, 37.0 for Omni
	MaxTotalDurationSec            float64    `json:"max_total_duration_sec,omitempty"`             // 148.0 for Veo, 40.0 for Omni
	MaxExtensionCount              int        `json:"max_extension_count,omitempty"`                // 20 for Veo (omitted for Omni)
	RequiresVeoSource              bool       `json:"requires_veo_source,omitempty"`                // true for Veo
	RequiresOmniSource             bool       `json:"requires_omni_source,omitempty"`               // true for Omni
	DisallowsVeoLiteSource         bool       `json:"disallows_veo_lite_source,omitempty"`          // true for Veo
	SourceObservedResolutions      []string   `json:"source_observed_resolutions,omitempty"`         // ["720p", "1280x720", "720x1280"]
	RequiresSourceProvenance       bool       `json:"requires_source_provenance,omitempty"`          // true for both
	RequiredSourceProvider         string     `json:"required_source_provider,omitempty"`            // "google"
	RequiredSourceTransport        string     `json:"required_source_transport,omitempty"`           // "google_predict_long_running" or "google_interactions"
	RequiresProviderResource       bool       `json:"requires_provider_resource,omitempty"`         // true for Veo
	RequiresInteractionHandle      bool       `json:"requires_interaction_handle,omitempty"`        // true for Omni
	RequiresOutputDigest           bool       `json:"requires_output_digest,omitempty"`             // true for Veo
	RequiresKnownExtensionCount    bool       `json:"requires_known_extension_count,omitempty"`      // true for both
	MaxReferenceAgeMs              int64      `json:"max_reference_age_ms,omitempty"`                // 48h (172800000ms) for Veo
	AllowedSourceModels            []string   `json:"allowed_source_models,omitempty"`              // ["veo-3.1-generate-preview", "veo-3.1-fast-generate-preview"] or ["gemini-omni-1.1-flash"]
	DisallowedSourceModels         []string   `json:"disallowed_source_models,omitempty"`           // ["veo-3.1-lite-generate-preview"]
	ObservedDimensionPairs         [][2]int   `json:"observed_dimension_pairs,omitempty"`           // [[1280, 720], [720, 1280]] for Veo
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
		editC.MaxExternalDurationSec = 10.0
		editC.MaxSourceDurationSec = 10.0
		editC.RequiresHandleMatch = true
		editC.RequiresHandleForLongVideo = true
		editC.RequiresInteractionHandle = false
		editC.SupportedProviders = []string{ProviderGoogleGemini}
		editC.RequiredSourceProvider = ProviderGoogleGemini
		editC.RequiredSourceTransport = pebblestore.VideoTransportGoogleInteractions
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
			extendC.RequiredSourceProvider = ProviderGoogleGemini
			extendC.RequiredSourceTransport = pebblestore.VideoTransportGooglePredictLongRunning
			extendC.RequiresProviderResource = true
			extendC.RequiresOutputDigest = true
			extendC.RequiresKnownExtensionCount = true
			extendC.MaxReferenceAgeMs = 48 * 3600 * 1000
			extendC.AllowedSourceModels = []string{"veo-3.1-generate-preview", "veo-3.1-fast-generate-preview"}
			extendC.DisallowedSourceModels = []string{"veo-3.1-lite-generate-preview"}
			extendC.ObservedDimensionPairs = [][2]int{{1280, 720}, {720, 1280}}
		} else if IsOmniModel(mID) {
			extendC.LockedDurationSeconds = 0
			extendC.MaxSourceDurationSec = 37.0
			extendC.MaxTotalDurationSec = 40.0
			extendC.RequiresOmniSource = true
			extendC.RequiredSourceProvider = ProviderGoogleGemini
			extendC.RequiredSourceTransport = pebblestore.VideoTransportGoogleInteractions
			extendC.RequiresInteractionHandle = true
			extendC.RequiresKnownExtensionCount = true
			extendC.AllowedSourceModels = []string{DefaultVideoIterationModel}
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

// ValidateSourceCompatibility validates whether a source video is eligible for targetModel and operation.
// It verifies finite duration, timestamps and expiry, provider/model constraints, handles, and dimensions.
func ValidateSourceCompatibility(
	targetProvider, targetModel, op string,
	srcProv *pebblestore.VideoProvenance,
	sourceWidth, sourceHeight int,
	sourceDurationSec float64,
) error {
	if math.IsNaN(sourceDurationSec) || math.IsInf(sourceDurationSec, 0) || sourceDurationSec < 0 {
		return errors.New("source video duration is invalid or not finite")
	}
	if sourceWidth < 0 || sourceHeight < 0 {
		return errors.New("source video dimensions cannot be negative")
	}

	opLower := strings.ToLower(strings.TrimSpace(op))
	pID := strings.ToLower(strings.TrimSpace(targetProvider))
	mID := strings.TrimSpace(targetModel)

	if srcProv != nil {
		now := time.Now().UnixMilli()
		if srcProv.ExpiresAt > 0 && now > srcProv.ExpiresAt {
			return errors.New("video source reference has expired")
		}
		if srcProv.CreatedAt > 0 && now < srcProv.CreatedAt-60000 {
			return errors.New("video source creation timestamp is in the future")
		}
	}

	switch opLower {
	case pebblestore.VideoOperationEdit:
		if IsVeoModel(mID) {
			return errors.New("Veo models do not support video editing; select an iteration model such as Gemini Omni")
		}
		if !IsOmniModel(mID) {
			return fmt.Errorf("selected model %q cannot edit a source video; select a video iteration model", mID)
		}
		if pID != ProviderGoogleGemini {
			return fmt.Errorf("video editing is not supported on provider %q; use Google Gemini Omni", targetProvider)
		}
		if !IsStableOmniModel(mID) {
			return fmt.Errorf("video editing is only supported on stable Gemini Omni (%s); %q is not supported", DefaultVideoIterationModel, mID)
		}
		hasHandle := srcProv != nil && (strings.TrimSpace(srcProv.InteractionID) != "" || srcProv.HasInteraction)
		if hasHandle {
			if srcProv.Model != "" && !strings.EqualFold(srcProv.Model, mID) {
				return fmt.Errorf("video interaction model mismatch: source was created with %q, cannot continue with %q", srcProv.Model, mID)
			}
		} else if sourceDurationSec > 10.0 {
			return fmt.Errorf("source video duration (%.1fs) exceeds maximum allowed for external video editing (10s)", sourceDurationSec)
		}
		return nil

	case pebblestore.VideoOperationExtend:
		if IsVeoModel(mID) {
			if pID != ProviderGoogleGemini {
				return errors.New("Veo native extension is only supported directly via Google Gemini, not OpenRouter")
			}
			if !IsVeo31Model(mID) || IsVeoLiteModel(mID) {
				return fmt.Errorf("Veo extension is only supported on Veo 3.1 standard or fast models; %q is not eligible", mID)
			}
			if srcProv == nil {
				return errors.New("Veo video extension requires trusted source provenance from a previous Veo generation")
			}
			if srcProv.Provider != ProviderGoogleGemini || !IsVeoModel(srcProv.Model) || IsOmniModel(srcProv.Model) {
				return fmt.Errorf("Veo extension requires a source generated by Google Veo; source was %s/%s", srcProv.Provider, srcProv.Model)
			}
			if IsVeoLiteModel(srcProv.Model) {
				return errors.New("Veo extension cannot extend videos generated by Veo Lite")
			}
			if srcProv.Transport != pebblestore.VideoTransportGooglePredictLongRunning {
				return fmt.Errorf("Veo source requires google_predict_long_running transport; got %q", srcProv.Transport)
			}
			if strings.TrimSpace(srcProv.ProviderResource) == "" && !srcProv.HasProviderResource {
				return errors.New("Veo source requires valid provider resource URI")
			}
			if strings.TrimSpace(srcProv.OutputDigestSHA256) == "" {
				return errors.New("Veo source requires non-empty output digest")
			}
			if srcProv.CreatedAt <= 0 || srcProv.ExpiresAt <= 0 {
				return errors.New("Veo source provenance missing timestamp metadata")
			}
			now := time.Now().UnixMilli()
			if now > srcProv.ExpiresAt || time.Since(time.UnixMilli(srcProv.CreatedAt)) > 48*time.Hour {
				return errors.New("Veo source video reference has expired (exceeds 48-hour validity period)")
			}
			if !srcProv.ExtensionCountKnown {
				return errors.New("Veo source extension count must be known")
			}
			if srcProv.ExtensionCount < 0 || srcProv.ExtensionCount >= 20 {
				return errors.New("video extension limit reached (20 extensions maximum)")
			}
			if sourceDurationSec > 141.0 {
				return fmt.Errorf("source video duration (%.1fs) exceeds maximum allowed for Veo extension (141s)", sourceDurationSec)
			}
			if sourceDurationSec+7.0 > 148.0 {
				return fmt.Errorf("extending source video (%.1fs) would exceed maximum output duration (148s)", sourceDurationSec)
			}
			isSource16x9 := (sourceWidth == 1280 && sourceHeight == 720) && (srcProv.ObservedWidth == 1280 && srcProv.ObservedHeight == 720)
			isSource9x16 := (sourceWidth == 720 && sourceHeight == 1280) && (srcProv.ObservedWidth == 720 && srcProv.ObservedHeight == 1280)
			if !isSource16x9 && !isSource9x16 {
				return fmt.Errorf("Veo source requires observed 720p dimensions (1280x720 or 720x1280); got source=%dx%d observed=%dx%d", sourceWidth, sourceHeight, srcProv.ObservedWidth, srcProv.ObservedHeight)
			}
			return nil

		} else if IsOmniModel(mID) {
			if !IsStableOmniModel(mID) {
				return fmt.Errorf("Omni video extension is only supported on stable model %s; %q is not eligible", DefaultVideoIterationModel, mID)
			}
			if pID != ProviderGoogleGemini {
				return fmt.Errorf("video extension is not supported on provider %q; use Google Gemini Omni", targetProvider)
			}
			if srcProv == nil {
				return errors.New("Omni video extension requires verified source provenance")
			}
			if srcProv.Provider != ProviderGoogleGemini || !IsStableOmniModel(srcProv.Model) {
				return fmt.Errorf("Omni extension requires source generated by stable Gemini Omni; got %s/%s", srcProv.Provider, srcProv.Model)
			}
			if srcProv.Transport != pebblestore.VideoTransportGoogleInteractions {
				return fmt.Errorf("Omni extension requires google_interactions transport; got %q", srcProv.Transport)
			}
			if strings.TrimSpace(srcProv.InteractionID) == "" && !srcProv.HasInteraction {
				return errors.New("Omni source provenance is missing interaction handle; cannot extend")
			}
			if !srcProv.ExtensionCountKnown {
				return errors.New("Omni source extension count must be known")
			}
			if sourceDurationSec > 37.0 {
				return fmt.Errorf("source video duration (%.1fs) exceeds maximum allowed for Omni video extension (37s input, max 40s total)", sourceDurationSec)
			}
			return nil
		}
		return fmt.Errorf("model %q does not support video extension; only Google Veo 3.1 and Gemini Omni support extension", mID)

	case pebblestore.VideoOperationCreate:
		return nil

	default:
		return fmt.Errorf("unknown video operation %q", op)
	}
}

// CheckSourceCompatibility determines whether a specific source video is compatible with targetModel and operation.
func CheckSourceCompatibility(
	targetProvider, targetModel, op string,
	srcProv *pebblestore.VideoProvenance,
	sourceWidth, sourceHeight int,
	sourceDurationSec float64,
) SourceCompatibilityResult {
	if err := ValidateSourceCompatibility(targetProvider, targetModel, op, srcProv, sourceWidth, sourceHeight, sourceDurationSec); err != nil {
		return SourceCompatibilityResult{Compatible: false, Reason: err.Error()}
	}
	return SourceCompatibilityResult{Compatible: true}
}
