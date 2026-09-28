package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/imagegen"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videogen"
)

const (
	mediaKindImage           = "image_generation"
	mediaKindTranscription   = "video_understanding"
	mediaKindVideoGeneration = "video_generation"
	mediaKindVideoIteration  = "video_iteration"
	mediaKindAudioGeneration = "audio_generation"

	DefaultVideoGenerationModel = "veo-3.1-generate-preview"
	DefaultVideoIterationModel  = "gemini-omni-1.1-flash"
	DefaultAudioGenerationModel = "lyria-3.5"
)

type MediaOptionVariant struct {
	Mode            string         `json:"mode,omitempty"`
	SupportedValues []any          `json:"supported_values,omitempty"`
	Conditions      map[string]any `json:"conditions,omitempty"`
	Notes           string         `json:"notes,omitempty"`
}

type MediaOptionSetting struct {
	Status                   string               `json:"status,omitempty"`
	DefaultValue             any                  `json:"default_value,omitempty"`
	SupportedValues          []any                `json:"supported_values,omitempty"`
	ProviderDocumentedValues []any                `json:"provider_documented_values,omitempty"`
	Variants                 []MediaOptionVariant `json:"variants,omitempty"`
	Notes                    string               `json:"notes,omitempty"`
}

type MediaFeatureOption struct {
	Status     string         `json:"status,omitempty"`
	Supported  bool           `json:"supported"`
	MaxInputs  int            `json:"max_inputs,omitempty"`
	Conditions map[string]any `json:"conditions,omitempty"`
	Notes      string         `json:"notes,omitempty"`
}

type MediaInitialImageOption struct {
	Supported          bool     `json:"supported"`
	MaxInputs          int      `json:"max_inputs,omitempty"`
	SupportedMimeTypes []string `json:"supported_mime_types,omitempty"`
	Notes              string   `json:"notes,omitempty"`
}

type mediaCatalogGenerationOptions struct {
	AspectRatios        []string                      `json:"aspect_ratios,omitempty"`
	Resolutions         []string                      `json:"resolutions,omitempty"`
	Durations           []int                         `json:"durations,omitempty"`
	DefaultRatio        string                        `json:"default_ratio,omitempty"`
	DefaultRes          string                        `json:"default_resolution,omitempty"`
	DefaultDur          int                           `json:"default_duration,omitempty"`
	MaxOutputs          int                           `json:"max_outputs,omitempty"`
	ResolutionDurations map[string][]int              `json:"resolution_durations,omitempty"`
	InitialImage        *MediaInitialImageOption      `json:"initial_image,omitempty"`
	Settings            map[string]MediaOptionSetting       `json:"settings,omitempty"`
	Features            map[string]MediaFeatureOption       `json:"features,omitempty"`
	Constraints         *videogen.VideoOperationConstraints `json:"constraints,omitempty"`
}

type mediaCatalogOption struct {
	ID                string                              `json:"id"`
	Provider          string                              `json:"provider"`
	Model             string                              `json:"model"`
	DisplayName       string                              `json:"display_name"`
	Kind              string                              `json:"kind"`
	Ready             bool                                `json:"ready"`
	Reason            string                              `json:"reason,omitempty"`
	Pricing           json.RawMessage                     `json:"pricing,omitempty"`
	GenerationOptions *mediaCatalogGenerationOptions      `json:"generation_options,omitempty"`
	Constraints       *videogen.VideoOperationConstraints `json:"constraints,omitempty"`
}

type mediaCatalogResponse struct {
	ImageModels           []mediaCatalogOption `json:"image_models"`
	TranscriptionModels   []mediaCatalogOption `json:"transcription_models"`
	VideoGenerationModels []mediaCatalogOption `json:"video_generation_models"`
	VideoIterationModels  []mediaCatalogOption `json:"video_iteration_models"`
	VideoModels           []mediaCatalogOption `json:"video_models"`
	AudioModels           []mediaCatalogOption `json:"audio_models"`
	DefaultImageModel     string               `json:"default_image_model,omitempty"`
	DefaultVideoModel     string               `json:"default_video_model,omitempty"`
	DefaultAudioModel     string               `json:"default_audio_model,omitempty"`
	VideoReady            bool                 `json:"video_ready"`
	VideoStatus           string               `json:"video_status"`
	AudioReady            bool                 `json:"audio_ready"`
	AudioStatus           string               `json:"audio_status"`
}

