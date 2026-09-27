package videogen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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

	// 3. Resolve and verify Source Provenance and Handle binding
	srcProv := req.SourceProvenance
	if srcProv == nil && req.Source != nil {
		srcProv = req.Source.Provenance
	}

	if req.Source != nil {
		isHandleOnly := len(req.Source.Bytes) == 0 && (strings.TrimSpace(req.Source.InteractionID) != "" || strings.TrimSpace(req.Source.URI) != "")
		if isHandleOnly {
			if srcProv == nil {
				return nil, errors.New("source-only video handle requires verified source provenance bound to caller identity")
			}
			if srcProv.AccountScopeID != "" && accountScopeID != "" && srcProv.AccountScopeID != accountScopeID {
				return nil, errors.New("source-only video handle belongs to a different account scope")
			}
		}
	}

	if srcProv != nil {
		if srcProv.AccountScopeID != "" && accountScopeID != "" && srcProv.AccountScopeID != accountScopeID {
			return nil, errors.New("video source belongs to a different account scope")
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
	}

	// 4. Resolve Target Model without hardcoded silent fallbacks
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

	// 5. Validate Model in Catalog
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

	// 6. Check Active Credential context for provider
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

	// 7. Operation-Specific Routing & Invariants
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
			if opts == nil || !opts.ConversationalEditingSupported {
				return nil, fmt.Errorf("model %q does not support conversational video editing", targetModel)
			}
		}

		if req.DurationSeconds > 0 {
			return nil, fmt.Errorf("model %q does not accept duration selection", targetModel)
		}
		resolvedDurationSeconds = 0

		if req.Source != nil && strings.TrimSpace(req.Source.InteractionID) != "" {
			isRetainedInteraction = true
			if srcProv != nil && srcProv.Model != "" && !strings.EqualFold(srcProv.Model, targetModel) {
				return nil, fmt.Errorf("video interaction model mismatch: source was created with %q, cannot continue with %q", srcProv.Model, targetModel)
			}
			if srcProv != nil && srcProv.CredentialID != "" && credID != "" && srcProv.CredentialID != credID {
				return nil, fmt.Errorf("video interaction credential mismatch: source used credential %q, active credential is %q", srcProv.CredentialID, credID)
			}
		} else {
			requiresFilesUpload = true
			if req.SourceDurationSeconds > 10.0 {
				return nil, fmt.Errorf("source video duration (%.1fs) exceeds maximum allowed for external video editing (10s)", req.SourceDurationSeconds)
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
			if srcProv == nil {
				return nil, errors.New("Veo video extension requires trusted source provenance from a previous Veo generation")
			}
			if srcProv.Provider != ProviderGoogleGemini || !IsVeoModel(srcProv.Model) || IsOmniModel(srcProv.Model) {
				return nil, fmt.Errorf("Veo extension requires a source generated by Google Veo; source was %s/%s", srcProv.Provider, srcProv.Model)
			}
			if IsVeoLiteModel(srcProv.Model) {
				return nil, errors.New("Veo extension cannot extend videos generated by Veo Lite")
			}

			// Enforce 720p resolution
			if req.Resolution != "" && !strings.EqualFold(NormalizeResolution(req.Resolution), "720p") {
				return nil, fmt.Errorf("Veo video extension requires 720p resolution; got %q", req.Resolution)
			}
			resolvedResolution = "720p"

			// Enforce 16:9 or 9:16 aspect ratio
			ar := NormalizeAspectRatio(req.AspectRatio)
			if ar == "" && opts != nil {
				ar = opts.DefaultRatio
			}
			if ar != "16:9" && ar != "9:16" {
				return nil, fmt.Errorf("Veo video extension requires 16:9 or 9:16 aspect ratio; got %q", req.AspectRatio)
			}
			resolvedAspectRatio = ar

			// Enforce max 20 extensions
			if srcProv.ExtensionCount >= 20 {
				return nil, errors.New("video extension limit reached (20 extensions maximum)")
			}

			// Enforce 2-day known reference validity
			if srcProv.CreatedAt > 0 && time.Since(time.UnixMilli(srcProv.CreatedAt)) > 48*time.Hour {
				return nil, errors.New("Veo source video reference has expired (exceeds 48-hour validity period)")
			}

			// Enforce input duration <= 141s and output ceiling <= 148s
			if req.SourceDurationSeconds > 141.0 {
				return nil, fmt.Errorf("source video duration (%.1fs) exceeds maximum allowed for Veo extension (141s)", req.SourceDurationSeconds)
			}
			if req.SourceDurationSeconds > 0 && req.SourceDurationSeconds+7.0 > 148.0 {
				return nil, fmt.Errorf("extending source video (%.1fs) would exceed maximum output duration (148s)", req.SourceDurationSeconds)
			}

			// Veo extension REST parameter durationSeconds must be 8
			resolvedDurationSeconds = 8

		} else if IsOmniModel(targetModel) {
			if providerID != ProviderGoogleGemini {
				return nil, fmt.Errorf("video extension is not supported on provider %q; use Google Gemini Omni", providerID)
			}
			if req.DurationSeconds > 0 {
				return nil, fmt.Errorf("model %q does not accept duration selection", targetModel)
			}
			resolvedDurationSeconds = 0

			if req.SourceDurationSeconds > 37.0 {
				return nil, fmt.Errorf("source video duration (%.1fs) exceeds maximum allowed for Omni video extension (max 40s total)", req.SourceDurationSeconds)
			}

			if req.Source != nil && strings.TrimSpace(req.Source.InteractionID) != "" {
				isRetainedInteraction = true
				if srcProv != nil && srcProv.Model != "" && !strings.EqualFold(srcProv.Model, targetModel) {
					return nil, fmt.Errorf("video interaction model mismatch: source was created with %q, cannot continue with %q", srcProv.Model, targetModel)
				}
				if srcProv != nil && srcProv.CredentialID != "" && credID != "" && srcProv.CredentialID != credID {
					return nil, fmt.Errorf("video interaction credential mismatch: source used credential %q, active credential is %q", srcProv.CredentialID, credID)
				}
			} else {
				requiresFilesUpload = true
				if req.SourceDurationSeconds > 10.0 {
					return nil, fmt.Errorf("source video duration (%.1fs) exceeds maximum allowed for external video input (10s)", req.SourceDurationSeconds)
				}
				advisoryWarnings = append(advisoryWarnings, "editing/extending uploaded external videos may be restricted in the EU/EEA, UK, and Switzerland; generate the initial video with Gemini Omni Flash in this region")
			}
		} else {
			if opts == nil || !opts.VideoExtensionSupported {
				return nil, fmt.Errorf("model %q does not support video extension", targetModel)
			}
		}

	case pebblestore.VideoOperationCreate:
		if IsOmniModel(targetModel) {
			if req.DurationSeconds > 0 {
				return nil, fmt.Errorf("model %q does not accept duration selection", targetModel)
			}
			resolvedDurationSeconds = 0
		}
	}

	// 8. Aspect Ratio & Resolution defaults / validation if not already resolved
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

	// 9. Initial image capability check for create
	if hasImage {
		if opts == nil || !opts.InitialImageSupported {
			return nil, fmt.Errorf("model %q does not support initial image input", targetModel)
		}
	}

	// 10. Transport Resolution
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

	// 11. Cost estimation
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
	if srcProv != nil {
		sourceExtCount = srcProv.ExtensionCount
		if srcProv.SourceLink != nil {
			sourceLink = srcProv.SourceLink.Clone()
		}
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


