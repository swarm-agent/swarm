package audiogen

import (
	"encoding/json"
	"errors"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/uisettings"
)

type speechFallbackCatalog []pebblestore.ModelCatalogRecord

func (c speechFallbackCatalog) ListCatalog(string, int) ([]pebblestore.ModelCatalogRecord, error) {
	return c, nil
}

// Purpose: SpeechAlignmentModel must select a deterministic capable catalog
// default, not a realtime endpoint, and must preserve explicit preferences and
// account credential isolation. This service-level test needs no HTTP dispatch.
func TestSpeechFallbackSelection(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	auth := pebblestore.NewAuthStore(db)
	if _, err := auth.UpsertCredential(pebblestore.AuthCredentialInput{Provider: "google", AccountScopeID: "account", Type: "api_key", APIKey: "test-key", SetActive: true}); err != nil {
		t.Fatal(err)
	}
	settings := uisettings.NewService(pebblestore.NewUISettingsStore(db))
	var catalog speechFallbackCatalog
	if err := json.Unmarshal([]byte(`[
 {"provider":"google","model":"z-model","display_name":"Z","catalog_modalities":{"inputs":["audio"],"outputs":["text"]}},
 {"provider":"google","model":"live-model","display_name":"A","catalog_modalities":{"inputs":["audio"],"outputs":["text"]}},
 {"provider":"google","model":"text-model","display_name":"B","catalog_modalities":{"inputs":["text"],"outputs":["text"]}},
 {"provider":"google","model":"c-model","display_name":"C","catalog_modalities":{"inputs":["audio"],"outputs":["text"]}}
 ]`), &catalog); err != nil {
		t.Fatal(err)
	}
	s := NewService(auth, settings, catalog)
	if model, err := s.SpeechAlignmentModel("account"); err != nil || model != "c-model" {
		t.Fatalf("model=%q err=%v", model, err)
	}
	if catalog[0].Model != "z-model" {
		t.Fatal("mutated catalog ordering")
	}
	if model, err := s.SpeechAlignmentModel("other-account"); err == nil || model != "" {
		t.Fatalf("cross-account fallback: %q %v", model, err)
	}
	ui, err := settings.GetForAccount("account")
	if err != nil {
		t.Fatal(err)
	}
	ui.Media.TranscriptionModel = "google/z-model"
	if _, err := settings.SetForAccount("account", ui); err != nil {
		t.Fatal(err)
	}
	if model, err := s.SpeechAlignmentModel("account"); err != nil || model != "z-model" {
		t.Fatalf("explicit model=%q err=%v", model, err)
	}
	ui.Media.TranscriptionModel = "missing"
	if _, err := settings.SetForAccount("account", ui); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SpeechAlignmentModel("account"); err == nil || errors.Is(err, ErrNoSpeechTranscriptionModel) {
		t.Fatalf("invalid explicit setting silently ignored: %v", err)
	}
}