func (s *Server) handleMediaSettingsCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if s.model == nil || s.imageGen == nil {
		writeError(w, http.StatusInternalServerError, errors.New("media catalog services are not configured"))
		return
	}
	caps, err := s.imageGen.Capabilities(identity.ContextWithPrincipal(r.Context(), principal))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	openRouterStatus := imagegen.ProviderStatus{ID: "openrouter", Label: "OpenRouter"}
	if s.auth != nil && strings.TrimSpace(principal.AccountScopeID) != "" {
		if creds, err := s.auth.ListCredentialsForAccount(principal.AccountScopeID, "openrouter", "", 1); err == nil && creds.Total > 0 {
			openRouterStatus.Ready = true
		} else {
			openRouterStatus.Ready = false
			openRouterStatus.Reason = "connect an OpenRouter API key to enable OpenRouter video models"
		}
	}
	response, err := s.mediaCatalogResponse(caps, openRouterStatus)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	var defaultImage, defaultVideo, defaultAudio string
	if s.uiSettings != nil && strings.TrimSpace(principal.AccountScopeID) != "" {
		if uiSet, err := s.uiSettings.GetForAccount(principal.AccountScopeID); err == nil {
			defaultImage = strings.TrimSpace(uiSet.Tools.Image.DefaultModel)
			defaultVideo = strings.TrimSpace(uiSet.Tools.Video.DefaultModel)
			defaultAudio = strings.TrimSpace(uiSet.Tools.Audio.DefaultModel)
		}
	}
	if defaultImage == "" {
		for _, m := range response.ImageModels {
			if m.Ready && m.Provider == "google" {
				defaultImage = m.ID
				break
			}
		}
		if defaultImage == "" {
			for _, m := range response.ImageModels {
				if m.Ready {
					defaultImage = m.ID
					break
				}
			}
		}
		if defaultImage == "" && len(response.ImageModels) > 0 {
			defaultImage = response.ImageModels[0].ID
		}
	}
	// Video defaults are persisted account choices, never catalog suggestions.
	// Keep an absent default empty, matching videogen's fail-closed resolution.
	if defaultAudio == "" {
		for _, m := range response.AudioModels {
			if m.ID == DefaultAudioGenerationModel && m.Ready {
				defaultAudio = m.ID
				break
			}
		}
		if defaultAudio == "" {
			for _, m := range response.AudioModels {
				if m.Ready {
					defaultAudio = m.ID
					break
				}
			}
		}
		if defaultAudio == "" {
			defaultAudio = DefaultAudioGenerationModel
		}
	}
	response.DefaultImageModel = defaultImage
	response.DefaultVideoModel = defaultVideo
	response.DefaultAudioModel = defaultAudio
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) mediaCatalogResponse(caps imagegen.Capabilities, extraStatuses ...imagegen.ProviderStatus) (mediaCatalogResponse, error) {
	response := mediaCatalogResponse{VideoReady: false, VideoStatus: "coming_soon", AudioReady: false, AudioStatus: "coming_soon"}
	providerStatus := make(map[string]imagegen.ProviderStatus, len(caps.Providers)+len(extraStatuses))
	for _, status := range caps.Providers {
		providerStatus[status.ID] = status
	}
	for _, status := range extraStatuses {
		providerStatus[status.ID] = status
	}
	for _, selection := range imagegen.HardcodedModelSelections() {
		status := providerStatus[selection.Provider]
		option := mediaCatalogOption{
			ID: selection.ID, Provider: selection.Provider, Model: selection.Model, Kind: mediaKindImage,
			Ready: status.Ready, Reason: status.Reason, DisplayName: selection.DisplayName,
		}
		lookup, err := s.model.GetCatalog("codex", selection.Model)
		if err != nil {
			return mediaCatalogResponse{}, err
		}
		if lookup.Found {
			option.Pricing = cloneMediaPricing(lookup.Record.Pricing)
			option.GenerationOptions = extractModelGenerationOptions(lookup.Record)
		}
		response.ImageModels = append(response.ImageModels, option)
	}
	googleSelections, err := s.imageGen.GoogleImageModelSelections()
	if err != nil {
		return mediaCatalogResponse{}, err
	}
	googleStatus := providerStatus[imagegen.ProviderGoogleGemini]
	for _, selection := range googleSelections {
		lookup, err := s.model.GetCatalog("google", selection.Model)
		if err != nil {
			return mediaCatalogResponse{}, err
		}
		option := mediaCatalogOption{
			ID: selection.ID, Provider: selection.Provider, Model: selection.Model, Kind: mediaKindImage,
			Ready: googleStatus.Ready, Reason: googleStatus.Reason, DisplayName: selection.DisplayName,
		}
		if lookup.Found {
			option.Pricing = cloneMediaPricing(lookup.Record.Pricing)
			option.GenerationOptions = extractModelGenerationOptions(lookup.Record)
		}
		response.ImageModels = append(response.ImageModels, option)
	}

	records, err := s.model.ListCatalog("google", 2000)
	if err != nil {
		return mediaCatalogResponse{}, err
	}
	for _, record := range records {
		if !isGoogleVideoTranscriptionCatalogRecord(record) {
			continue
		}
		response.TranscriptionModels = append(response.TranscriptionModels, mediaCatalogOption{
			ID: record.Model, Provider: "google", Model: record.Model,
			DisplayName: firstMediaDisplayName(record.DisplayName, record.Model), Kind: mediaKindTranscription,
			Ready: googleStatus.Ready, Reason: googleStatus.Reason, Pricing: cloneMediaPricing(record.Pricing),
			GenerationOptions: extractModelGenerationOptions(record),
		})
	}
	sort.Slice(response.TranscriptionModels, func(i, j int) bool {
		return response.TranscriptionModels[i].DisplayName < response.TranscriptionModels[j].DisplayName
	})

	// Google video and audio generation models
	for _, record := range records {
		if isVideoOutputCatalogRecord(record) {
			displayName := firstMediaDisplayName(record.DisplayName, record.Model)
			baseOption := mediaCatalogOption{
				ID:                record.Model,
				Provider:          "google",
				Model:             record.Model,
				DisplayName:       displayName,
				Ready:             googleStatus.Ready,
				Reason:            googleStatus.Reason,
				Pricing:           cloneMediaPricing(record.Pricing),
				GenerationOptions: extractModelGenerationOptions(record),
				Constraints:       videogen.BuildVideoOperationConstraints("google", record.Model, record),
			}
			if baseOption.GenerationOptions != nil && baseOption.Constraints != nil {
				baseOption.GenerationOptions.Constraints = baseOption.Constraints
			}
			if isVideoGenerationCatalogRecord(record) {
				genOption := baseOption
				genOption.Kind = mediaKindVideoGeneration
				response.VideoGenerationModels = append(response.VideoGenerationModels, genOption)
			}
			if isVideoIterationCatalogRecord(record) {
				iterOption := baseOption
				iterOption.Kind = mediaKindVideoIteration
				response.VideoIterationModels = append(response.VideoIterationModels, iterOption)
			}
			response.VideoModels = append(response.VideoModels, baseOption)
		}
		if isAudioGenerationCatalogRecord(record) {
			displayName := firstMediaDisplayName(record.DisplayName, record.Model)
			response.AudioModels = append(response.AudioModels, mediaCatalogOption{
				ID:                record.Model,
				Provider:          "google",
				Model:             record.Model,
				DisplayName:       displayName,
				Kind:              mediaKindAudioGeneration,
				Ready:             googleStatus.Ready,
				Reason:            googleStatus.Reason,
				Pricing:           cloneMediaPricing(record.Pricing),
				GenerationOptions: extractModelGenerationOptions(record),
			})
		}
	}

	// OpenRouter video and audio models
	openRouterStatus := providerStatus["openrouter"]
	openRouterRecords, _ := s.model.ListCatalog("openrouter", 2000)
	for _, record := range openRouterRecords {
		if isVideoOutputCatalogRecord(record) {
			displayName := firstMediaDisplayName(record.DisplayName, record.Model)
			baseOption := mediaCatalogOption{
				ID:                record.Model,
				Provider:          "openrouter",
				Model:             record.Model,
				DisplayName:       displayName,
				Ready:             openRouterStatus.Ready,
				Reason:            openRouterStatus.Reason,
				Pricing:           cloneMediaPricing(record.Pricing),
				GenerationOptions: extractModelGenerationOptions(record),
				Constraints:       videogen.BuildVideoOperationConstraints("openrouter", record.Model, record),
			}
			if baseOption.GenerationOptions != nil && baseOption.Constraints != nil {
				baseOption.GenerationOptions.Constraints = baseOption.Constraints
			}
			if isVideoGenerationCatalogRecord(record) {
				genOption := baseOption
				genOption.Kind = mediaKindVideoGeneration
				response.VideoGenerationModels = append(response.VideoGenerationModels, genOption)
			}
			response.VideoModels = append(response.VideoModels, baseOption)
		}
		if isAudioGenerationCatalogRecord(record) {
			displayName := firstMediaDisplayName(record.DisplayName, record.Model)
			response.AudioModels = append(response.AudioModels, mediaCatalogOption{
				ID:                record.Model,
				Provider:          "openrouter",
				Model:             record.Model,
				DisplayName:       displayName,
				Kind:              mediaKindAudioGeneration,
				Ready:             openRouterStatus.Ready,
				Reason:            openRouterStatus.Reason,
				Pricing:           cloneMediaPricing(record.Pricing),
				GenerationOptions: extractModelGenerationOptions(record),
			})
		}
	}

	sortVideoModels := func(models []mediaCatalogOption) {
		sort.Slice(models, func(i, j int) bool {
			prioI := videoModelPriority(models[i])
			prioJ := videoModelPriority(models[j])
			if prioI != prioJ {
				return prioI < prioJ
			}
			return models[i].DisplayName < models[j].DisplayName
		})
	}
	sortVideoModels(response.VideoGenerationModels)
	sortVideoModels(response.VideoIterationModels)
	sortVideoModels(response.VideoModels)

	sortAudioModels := func(models []mediaCatalogOption) {
		sort.Slice(models, func(i, j int) bool {
			prioI := audioModelPriority(models[i])
			prioJ := audioModelPriority(models[j])
			if prioI != prioJ {
				return prioI < prioJ
			}
			return models[i].DisplayName < models[j].DisplayName
		})
	}
	sortAudioModels(response.AudioModels)

	if len(response.AudioModels) > 0 {
		if googleStatus.Ready {
			response.AudioReady = true
			response.AudioStatus = "ready"
		} else {
			response.AudioReady = false
			response.AudioStatus = "needs_auth"
		}
	}

	if len(response.VideoGenerationModels) > 0 || len(response.VideoIterationModels) > 0 {
		if googleStatus.Ready || openRouterStatus.Ready {
			response.VideoReady = true
			response.VideoStatus = "ready"
		} else {
			response.VideoReady = false
			response.VideoStatus = "needs_auth"
		}
	}

	return response, nil
}

