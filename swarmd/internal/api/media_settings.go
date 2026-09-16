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
)

const (
	mediaKindImage           = "image_generation"
	mediaKindTranscription   = "video_understanding"
	mediaKindVideoGeneration = "video_generation"
	mediaKindVideoIteration  = "video_iteration"

	DefaultVideoGenerationModel = "veo-3.1-generate-preview"
	DefaultVideoIterationModel  = "gemini-omni-1.1-flash"
)

type mediaCatalogOption struct {
	ID          string          `json:"id"`
	Provider    string          `json:"provider"`
	Model       string          `json:"model"`
	DisplayName string          `json:"display_name"`
	Kind        string          `json:"kind"`
	Ready       bool            `json:"ready"`
	Reason      string          `json:"reason,omitempty"`
	Pricing     json.RawMessage `json:"pricing,omitempty"`
}

type mediaCatalogResponse struct {
	ImageModels           []mediaCatalogOption `json:"image_models"`
	TranscriptionModels   []mediaCatalogOption `json:"transcription_models"`
	VideoGenerationModels []mediaCatalogOption `json:"video_generation_models"`
	VideoIterationModels  []mediaCatalogOption `json:"video_iteration_models"`
	VideoModels           []mediaCatalogOption `json:"video_models"`
	VideoReady            bool                 `json:"video_ready"`
	VideoStatus           string               `json:"video_status"`
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
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) mediaCatalogResponse(caps imagegen.Capabilities, extraStatuses ...imagegen.ProviderStatus) (mediaCatalogResponse, error) {
	response := mediaCatalogResponse{VideoReady: false, VideoStatus: "coming_soon"}
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
		})
	}
	sort.Slice(response.TranscriptionModels, func(i, j int) bool {
		return response.TranscriptionModels[i].DisplayName < response.TranscriptionModels[j].DisplayName
	})

	// Google video generation and iteration models
	for _, record := range records {
		if !isVideoOutputCatalogRecord(record) {
			continue
		}
		displayName := firstMediaDisplayName(record.DisplayName, record.Model)
		baseOption := mediaCatalogOption{
			ID:          record.Model,
			Provider:    "google",
			Model:       record.Model,
			DisplayName: displayName,
			Ready:       googleStatus.Ready,
			Reason:      googleStatus.Reason,
			Pricing:     cloneMediaPricing(record.Pricing),
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

	// OpenRouter video generation models
	openRouterStatus := providerStatus["openrouter"]
	openRouterRecords, _ := s.model.ListCatalog("openrouter", 2000)
	for _, record := range openRouterRecords {
		if !isVideoOutputCatalogRecord(record) {
			continue
		}
		displayName := firstMediaDisplayName(record.DisplayName, record.Model)
		baseOption := mediaCatalogOption{
			ID:          record.Model,
			Provider:    "openrouter",
			Model:       record.Model,
			DisplayName: displayName,
			Ready:       openRouterStatus.Ready,
			Reason:      openRouterStatus.Reason,
			Pricing:     cloneMediaPricing(record.Pricing),
		}
		if isVideoGenerationCatalogRecord(record) {
			genOption := baseOption
			genOption.Kind = mediaKindVideoGeneration
			response.VideoGenerationModels = append(response.VideoGenerationModels, genOption)
		}
		response.VideoModels = append(response.VideoModels, baseOption)
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
	if strings.EqualFold(strings.TrimSpace(record.Provider), "google") {
		if strings.Contains(strings.ToLower(record.Model), "omni") {
			return true
		}
		var ps struct {
			Google struct {
				VideoGeneration struct {
					Features struct {
						ConversationalEditing struct {
							Supported bool `json:"supported"`
						} `json:"conversational_editing"`
					} `json:"features"`
				} `json:"video_generation"`
			} `json:"google"`
		}
		if err := json.Unmarshal(record.ProviderSpecific, &ps); err == nil {
			if ps.Google.VideoGeneration.Features.ConversationalEditing.Supported {
				return true
			}
		}
	}
	return false
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
