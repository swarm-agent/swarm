package videogen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// VideoPreflightRequest carries all inputs required to validate, resolve, and
// preflight a video creation, editing, or extension request before submission or execution.
type VideoPreflightRequest struct {
	AccountScopeID        string
	Operation             string // "create" | "edit" | "extend"
	ExplicitModel         string // user-specified model identifier
	AspectRatio           string
	Resolution            string
	DurationSeconds       int
	Prompt                string
	Principal             identity.Principal
	Source                *ManagedVideoSource
	SourceProvenance      *pebblestore.VideoProvenance
	Image                 *ManagedVideoImage
	SourceDurationSeconds float64
	SourceWidth           int
	SourceHeight          int
	IsQueuedTask          bool
}

// VideoPreflightResult holds resolved and verified execution parameters.
type VideoPreflightResult struct {
	Operation             string
	ResolvedModel         string
	ResolvedProvider      string
	ResolvedTransport     string
	CredentialID          string
	CredentialVersion     string
	AspectRatio           string
	Resolution            string
	DurationSeconds       int
	AdvisoryWarnings      []string
	EstimatedCostUSD      float64
	PriceStatus           string
	PricingSummary        string
	SnapshotID            string
	SnapshotVersion       string
	ModelRecord           pebblestore.ModelCatalogRecord
	ParsedOptions         *ParsedVideoOptions
	SourceLink            *pebblestore.VideoSourceLink
	SourceExtensionCount  int
	RequiresFilesUpload   bool
	IsRetainedInteraction bool
}