func videoModelPriority(option mediaCatalogOption) int {
	switch option.Model {
	case "veo-3.1-generate-preview":
		return 1
	case "veo-3.1-fast-generate-preview":
		return 2
	case "veo-3.1-lite-generate-preview":
		return 3
	case "gemini-omni-1.1-flash":
		return 4
	case "gemini-omni-flash-preview":
		return 5
	case "google/veo-3.1":
		return 20
	case "google/veo-3.1-fast":
		return 21
	case "google/veo-3.1-lite":
		return 22
	}
	if option.Provider == "google" {
		return 10
	}
	return 30
}

func audioModelPriority(option mediaCatalogOption) int {
	switch option.Model {
	case "lyria-3.5":
		return 1
	case "lyria-3-clip-preview":
		return 2
	case "lyria-3-pro-preview":
		return 3
	case "lyria-realtime-exp":
		return 4
	case "google/lyria-3.5":
		return 20
	case "google/lyria-3-clip-preview":
		return 21
	case "google/lyria-3-pro-preview":
		return 22
	}
	if option.Provider == "google" {
		return 10
	}
	return 30
}

func isAudioGenerationCatalogRecord(record pebblestore.ModelCatalogRecord) bool {
	if containsMediaModality(record.CatalogModalities.Outputs, "video") {
		return false
	}
	joined := strings.ToLower(strings.Join([]string{record.Model, record.DisplayName, record.CatalogID, strings.Join(record.CatalogModalities.Categories, " ")}, " "))
	if !containsMediaModality(record.CatalogModalities.Outputs, "audio") &&
		!containsMediaModality(record.CatalogModalities.Categories, "audio_generation") &&
		!strings.Contains(joined, "lyria") {
		return false
	}
	for _, excluded := range []string{"embedding", "robot", "research", "live-translate", "transcription"} {
		if strings.Contains(joined, excluded) {
			return false
		}
	}
	return true
}

