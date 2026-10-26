package videotranscription

import (
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// NormalizeTranscriptionModel accepts bare IDs and Google-qualified settings.
// Empty settings remain empty so callers can retain their own default policy.
func NormalizeTranscriptionModel(setting string) (string, error) {
	model := strings.TrimSpace(setting)
	if provider, id, qualified := strings.Cut(model, "/"); qualified {
		if !strings.EqualFold(strings.TrimSpace(provider), "google") {
			return "", fmt.Errorf("transcription model %q has provider %q; provider must be google", setting, provider)
		}
		model = strings.TrimSpace(id)
		if model == "" || strings.Contains(model, "/") {
			return "", fmt.Errorf("invalid Google transcription model %q", setting)
		}
	}
	return model, nil
}

// ResolveTranscriptionModel resolves a setting against the Google catalog with
// caller-owned input requirements and the shared text-output requirement.
func ResolveTranscriptionModel(setting string, records []pebblestore.ModelCatalogRecord, input string) (pebblestore.ModelCatalogRecord, error) {
	model, err := NormalizeTranscriptionModel(setting)
	if err != nil {
		return pebblestore.ModelCatalogRecord{}, err
	}
	for _, record := range records {
		if record.Model != model {
			continue
		}
		if record.Provider != "" && !strings.EqualFold(record.Provider, "google") {
			return pebblestore.ModelCatalogRecord{}, fmt.Errorf("transcription model %q has provider %q; provider must be google", model, record.Provider)
		}
		if !containsFold(record.CatalogModalities.Inputs, input) {
			return pebblestore.ModelCatalogRecord{}, fmt.Errorf("transcription model %q lacks %s input modality", model, input)
		}
		if !containsFold(record.CatalogModalities.Outputs, "text") {
			return pebblestore.ModelCatalogRecord{}, fmt.Errorf("transcription model %q lacks text output modality", model)
		}
		return record, nil
	}
	return pebblestore.ModelCatalogRecord{}, fmt.Errorf("transcription model %q is absent from the current Google catalog (requires %s input and text output modalities)", model, input)
}
