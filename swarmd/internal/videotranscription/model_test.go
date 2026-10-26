package videotranscription

import (
	"strings"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

type resolutionCatalog []pebblestore.ModelCatalogRecord

func (c resolutionCatalog) ListCatalog(string, int) ([]pebblestore.ModelCatalogRecord, error) {
	return c, nil
}

// Purpose: resolveModel must normalize account selections without accepting a
// foreign provider or missing modalities. This service-boundary test proves
// rejection before dispatch and identical cache identity without network calls.
func TestTranscriptionModelResolution(t *testing.T) {
	const id = "gemini-3.8-flash"
	for _, input := range []string{"video", "audio"} {
		var hash string
		for _, selection := range []string{id, "google/" + id, "openai/" + id} {
			for _, missing := range []string{"", input, "text"} {
				t.Run(input+"/"+selection+"/missing-"+missing, func(t *testing.T) {
					record := pebblestore.ModelCatalogRecord{Provider: "google", Model: id, SourceSnapshotID: "snap", SourceSnapshotVersion: "1", CatalogModalities: pebblestore.ModelCatalogModalities{Inputs: []string{input}, Outputs: []string{"text"}}}
					if missing == input {
						record.CatalogModalities.Inputs = nil
					}
					if missing == "text" {
						record.CatalogModalities.Outputs = nil
					}
					s := NewService(nil, resolutionCatalog{record}, audioSettings{model: selection}, nil)
					model, snapshot, gotHash, err := s.resolveModel("account", input)
					want := missing
					if strings.HasPrefix(selection, "openai/") {
						want = "provider must be google"
					}
					if want != "" {
						if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), id) || model != "" || snapshot != "" || gotHash != "" {
							t.Fatalf("rejected resolution = %q %q %q %v", model, snapshot, gotHash, err)
						}
						return
					}
					if err != nil || model != id || snapshot != "snap:1" || gotHash == "" {
						t.Fatalf("resolution = %q %q %q %v", model, snapshot, gotHash, err)
					}
					if hash != "" && hash != gotHash {
						t.Fatal("prefix changed cache identity")
					}
					hash = gotHash
				})
			}
		}
	}
}