// PreflightVideoOperation validates and resolves a video request.
// It enforces operation discrimination, model resolution without silent fallback,
// input compatibility, source provenance verification, and provider constraints.
func (s *Service) PreflightVideoOperation(ctx context.Context, req VideoPreflightRequest) (*VideoPreflightResult, error) {
	if s == nil {
		return nil, errors.New("videogen service is not configured")
	}

	accountScopeID := strings.TrimSpace(req.Principal.AccountScopeID)
	if accountScopeID == "" {
		accountScopeID = strings.TrimSpace(req.AccountScopeID)
	}
	if accountScopeID == "" {
		return nil, errors.New("account scope id is required")
	}

	hasSource := (req.Source != nil && (len(req.Source.Bytes) > 0 || strings.TrimSpace(req.Source.InteractionID) != "" || strings.TrimSpace(req.Source.URI) != "" || req.Source.Provenance != nil)) || req.SourceProvenance != nil
	hasImage := req.Image != nil && len(req.Image.Bytes) > 0

	// 1. Validate mutual exclusion of Source video and Image input
	if hasSource && hasImage {
		return nil, errors.New("cannot combine source video with initial image; choose either video editing/extension or image-to-video creation")
	}

	// 2. Validate Operation discriminator against presence of Source
	op := strings.ToLower(strings.TrimSpace(req.Operation))
	if hasSource {
		if op == "" {
			return nil, errors.New("video operation must be explicitly specified when source media is provided (edit or extend)")
		}
		if op == pebblestore.VideoOperationCreate {
			return nil, errors.New("cannot provide source video for create operation; use edit or extend")
		}
		if op != pebblestore.VideoOperationEdit && op != pebblestore.VideoOperationExtend {
			return nil, fmt.Errorf("unsupported video operation %q with source; must be edit or extend", op)
		}
	} else {
		if op == pebblestore.VideoOperationEdit || op == pebblestore.VideoOperationExtend {
			return nil, fmt.Errorf("video %s operation requires source video", op)
		}
		if op == "" {
			op = pebblestore.VideoOperationCreate
		} else if op != pebblestore.VideoOperationCreate {
			return nil, fmt.Errorf("unsupported video operation %q", op)
		}
	}

	if hasImage && op != pebblestore.VideoOperationCreate {
		return nil, fmt.Errorf("initial image input is not supported for video %s operation; only create operation supports initial image", op)
	}

	// 3. Resolve source provenance
	srcProv := req.SourceProvenance
	if srcProv == nil && req.Source != nil {
		srcProv = req.Source.Provenance
	}

	// 4. Measure source video dimensions and duration safely in preflight
	var sourceDurationSeconds float64 = req.SourceDurationSeconds
	var sourceWidth int = req.SourceWidth
	var sourceHeight int = req.SourceHeight

	if req.Source != nil && len(req.Source.Bytes) > 0 {
		if sourceDurationSeconds <= 0 || math.IsNaN(sourceDurationSeconds) || math.IsInf(sourceDurationSeconds, 0) || sourceWidth <= 0 || sourceHeight <= 0 {
			srcMeta, err := s.probeVideoBytes(ctx, req.Source.Bytes)
			if err != nil {
				return nil, fmt.Errorf("probe source video: %w", err)
			}
			if srcMeta.DurationSeconds <= 0 || math.IsNaN(srcMeta.DurationSeconds) || math.IsInf(srcMeta.DurationSeconds, 0) {
				return nil, errors.New("source video duration must be positive and finite")
			}
			if srcMeta.Width <= 0 || srcMeta.Height <= 0 {
				return nil, errors.New("source video dimensions must be positive")
			}
			sourceDurationSeconds = srcMeta.DurationSeconds
			sourceWidth = srcMeta.Width
			sourceHeight = srcMeta.Height
		}
	}
	if sourceDurationSeconds <= 0 && srcProv != nil {
		// Handle-only uses trusted observed metadata from provenance
		if srcProv.ObservedDurationMs > 0 {
			sourceDurationSeconds = float64(srcProv.ObservedDurationMs) / 1000.0
		}
		if sourceWidth <= 0 {
			sourceWidth = srcProv.ObservedWidth
		}
		if sourceHeight <= 0 {
			sourceHeight = srcProv.ObservedHeight
		}
	}

	if op == pebblestore.VideoOperationEdit || op == pebblestore.VideoOperationExtend {
		if sourceDurationSeconds <= 0 || math.IsNaN(sourceDurationSeconds) || math.IsInf(sourceDurationSeconds, 0) {
			return nil, errors.New("source video duration must be positive and finite")
		}
		if sourceWidth <= 0 || sourceHeight <= 0 {
			return nil, errors.New("source video dimensions must be positive")
		}
	}

	// 5. Resolve Target Model without hardcoded silent fallbacks
	var targetModel, providerID string
	explicitModel := strings.TrimSpace(req.ExplicitModel)

	if explicitModel != "" {
		targetModel, providerID = s.parseModelAndProvider(explicitModel)
	} else {
		var defaultModel, iterationModel string
		if s.uiSettings != nil && accountScopeID != "" {
			ui, err := s.uiSettings.GetForAccount(accountScopeID)
			if err == nil {
				defaultModel = strings.TrimSpace(ui.Tools.Video.DefaultModel)
				iterationModel = strings.TrimSpace(ui.Tools.Video.IterationModel)
			}
		}

		switch op {
		case pebblestore.VideoOperationEdit:
			if iterationModel == "" {
				return nil, errors.New("no default video iteration model configured for account; select a model or configure one in Settings")
			}
			targetModel, providerID = s.parseModelAndProvider(iterationModel)

		case pebblestore.VideoOperationExtend:
			isOmniSource := (req.Source != nil && strings.TrimSpace(req.Source.InteractionID) != "") || (srcProv != nil && IsOmniModel(srcProv.Model)) || (req.Source != nil && IsOmniModel(req.Source.Model))
			isVeoSource := (srcProv != nil && IsVeoModel(srcProv.Model)) || (req.Source != nil && IsVeoModel(req.Source.Model))

			if isOmniSource {
				if iterationModel == "" {
					return nil, errors.New("no default video iteration model configured for account; select a model or configure one in Settings")
				}
				targetModel, providerID = s.parseModelAndProvider(iterationModel)
			} else if isVeoSource {
				if defaultModel == "" {
					return nil, errors.New("no default video model configured for account; select a model or configure one in Settings")
				}
				targetModel, providerID = s.parseModelAndProvider(defaultModel)
			} else {
				if defaultModel == "" {
					return nil, errors.New("no default video model configured for account; select a model or configure one in Settings")
				}
				targetModel, providerID = s.parseModelAndProvider(defaultModel)
			}

		case pebblestore.VideoOperationCreate:
			if defaultModel == "" {
				return nil, errors.New("no default video model configured for account; select a model or configure one in Settings")
			}
			targetModel, providerID = s.parseModelAndProvider(defaultModel)
		}
	}

	// 6. Validate Model in Catalog
	modelRecord, found := s.resolveModelRecord(providerID, targetModel)
	if !found {
		return nil, fmt.Errorf("selected video model %q is not in the model catalog", targetModel)
	}

	hasVideoOutput := ContainsStringFold(modelRecord.CatalogModalities.Outputs, "video") ||
		ContainsStringFold(modelRecord.CatalogModalities.Categories, "video_generation") ||
		ContainsStringFold(modelRecord.CatalogModalities.Categories, "video_iteration")
	if !hasVideoOutput {
		return nil, fmt.Errorf("selected model %q does not support video output", targetModel)
	}

	opts := ExtractVideoOptions(modelRecord)
	var advisoryWarnings []string
	var requiresFilesUpload, isRetainedInteraction bool
	var resolvedAspectRatio, resolvedResolution string
	resolvedDurationSeconds := req.DurationSeconds

	// 7. Check Active Credential context for provider
	var credID, credVersion string
	if s.authStore != nil && accountScopeID != "" {
		activeCred, ok, err := s.authStore.GetActiveCredentialForAccount(accountScopeID, providerID)
		if err != nil {
			return nil, fmt.Errorf("read %s credentials: %w", providerID, err)
		}
		if !ok || strings.TrimSpace(activeCred.APIKey) == "" {
			return nil, fmt.Errorf("%s api key is not configured; add an API key in Settings -> Providers", providerID)
		}
		credID = activeCred.ID
		if activeCred.UpdatedAt > 0 {
			credVersion = fmt.Sprintf("v%d", activeCred.UpdatedAt)
		}
	}

	// 8. Verify Source Provenance and Handle binding fail-closed
	hasInteractionHandle := req.Source != nil && strings.TrimSpace(req.Source.InteractionID) != ""
	isHandleOnly := req.Source != nil && len(req.Source.Bytes) == 0 && (strings.TrimSpace(req.Source.InteractionID) != "" || strings.TrimSpace(req.Source.URI) != "")

	if (isHandleOnly || hasInteractionHandle) && srcProv == nil {
		return nil, errors.New("source-only video handle requires verified source provenance bound to caller identity")
	}

	if srcProv != nil {
		if strings.TrimSpace(srcProv.AccountScopeID) == "" {
			return nil, errors.New("source provenance account_scope_id is required")
		}
		if srcProv.AccountScopeID != accountScopeID {
			return nil, errors.New("video source belongs to a different account scope")
		}
		if strings.TrimSpace(srcProv.CredentialID) == "" {
			return nil, errors.New("source provenance credential_id is required")
		}
		if credID != "" && srcProv.CredentialID != credID {
			return nil, fmt.Errorf("video interaction credential mismatch: source used credential %q, active credential is %q", srcProv.CredentialID, credID)
		}
		if credVersion != "" {
			if strings.TrimSpace(srcProv.CredentialVersion) == "" {
				return nil, errors.New("source provenance credential_version is required")
			}
			if srcProv.CredentialVersion != credVersion {
				return nil, fmt.Errorf("video interaction credential version mismatch: source used version %q, active credential is %q", srcProv.CredentialVersion, credVersion)
			}
		}
		if hasInteractionHandle {
			if strings.TrimSpace(srcProv.InteractionID) == "" {
				return nil, errors.New("source provenance missing interaction ID")
			}
			if strings.TrimSpace(req.Source.InteractionID) != strings.TrimSpace(srcProv.InteractionID) {
				return nil, fmt.Errorf("source interaction handle %q does not match stored provenance interaction ID %q", req.Source.InteractionID, srcProv.InteractionID)
			}
		}
		if IsOmniModel(srcProv.Model) && strings.TrimSpace(srcProv.InteractionID) == "" {
			return nil, errors.New("Omni source provenance is missing interaction handle; cannot continue conversation or extend")
		}
		if srcProv.ExpiresAt > 0 && time.Now().UnixMilli() > srcProv.ExpiresAt {
			return nil, errors.New("video source reference has expired")
		}
		if req.Source != nil && len(req.Source.Bytes) > 0 && srcProv.OutputDigestSHA256 != "" {
			h := sha256.Sum256(req.Source.Bytes)
			calcDigest := hex.EncodeToString(h[:])
			if !strings.EqualFold(calcDigest, srcProv.OutputDigestSHA256) {
				return nil, errors.New("source video bytes do not match expected provenance digest")
			}
		}
	} else if op == pebblestore.VideoOperationExtend {
		return nil, errors.New("video extension requires verified source provenance")
	}

	// 9. Operation-Specific Routing & Invariants
	switch op {
	case pebblestore.VideoOperationEdit:
		if IsVeoModel(targetModel) {
			return nil, errors.New("Veo models do not support video editing; select an iteration model such as Gemini Omni")
		}
		if !IsOmniModel(targetModel) {
			return nil, fmt.Errorf("selected model %q cannot edit a source video; select a video iteration model", targetModel)
		}
		if providerID != ProviderGoogleGemini {
			return nil, fmt.Errorf("video editing is not supported on provider %q; use Google Gemini Omni", providerID)
		}
		if !IsStableOmniModel(targetModel) {
			return nil, fmt.Errorf("video editing is only supported on stable Gemini Omni (%s); %q is not supported", DefaultVideoIterationModel, targetModel)
		}
		if opts == nil || !opts.ConversationalEditingSupported {
			return nil, fmt.Errorf("model %q does not support conversational video editing", targetModel)
		}

		if req.DurationSeconds > 0 {
			return nil, fmt.Errorf("model %q does not accept duration selection", targetModel)
		}
		resolvedDurationSeconds = 0

		if (req.Source != nil && strings.TrimSpace(req.Source.InteractionID) != "") || (srcProv != nil && strings.TrimSpace(srcProv.InteractionID) != "") {
			isRetainedInteraction = true
			if srcProv != nil && srcProv.Model != "" && !strings.EqualFold(srcProv.Model, targetModel) {
				return nil, fmt.Errorf("video interaction model mismatch: source was created with %q, cannot continue with %q", srcProv.Model, targetModel)
			}
		} else {
			// External upload without interaction ID
			requiresFilesUpload = true
			if req.Source == nil || len(req.Source.Bytes) == 0 {
				return nil, errors.New("video editing without interaction handle requires source video bytes")
			}
			if sourceDurationSeconds > 10.0 {
				return nil, fmt.Errorf("source video duration (%.1fs) exceeds maximum allowed for external video editing (10s)", sourceDurationSeconds)
			}
			advisoryWarnings = append(advisoryWarnings, "editing uploaded external videos may be restricted in the EU/EEA, UK, and Switzerland; generate the initial video with Gemini Omni Flash to enable multi-turn conversational editing in these regions")
		}

	case pebblestore.VideoOperationExtend:
		if IsVeoModel(targetModel) {
			if providerID != ProviderGoogleGemini {
				return nil, errors.New("Veo native extension is only supported directly via Google Gemini, not OpenRouter")
			}
			if !IsVeo31Model(targetModel) || IsVeoLiteModel(targetModel) {
				return nil, fmt.Errorf("Veo extension is only supported on Veo 3.1 standard or fast models; %q is not eligible", targetModel)
			}
			if opts == nil || !opts.VideoExtensionSupported {
				return nil, fmt.Errorf("model %q does not support video extension in catalog metadata", targetModel)
			}
			if srcProv == nil {
				return nil, errors.New("Veo video extension requires trusted source provenance from a previous Veo generation")
			}
			if srcProv.Provider != ProviderGoogleGemini || !IsVeoModel(srcProv.Model) || IsOmniModel(srcProv.Model) {
				return nil, fmt.Errorf("Veo extension requires a source generated by Google Veo; source was %s/%s", srcProv.Provider, srcProv.Model)
			}
			if IsVeoLiteModel(srcProv.Model) {
				return nil, errors.New("Veo extension cannot extend videos generated by Veo Lite")
			}
			if srcProv.Transport != pebblestore.VideoTransportGooglePredictLongRunning {
				return nil, fmt.Errorf("Veo source requires google_predict_long_running transport; got %q", srcProv.Transport)
			}
			if strings.TrimSpace(srcProv.ProviderResource) == "" {
				return nil, errors.New("Veo source requires valid provider resource URI")
			}
			if strings.TrimSpace(srcProv.OutputDigestSHA256) == "" {
				return nil, errors.New("Veo source requires non-empty output digest")
			}
			if srcProv.CreatedAt <= 0 {
				return nil, errors.New("Veo source provenance missing created_at timestamp")
			}
			if srcProv.ExpiresAt <= 0 {
				return nil, errors.New("Veo source provenance missing expires_at timestamp")
			}
			now := time.Now().UnixMilli()
			if now > srcProv.ExpiresAt || time.Since(time.UnixMilli(srcProv.CreatedAt)) > 48*time.Hour {
				return nil, errors.New("Veo source video reference has expired (exceeds 48-hour validity period)")
			}
			if !srcProv.ExtensionCountKnown {
				return nil, errors.New("Veo source extension count must be known")
			}
			if srcProv.ExtensionCount < 0 || srcProv.ExtensionCount >= 20 {
				return nil, errors.New("video extension limit reached (20 extensions maximum)")
			}
			if sourceDurationSeconds > 141.0 {
				return nil, fmt.Errorf("source video duration (%.1fs) exceeds maximum allowed for Veo extension (141s)", sourceDurationSeconds)
			}
			if sourceDurationSeconds+7.0 > 148.0 {
				return nil, fmt.Errorf("extending source video (%.1fs) would exceed maximum output duration (148s)", sourceDurationSeconds)
			}
			if req.DurationSeconds != 0 && req.DurationSeconds != 8 {
				return nil, fmt.Errorf("Veo video extension only supports 8s duration (got %d)", req.DurationSeconds)
			}
			resolvedDurationSeconds = 8

			// Enforce 720p resolution
			if req.Resolution != "" && !strings.EqualFold(NormalizeResolution(req.Resolution), "720p") {
				return nil, fmt.Errorf("Veo video extension requires 720p resolution; got %q", req.Resolution)
			}
			resolvedResolution = "720p"

			// Source dimensions must be observed exactly 1280x720 or 720x1280
			isSource16x9 := (sourceWidth == 1280 && sourceHeight == 720) && (srcProv.ObservedWidth == 1280 && srcProv.ObservedHeight == 720)
			isSource9x16 := (sourceWidth == 720 && sourceHeight == 1280) && (srcProv.ObservedWidth == 720 && srcProv.ObservedHeight == 1280)
			if !isSource16x9 && !isSource9x16 {
				return nil, fmt.Errorf("Veo source requires observed 720p dimensions (1280x720 or 720x1280); got source=%dx%d observed=%dx%d", sourceWidth, sourceHeight, srcProv.ObservedWidth, srcProv.ObservedHeight)
			}

			// Use source aspect ratio, not default if omitted
			if req.AspectRatio == "" {
				if isSource16x9 {
					resolvedAspectRatio = "16:9"
				} else {
					resolvedAspectRatio = "9:16"
				}
			} else {
				ar := NormalizeAspectRatio(req.AspectRatio)
				if ar != "16:9" && ar != "9:16" {
					return nil, fmt.Errorf("Veo video extension requires 16:9 or 9:16 aspect ratio; got %q", req.AspectRatio)
				}
				if ar == "16:9" && !isSource16x9 {
					return nil, fmt.Errorf("requested aspect ratio 16:9 does not match source dimensions (%dx%d)", sourceWidth, sourceHeight)
				}
				if ar == "9:16" && !isSource9x16 {
					return nil, fmt.Errorf("requested aspect ratio 9:16 does not match source dimensions (%dx%d)", sourceWidth, sourceHeight)
				}
				resolvedAspectRatio = ar
			}

		} else if IsOmniModel(targetModel) {
			if !IsStableOmniModel(targetModel) {
				return nil, fmt.Errorf("Omni video extension is only supported on stable model %s; %q is not eligible", DefaultVideoIterationModel, targetModel)
			}
			if providerID != ProviderGoogleGemini {
				return nil, fmt.Errorf("video extension is not supported on provider %q; use Google Gemini Omni", providerID)
			}
			if opts == nil || !opts.VideoExtensionSupported {
				return nil, fmt.Errorf("model %q does not support video extension in catalog metadata", targetModel)
			}
			if req.DurationSeconds > 0 {
				return nil, fmt.Errorf("model %q does not accept duration selection", targetModel)
			}
			resolvedDurationSeconds = 0

			if srcProv == nil {
				return nil, errors.New("Omni video extension requires verified source provenance")
			}
			if srcProv.Provider != ProviderGoogleGemini || !IsStableOmniModel(srcProv.Model) {
				return nil, fmt.Errorf("Omni extension requires source generated by stable Gemini Omni; got %s/%s", srcProv.Provider, srcProv.Model)
			}
			if srcProv.Transport != pebblestore.VideoTransportGoogleInteractions {
				return nil, fmt.Errorf("Omni extension requires google_interactions transport; got %q", srcProv.Transport)
			}
			if strings.TrimSpace(srcProv.InteractionID) == "" {
				return nil, errors.New("Omni source provenance is missing interaction handle; cannot extend")
			}
			if !srcProv.ExtensionCountKnown {
				return nil, errors.New("Omni source extension count must be known")
			}
			if sourceDurationSeconds > 37.0 {
				return nil, fmt.Errorf("source video duration (%.1fs) exceeds maximum allowed for Omni video extension (37s input, max 40s total)", sourceDurationSeconds)
			}
			isRetainedInteraction = true

		} else {
			return nil, fmt.Errorf("model %q does not support video extension; only Google Veo 3.1 and Gemini Omni support extension", targetModel)
		}

	case pebblestore.VideoOperationCreate:
		if IsOmniModel(targetModel) {
			if req.DurationSeconds > 0 {
				return nil, fmt.Errorf("model %q does not accept duration selection", targetModel)
			}
			resolvedDurationSeconds = 0
		}
	}

	// 10. Aspect Ratio & Resolution defaults / validation if not already resolved
	if resolvedAspectRatio == "" {
		if req.AspectRatio != "" {
			if opts == nil || len(opts.AspectRatios) == 0 {
				return nil, errors.New("video aspect ratio metadata unavailable for selected model")
			}
			if !ContainsStringFold(opts.AspectRatios, req.AspectRatio) && !IsEquivalentAspectRatio(opts.AspectRatios, req.AspectRatio) {
				return nil, fmt.Errorf("unsupported aspect ratio %q for model %q; supported: %s", req.AspectRatio, targetModel, strings.Join(opts.AspectRatios, ", "))
			}
			resolvedAspectRatio = NormalizeAspectRatio(req.AspectRatio)
		} else if opts != nil && opts.DefaultRatio != "" {
			resolvedAspectRatio = opts.DefaultRatio
		}
	}

	if resolvedResolution == "" {
		if req.Resolution != "" {
			if opts == nil || len(opts.Resolutions) == 0 {
				return nil, errors.New("video resolution metadata unavailable for selected model")
			}
			if !ContainsStringFold(opts.Resolutions, req.Resolution) {
				return nil, fmt.Errorf("unsupported resolution %q for model %q; supported: %s", req.Resolution, targetModel, strings.Join(opts.Resolutions, ", "))
			}
			resolvedResolution = NormalizeResolution(req.Resolution)
		} else if opts != nil && opts.DefaultRes != "" {
			resolvedResolution = opts.DefaultRes
		}
	}

	// Duration check for non-Omni models during create
	if op == pebblestore.VideoOperationCreate && !IsOmniModel(targetModel) {
		resLower := strings.ToLower(resolvedResolution)
		var allowedDurs []int
		if opts != nil && len(opts.Durations) > 0 {
			allowedDurs = opts.Durations
		}
		if opts != nil && opts.ResolutionDurations != nil {
			if rd, ok := opts.ResolutionDurations[resLower]; ok && len(rd) > 0 {
				allowedDurs = rd
			}
		}
		if req.DurationSeconds > 0 {
			if len(allowedDurs) == 0 {
				return nil, errors.New("video duration metadata unavailable for selected model")
			}
			foundDur := false
			for _, d := range allowedDurs {
				if d == req.DurationSeconds {
					foundDur = true
					break
				}
			}
			if !foundDur {
				if (resLower == "1080p" || resLower == "4k") && IsVeoModel(targetModel) {
					return nil, fmt.Errorf("video resolution %s requires 8s duration", resolvedResolution)
				}
				var durStrs []string
				for _, d := range allowedDurs {
					durStrs = append(durStrs, fmt.Sprintf("%d", d))
				}
				return nil, fmt.Errorf("unsupported duration %d seconds for model %q at %s; supported durations are %s", req.DurationSeconds, targetModel, resolvedResolution, strings.Join(durStrs, ", "))
			}
			resolvedDurationSeconds = req.DurationSeconds
		} else if opts != nil && opts.DefaultDur > 0 {
			resolvedDurationSeconds = opts.DefaultDur
		}
	}

	// Initial image capability check for create
	if hasImage {
		if opts == nil || !opts.InitialImageSupported {
			return nil, fmt.Errorf("model %q does not support initial image input", targetModel)
		}
	}

	// 11. Transport Resolution
	var resolvedTransport string
	switch providerID {
	case ProviderGoogleGemini:
		if IsOmniModel(targetModel) {
			resolvedTransport = pebblestore.VideoTransportGoogleInteractions
		} else {
			resolvedTransport = pebblestore.VideoTransportGooglePredictLongRunning
		}
	case ProviderOpenRouter:
		resolvedTransport = pebblestore.VideoTransportOpenRouterVideos
	default:
		resolvedTransport = "unknown"
	}

	// 12. Cost estimation
	estimate := pebblestore.EstimateMediaCostFromRecord(modelRecord, pebblestore.MediaCostEstimateOptions{
		Provider:        providerID,
		Model:           targetModel,
		Kind:            "video",
		Count:           1,
		DurationSeconds: resolvedDurationSeconds,
		Resolution:      resolvedResolution,
		AspectRatio:     resolvedAspectRatio,
		IncludesAudio:   true,
		IsIteration:     op != pebblestore.VideoOperationCreate,
		ServiceTier:     "standard",
	})

	var sourceLink *pebblestore.VideoSourceLink
	sourceExtCount := 0
	if req.Source != nil && req.Source.SourceLink != nil {
		sourceLink = req.Source.SourceLink.Clone()
	}
	if srcProv != nil {
		sourceExtCount = srcProv.ExtensionCount
	}

	return &VideoPreflightResult{
		Operation:             op,
		ResolvedModel:         targetModel,
		ResolvedProvider:      providerID,
		ResolvedTransport:     resolvedTransport,
		CredentialID:          credID,
		CredentialVersion:     credVersion,
		AspectRatio:           resolvedAspectRatio,
		Resolution:            resolvedResolution,
		DurationSeconds:       resolvedDurationSeconds,
		AdvisoryWarnings:      advisoryWarnings,
		EstimatedCostUSD:      estimate.CostUSD,
		PriceStatus:           estimate.PriceStatus,
		PricingSummary:        estimate.PricingSummary,
		SnapshotID:            estimate.SnapshotID,
		SnapshotVersion:       estimate.SnapshotVersion,
		ModelRecord:           modelRecord,
		ParsedOptions:         opts,
		SourceLink:            sourceLink,
		SourceExtensionCount:  sourceExtCount,
		RequiresFilesUpload:   requiresFilesUpload,
		IsRetainedInteraction: isRetainedInteraction,
	}, nil
}

func (s *Service) parseModelAndProvider(modelID string) (string, string) {
	trimmed := strings.TrimSpace(modelID)
	if strings.Contains(trimmed, ":") {
		parts := strings.SplitN(trimmed, ":", 2)
		return strings.TrimSpace(parts[1]), strings.ToLower(strings.TrimSpace(parts[0]))
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "openrouter/") && strings.Count(trimmed, "/") >= 2 {
		return trimmed[len("openrouter/"):], ProviderOpenRouter
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "google/") && !strings.Contains(trimmed[len("google/"):], "/") {
		return trimmed, ProviderOpenRouter
	}
	return trimmed, s.inferProvider(trimmed)
}
