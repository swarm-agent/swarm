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
	mediaKindImage         = "image_generation"
	mediaKindTranscription = "video_understanding"
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
	ImageModels         []mediaCatalogOption `json:"image_models"`
	TranscriptionModels []mediaCatalogOption `json:"transcription_models"`
	VideoReady          bool                 `json:"video_ready"`
	VideoStatus         string               `json:"video_status"`
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
	response, err := s.mediaCatalogResponse(caps)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) mediaCatalogResponse(caps imagegen.Capabilities) (mediaCatalogResponse, error) {
	response := mediaCatalogResponse{VideoReady: false, VideoStatus: "coming_soon"}
	providerStatus := make(map[string]imagegen.ProviderStatus, len(caps.Providers))
	for _, status := range caps.Providers {
		providerStatus[status.ID] = status
	}
	for _, selectionID := range imagegen.SupportedModelSelectionIDs() {
		selection, err := imagegen.ResolveModelSelection(selectionID)
		if err != nil {
			return mediaCatalogResponse{}, err
		}
		catalogProvider := imageCatalogProvider(selection.Provider)
		lookup, err := s.model.GetCatalog(catalogProvider, selection.Model)
		if err != nil {
			return mediaCatalogResponse{}, err
		}
		status := providerStatus[selection.Provider]
		option := mediaCatalogOption{
			ID: selection.ID, Provider: selection.Provider, Model: selection.Model, Kind: mediaKindImage,
			Ready: status.Ready, Reason: status.Reason, DisplayName: selection.DisplayName,
		}
		if lookup.Found {
			option.DisplayName = firstMediaDisplayName(lookup.Record.DisplayName, option.DisplayName, selection.Model)
			option.Pricing = cloneMediaPricing(lookup.Record.Pricing)
		}
		response.ImageModels = append(response.ImageModels, option)
	}

	records, err := s.model.ListCatalog("google", 2000)
	if err != nil {
		return mediaCatalogResponse{}, err
	}
	googleStatus := providerStatus[imagegen.ProviderGoogleGemini]
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
	return response, nil
}

func imageCatalogProvider(provider string) string {
	switch provider {
	case imagegen.ProviderCodexOpenAI:
		return "codex"
	case imagegen.ProviderGoogleGemini:
		return "google"
	default:
		return provider
	}
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