func isVideoOutputCatalogRecord(record pebblestore.ModelCatalogRecord) bool {
	if !containsMediaModality(record.CatalogModalities.Outputs, "video") {
		return false
	}
	joined := strings.ToLower(strings.Join([]string{record.Model, record.DisplayName, record.CatalogID, strings.Join(record.CatalogModalities.Categories, " ")}, " "))
	for _, excluded := range []string{"embedding", "robot", "research", "live"} {
		if strings.Contains(joined, excluded) {
			return false
		}
	}
	return true
}

func isVideoGenerationCatalogRecord(record pebblestore.ModelCatalogRecord) bool {
	return isVideoOutputCatalogRecord(record)
}

func isVideoIterationCatalogRecord(record pebblestore.ModelCatalogRecord) bool {
	if !isVideoOutputCatalogRecord(record) {
		return false
	}
	providerID := strings.ToLower(strings.TrimSpace(record.Provider))
	vOpts := videogen.ExtractVideoOptions(record)
	return videogen.SupportsVideoIteration(providerID, record.Model, vOpts)
}

func isGoogleVideoTranscriptionCatalogRecord(record pebblestore.ModelCatalogRecord) bool {
	if !strings.EqualFold(strings.TrimSpace(record.Provider), "google") ||
		!containsMediaModality(record.CatalogModalities.Inputs, "video") ||
		!containsMediaModality(record.CatalogModalities.Outputs, "text") {
		return false
	}
	joined := strings.ToLower(strings.Join([]string{record.Model, record.DisplayName, record.CatalogID, strings.Join(record.CatalogModalities.Categories, " ")}, " "))
	for _, excluded := range []string{"embedding", "veo", "robot", "research", "live", "imagen"} {
		if strings.Contains(joined, excluded) {
			return false
		}
	}
	return true
}

