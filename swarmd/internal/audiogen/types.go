package audiogen

import (
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	ProviderGoogleGemini = "google"

	ModelLyriaClip = "lyria-3-clip-preview"
	ModelLyriaSong = "lyria-3.5"
	ModelLyriaPro  = "lyria-3-pro-preview"

	DefaultAudioClipModel = ModelLyriaClip
	DefaultAudioSongModel = ModelLyriaSong

	ClipDurationLimitSeconds   = 30
	DefaultClipDurationSeconds = 30
	DefaultSongDurationSeconds = 120

	DefaultAudioMIMEType = "audio/mp3"

	managedAudioMaxBytes            = 50 << 20
	managedAudioMaxParallelRequests = 4

	defaultGoogleBaseURL = "https://generativelanguage.googleapis.com"
)

// ModelCatalog provides access to cached provider model records and pricing.
type ModelCatalog interface {
	ListCatalog(providerID string, limit int) ([]pebblestore.ModelCatalogRecord, error)
}

// ManagedAudioDurationCapability defines supported duration ranges and presets.
type ManagedAudioDurationCapability struct {
	DefaultValue    int    `json:"default_value"`
	MinSeconds      int    `json:"min_seconds"`
	MaxSeconds      int    `json:"max_seconds"`
	SupportedValues []int  `json:"supported_values,omitempty"`
	Notes           string `json:"notes,omitempty"`
}

// ManagedAudioCapabilities details what the configured audio model can do.
type ManagedAudioCapabilities struct {
	Available       bool                           `json:"available"`
	Reason          string                         `json:"reason,omitempty"`
	Model           string                         `json:"model"`
	Provider        string                         `json:"provider"`
	DisplayName     string                         `json:"display_name"`
	Kind            string                         `json:"kind"` // "clip" or "full_song"
	DurationSeconds ManagedAudioDurationCapability `json:"duration_seconds"`
	Features        map[string]any                 `json:"features,omitempty"`
	Billing         map[string]any                 `json:"billing,omitempty"`
	CapabilityToken string                         `json:"capability_token,omitempty"`
}

// ManagedAudioRequest specifies audio generation or iteration parameters.
type ManagedAudioRequest struct {
	Prompt                string
	DurationSeconds       int
	Principal             identity.Principal
	Model                 string
	Source                *ManagedAudioSource
	Image                 *ManagedAudioImage
	NegativePrompt        string
	TargetDurationSeconds float64
	FadeOutSeconds        float64
	CapabilityToken       string
}

// ManagedAudioSource specifies an existing audio track or prior interaction for iteration.
type ManagedAudioSource struct {
	Bytes         []byte
	MediaType     string
	InteractionID string
	Model         string
}

// ManagedAudioImage provides image inspiration for multimodal music generation.
type ManagedAudioImage struct {
	Bytes     []byte
	MediaType string
}

// AudioMetadata carries prompt, duration, model, and arrangement context with generated clips.
type AudioMetadata struct {
	Prompt                string  `json:"prompt"`
	ArrangementPrompt     string  `json:"arrangement_prompt,omitempty"`
	TargetDurationSeconds float64 `json:"target_duration_seconds,omitempty"`
	ActualDurationMs      int     `json:"actual_duration_ms,omitempty"`
	Model                 string  `json:"model"`
	Provider              string  `json:"provider"`
	InteractionID         string  `json:"interaction_id,omitempty"`
	PreviousInteractionID string  `json:"previous_interaction_id,omitempty"`
	Lyrics                string  `json:"lyrics,omitempty"`
	HasImageInspiration   bool    `json:"has_image_inspiration,omitempty"`
	Trimmed               bool    `json:"trimmed,omitempty"`
}

// ManagedAudioResult contains the generated or edited audio payload and metadata.
type ManagedAudioResult struct {
	Bytes            []byte
	MediaType        string
	InteractionID    string
	Model            string
	Provider         string
	DurationMs       int
	EstimatedCostUSD float64
	PricingSummary   string
	Lyrics           string
	Metadata         AudioMetadata
}
