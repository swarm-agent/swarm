package api

import (
	"context"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/imagegen"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// A media request without a model uses the account's default for its kind
// (swarm_set_media_models), and without one the first ready model Swarm
// recommends. The model is fixed on the task when it is created, so the
// checks, the generation and the result all name the same model.

// mediaDefaultPreference is the order Swarm recommends models in, per kind;
// the first ready one with usable settings wins.
var mediaDefaultPreference = map[string][]string{
	"image": {"gemini-nano-banana-2.1", "gemini-3.1-flash-image", "gemini-3-pro-image", "gemini-3.1-flash-lite-image", imagegen.DefaultModelSelectionID},
	// Omni is Google's lasting video model (the Veo 3.1 previews are retired
	// by Google); Veo stays one model choice away.
	"video": {"gemini-omni-1.1-flash", "veo-3.1-fast-generate-preview", "veo-3.1-generate-preview", "google/veo-3.1-fast", "google/veo-3.1"},
}

// mediaCatalogFor is the media model catalog as the principal sees it: which
// models exist, which are ready, and each one's settings.
func (s *Server) mediaCatalogFor(ctx context.Context, principal identity.Principal) (mediaCatalogResponse, error) {
	caps, err := s.imageGen.Capabilities(identity.ContextWithPrincipal(ctx, principal))
	if err != nil {
		return mediaCatalogResponse{}, err
	}
	openRouterStatus := imagegen.ProviderStatus{ID: "openrouter", Label: "OpenRouter"}
	if s.auth != nil && strings.TrimSpace(principal.AccountScopeID) != "" {
		if creds, err := s.auth.ListCredentialsForAccount(principal.AccountScopeID, "openrouter", "", 1); err == nil && creds.Total > 0 {
			openRouterStatus.Ready = true
		} else {
			openRouterStatus.Reason = "connect an OpenRouter API key to enable OpenRouter video models"
		}
	}
	return s.mediaCatalogResponse(caps, openRouterStatus)
}

// mediaAccountDefault is the account's chosen default model for a kind.
func (s *Server) mediaAccountDefault(principal identity.Principal, kind string) string {
	if s.uiSettings == nil || strings.TrimSpace(principal.AccountScopeID) == "" {
		return ""
	}
	settings, err := s.uiSettings.GetForAccount(principal.AccountScopeID)
	if err != nil {
		return ""
	}
	switch kind {
	case "image":
		return strings.TrimSpace(settings.Tools.Image.DefaultModel)
	case "video":
		return strings.TrimSpace(settings.Tools.Video.DefaultModel)
	case "sound", "audio":
		return strings.TrimSpace(settings.Tools.Audio.DefaultModel)
	}
	return ""
}

// recommendedMediaModel is the first ready model of the preference list that
// has the settings Swarm checks requests against. Sound has no fixed default:
// Swarm picks the clip or the song model from the requested length.
func recommendedMediaModel(catalog mediaCatalogResponse, kind string) string {
	var options []mediaCatalogOption
	switch kind {
	case "image":
		options = catalog.ImageModels
	case "video":
		options = catalog.VideoGenerationModels
	default:
		return ""
	}
	usable := map[string]bool{}
	for _, option := range options {
		if !option.Ready {
			continue
		}
		if kind == "image" && option.ID != imagegen.DefaultModelSelectionID && (option.GenerationOptions == nil || len(option.GenerationOptions.AspectRatios) == 0) {
			continue
		}
		if kind == "video" && (option.GenerationOptions == nil || len(option.GenerationOptions.Resolutions) == 0) {
			continue
		}
		usable[option.ID] = true
	}
	for _, id := range mediaDefaultPreference[kind] {
		if usable[id] {
			return id
		}
	}
	for _, option := range options {
		if usable[option.ID] {
			return option.ID
		}
	}
	return ""
}

// fillMediaTaskModel fixes a direct media task's model when the request named
// none. It leaves the model empty when nothing is ready, so the request fails
// with the provider's own reason.
func (s *Server) fillMediaTaskModel(ctx context.Context, principal identity.Principal, task *pebblestore.ProjectTaskRecord) {
	if task == nil || strings.TrimSpace(task.Model) != "" {
		return
	}
	task.Model = s.defaultMediaModel(ctx, principal, task.Agent)
}

// defaultMediaModel is the model an image or video request without one uses:
// the account's default, else the recommended ready model; empty for other
// kinds or when nothing is ready.
func (s *Server) defaultMediaModel(ctx context.Context, principal identity.Principal, kind string) string {
	if kind != "image" && kind != "video" {
		return ""
	}
	if model := s.mediaAccountDefault(principal, kind); model != "" {
		return model
	}
	if s.imageGen == nil || s.model == nil {
		return ""
	}
	catalog, err := s.mediaCatalogFor(ctx, principal)
	if err != nil {
		return ""
	}
	return recommendedMediaModel(catalog, kind)
}
