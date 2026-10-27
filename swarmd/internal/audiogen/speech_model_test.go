package audiogen

import (
	"strings"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/uisettings"
	"testing"
)

// Purpose: SpeechAlignmentModel must share transcription-setting normalization
// while requiring audio, not video. This service test rejects foreign providers
// and missing audio/text before dispatch, without needing provider credentials.
func TestSpeechAlignmentModelResolution(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	settings := uisettings.NewService(pebblestore.NewUISettingsStore(db))
	const id = "gemini-3.8-flash"
	for _, selection := range []string{id, "google/" + id, "openai/" + id} {
		for _, missing := range []string{"", "audio", "text"} {
			t.Run(selection+"/missing-"+missing, func(t *testing.T) {
				ui, err := settings.GetForAccount("account")
				if err != nil {
					t.Fatal(err)
				}
				ui.Media.TranscriptionModel = selection
				if _, err := settings.SetForAccount("account", ui); err != nil {
					t.Fatal(err)
				}
				record := pebblestore.ModelCatalogRecord{Provider: "google", Model: id, CatalogModalities: pebblestore.ModelCatalogModalities{Inputs: []string{"audio"}, Outputs: []string{"text"}}}
				if missing == "audio" {
					record.CatalogModalities.Inputs = []string{"video"}
				}
				if missing == "text" {
					record.CatalogModalities.Outputs = nil
				}
				s := NewService(nil, settings, speechFallbackCatalog{record})
				model, err := s.SpeechAlignmentModel("account")
				want := missing
				if strings.HasPrefix(selection, "openai/") {
					want = "provider must be google"
				}
				if want != "" {
					if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), id) || model != "" {
						t.Fatalf("rejected resolution = %q %v", model, err)
					}
					return
				}
				if err != nil || model != id {
					t.Fatalf("resolution = %q %v", model, err)
				}
			})
		}
	}
}