func containsMediaModality(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), wanted) {
			return true
		}
	}
	return false
}

func firstMediaDisplayName(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return "Model"
}

func cloneMediaPricing(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func containsAnyStringFold(slice []any, val string) bool {
	for _, item := range slice {
		if s, ok := item.(string); ok && strings.EqualFold(strings.TrimSpace(s), strings.TrimSpace(val)) {
			return true
		}
	}
	return false
}

func extractModelGenerationOptions(record pebblestore.ModelCatalogRecord) *mediaCatalogGenerationOptions {
	if len(record.ProviderSpecific) == 0 {
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(record.ProviderSpecific, &raw); err != nil {
		return nil
	}
	providerKey := strings.ToLower(strings.TrimSpace(record.Provider))
	provData, ok := raw[providerKey]
	if !ok || len(provData) == 0 {
		// Strict exact provider matching: never fall back to an arbitrary first provider key
		return nil
	}

	type rawSetting struct {
		Status                   string `json:"status"`
		DefaultValue             any    `json:"default_value"`
		SupportedValues          []any  `json:"supported_values"`
		ProviderDocumentedValues []any  `json:"provider_documented_values"`
		Variants                 []struct {
			Mode            string         `json:"mode"`
			SupportedValues []any          `json:"supported_values"`
			Conditions      map[string]any `json:"conditions"`
			Notes           string         `json:"notes"`
		} `json:"variants"`
		Notes string `json:"notes"`
	}

	type rawFeature struct {
		Status     string         `json:"status"`
		Supported  bool           `json:"supported"`
		MaxInputs  *int           `json:"max_inputs"`
		Conditions map[string]any `json:"conditions"`
		Notes      string         `json:"notes"`
	}

	var parsed struct {
		ImageGeneration *struct {
			Status   string                `json:"status"`
			Settings map[string]rawSetting `json:"settings"`
			Features map[string]rawFeature `json:"features"`
		} `json:"image_generation"`
		VideoGeneration *struct {
			Status   string                `json:"status"`
			Settings map[string]rawSetting `json:"settings"`
			Features map[string]rawFeature `json:"features"`
		} `json:"video_generation"`
		MusicGeneration *struct {
			Status   string                `json:"status"`
			Settings map[string]rawSetting `json:"settings"`
			Features map[string]rawFeature `json:"features"`
		} `json:"music_generation"`
		Settings map[string]rawSetting `json:"settings"`
		Features map[string]rawFeature `json:"features"`
	}
	if err := json.Unmarshal(provData, &parsed); err != nil {
		return nil
	}

	isVideo := containsStringFold(record.CatalogModalities.Outputs, "video") ||
		containsStringFold(record.CatalogModalities.Categories, "video_generation") ||
		containsStringFold(record.CatalogModalities.Categories, "video_iteration")
	isAudio := containsStringFold(record.CatalogModalities.Outputs, "audio") ||
		containsStringFold(record.CatalogModalities.Categories, "audio_generation")
	isImage := containsStringFold(record.CatalogModalities.Outputs, "image") ||
		containsStringFold(record.CatalogModalities.Categories, "image_generation")

	var settingsMap map[string]rawSetting
	var featuresMap map[string]rawFeature

	switch {
	case isVideo && parsed.VideoGeneration != nil:
		settingsMap = parsed.VideoGeneration.Settings
		featuresMap = parsed.VideoGeneration.Features
	case isAudio && parsed.MusicGeneration != nil:
		settingsMap = parsed.MusicGeneration.Settings
		featuresMap = parsed.MusicGeneration.Features
	case isImage && parsed.ImageGeneration != nil:
		settingsMap = parsed.ImageGeneration.Settings
		featuresMap = parsed.ImageGeneration.Features
	}
	if len(settingsMap) == 0 && len(parsed.Settings) > 0 {
		settingsMap = parsed.Settings
	}
	if len(featuresMap) == 0 && len(parsed.Features) > 0 {
		featuresMap = parsed.Features
	}

	if len(settingsMap) == 0 && len(featuresMap) == 0 {
		return nil
	}

	outSettings := make(map[string]MediaOptionSetting)
	for k, s := range settingsMap {
		var outVariants []MediaOptionVariant
		for _, v := range s.Variants {
			outVariants = append(outVariants, MediaOptionVariant{
				Mode:            v.Mode,
				SupportedValues: v.SupportedValues,
				Conditions:      v.Conditions,
				Notes:           v.Notes,
			})
		}
		outSettings[k] = MediaOptionSetting{
			Status:                   s.Status,
			DefaultValue:             s.DefaultValue,
			SupportedValues:          s.SupportedValues,
			ProviderDocumentedValues: s.ProviderDocumentedValues,
			Variants:                 outVariants,
			Notes:                    s.Notes,
		}
	}

	outFeatures := make(map[string]MediaFeatureOption)
	for k, f := range featuresMap {
		maxIn := 0
		if f.MaxInputs != nil {
			maxIn = *f.MaxInputs
		}
		outFeatures[k] = MediaFeatureOption{
			Status:     f.Status,
			Supported:  f.Supported,
			MaxInputs:  maxIn,
			Conditions: f.Conditions,
			Notes:      f.Notes,
		}
	}

	if isVideo {
		vOpts := videogen.ExtractVideoOptions(record)
		if vOpts == nil {
			return nil
		}
		var initialImage *MediaInitialImageOption
		if vOpts.InitialImageSupported {
			maxIn := 1
			if vOpts.InitialImageMaxInputs > 0 {
				maxIn = vOpts.InitialImageMaxInputs
			}
			initialImage = &MediaInitialImageOption{
				Supported:          true,
				MaxInputs:          maxIn,
				SupportedMimeTypes: []string{"image/png", "image/jpeg"},
			}
		}
		constraints := videogen.BuildVideoOperationConstraints(record.Provider, record.Model, record)
		return &mediaCatalogGenerationOptions{
			AspectRatios:        vOpts.AspectRatios,
			Resolutions:         vOpts.Resolutions,
			Durations:           vOpts.Durations,
			DefaultRatio:        vOpts.DefaultRatio,
			DefaultRes:          vOpts.DefaultRes,
			DefaultDur:          vOpts.DefaultDur,
			MaxOutputs:          8,
			ResolutionDurations: vOpts.ResolutionDurations,
			InitialImage:        initialImage,
			Settings:            outSettings,
			Features:            outFeatures,
			Constraints:         constraints,
		}
	}

	var aspectRatios []string
	var resolutions []string
	var durations []int
	var defaultRatio string
	var defaultRes string
	var defaultDur int

	// Aspect ratio
	if ar, ok := settingsMap["aspect_ratio"]; ok && !strings.EqualFold(ar.Status, "unsupported") {
		for _, v := range ar.SupportedValues {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				aspectRatios = append(aspectRatios, strings.TrimSpace(s))
			}
		}
		if s, ok := ar.DefaultValue.(string); ok {
			defaultRatio = strings.TrimSpace(s)
		}
	}

	// Resolution / image_size
	if res, ok := settingsMap["resolution"]; ok && !strings.EqualFold(res.Status, "unsupported") {
		for _, v := range res.SupportedValues {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				resolutions = append(resolutions, strings.TrimSpace(s))
			}
		}
		if s, ok := res.DefaultValue.(string); ok {
			defaultRes = strings.TrimSpace(s)
		}
	}
	if len(resolutions) == 0 {
		if is, ok := settingsMap["image_size"]; ok && !strings.EqualFold(is.Status, "unsupported") {
			for _, v := range is.SupportedValues {
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					resolutions = append(resolutions, strings.TrimSpace(s))
				}
			}
			if s, ok := is.DefaultValue.(string); ok {
				defaultRes = strings.TrimSpace(s)
			}
		}
	}

	// Duration seconds
	if dur, ok := settingsMap["duration_seconds"]; ok && !strings.EqualFold(dur.Status, "unsupported") && !strings.EqualFold(dur.Status, "unknown") {
		for _, v := range dur.SupportedValues {
			switch n := v.(type) {
			case float64:
				durations = append(durations, int(n))
			case int:
				durations = append(durations, n)
			}
		}
		switch n := dur.DefaultValue.(type) {
		case float64:
			defaultDur = int(n)
		case int:
			defaultDur = n
		}
	} else if dur, ok := settingsMap["duration_seconds"]; ok && (dur.Status == "" || strings.EqualFold(dur.Status, "verified")) {
		for _, v := range dur.SupportedValues {
			switch n := v.(type) {
			case float64:
				durations = append(durations, int(n))
			case int:
				durations = append(durations, n)
			}
		}
		switch n := dur.DefaultValue.(type) {
		case float64:
			defaultDur = int(n)
		case int:
			defaultDur = n
		}
	}

	// Resolution-dependent durations
	var resolutionDurations map[string][]int
	if isVideo && len(resolutions) > 0 && len(durations) > 0 {
		resolutionDurations = make(map[string][]int)
		for _, r := range resolutions {
			rLower := strings.ToLower(r)
			var rDurs []int

			// Check variants in duration_seconds
			if durSetting, ok := settingsMap["duration_seconds"]; ok && len(durSetting.Variants) > 0 {
				for _, v := range durSetting.Variants {
					condRes, _ := v.Conditions["resolution"].(string)
					condResLower := strings.ToLower(condRes)
					matchesRes := condResLower == rLower ||
						((rLower == "1080p" || rLower == "4k") && condResLower == "1080p_or_4k") ||
						(condResLower == "" && len(v.Conditions) == 0 && rLower == "720p")
					if matchesRes {
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

			// Also check variants on resolution
			if len(rDurs) == 0 {
				if resSetting, ok := settingsMap["resolution"]; ok && len(resSetting.Variants) > 0 {
					for _, v := range resSetting.Variants {
						if containsAnyStringFold(v.SupportedValues, r) {
							if condDur, ok := v.Conditions["duration_seconds"]; ok {
								switch n := condDur.(type) {
								case float64:
									rDurs = append(rDurs, int(n))
								case int:
									rDurs = append(rDurs, n)
								}
							}
						}
					}
				}
			}

			if len(rDurs) > 0 {
				resolutionDurations[rLower] = rDurs
			} else {
				resolutionDurations[rLower] = durations
			}
		}
	}

	// Initial image support
	var initialImage *MediaInitialImageOption
	if isVideo {
		if feat, ok := featuresMap["initial_image"]; ok && feat.Supported {
			maxIn := 1
			if feat.MaxInputs != nil && *feat.MaxInputs > 0 {
				maxIn = *feat.MaxInputs
			}
			initialImage = &MediaInitialImageOption{
				Supported:          true,
				MaxInputs:          maxIn,
				SupportedMimeTypes: []string{"image/png", "image/jpeg", "image/webp", "image/heic", "image/heif"},
				Notes:              feat.Notes,
			}
		} else if containsStringFold(record.CatalogModalities.Inputs, "image") {
			initialImage = &MediaInitialImageOption{
				Supported:          true,
				MaxInputs:          1,
				SupportedMimeTypes: []string{"image/png", "image/jpeg", "image/webp", "image/heic", "image/heif"},
				Notes:              "Initial keyframe image input supported",
			}
		}
	}

	maxOutputs := 1
	if isVideo {
		maxOutputs = 8 // Swarm application video clip limit
	} else if isImage {
		maxOutputs = 25
	}

	if len(aspectRatios) == 0 && len(resolutions) == 0 && len(durations) == 0 && initialImage == nil && len(outFeatures) == 0 && len(outSettings) == 0 {
		return nil
	}

	return &mediaCatalogGenerationOptions{
		AspectRatios:        aspectRatios,
		Resolutions:         resolutions,
		Durations:           durations,
		DefaultRatio:        defaultRatio,
		DefaultRes:          defaultRes,
		DefaultDur:          defaultDur,
		MaxOutputs:          maxOutputs,
		ResolutionDurations: resolutionDurations,
		InitialImage:        initialImage,
		Settings:            outSettings,
		Features:            outFeatures,
	}
}
