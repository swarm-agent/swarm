package pebblestore

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// Requirement: provider vertex runs the same Gemini models as google, priced
// from google's catalog records, so its usage counts toward the daily spend
// cap. Without this every Vertex turn cost 0 and the cap never tripped.
func TestVertexUsageIsPricedFromGoogleCatalog(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "s.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pricing, _ := json.Marshal(map[string]float64{"input_price_per_million_tokens": 1, "output_price_per_million_tokens": 2})
	if err := NewModelCatalogStore(store).SetRecord(ModelCatalogRecord{Provider: "google", Model: "gemini-test", Pricing: pricing}); err != nil {
		t.Fatal(err)
	}
	sessions := NewSessionStore(store)
	google, gStatus := sessions.CalculateCostWithStatus("google", "gemini-test", 1_000_000, 1_000_000, 0, 0, 0)
	vertex, vStatus := sessions.CalculateCostWithStatus("vertex", "gemini-test", 1_000_000, 1_000_000, 0, 0, 0)
	if google <= 0 || vertex != google || vStatus != gStatus {
		t.Fatalf("google=%v/%s vertex=%v/%s", google, gStatus, vertex, vStatus)
	}
}
