package model

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: provider-level Auto is authoritative; stale per-model tags must not
// win by lexical order or retain obsolete thinking. The decoder is the narrow
// materialization boundary used by pinned seeding and live refresh.
func TestProviderRecommendationOverridesLegacyModelTags(t *testing.T) {
	legacy := []pebblestore.ModelCatalogRecommendation{{Role: "auto", Thinking: "high"}, {Role: "main", Thinking: "high"}}
	authoritative := map[string]swarmSnapshotProviderRecommendation{"auto": {Model: "gpt-new", Thinking: "medium"}}
	if got := appendProviderRecommendations(legacy, "codex", "gpt-old", "codex/gpt-old", authoritative); len(got) != 0 {
		t.Fatalf("stale tags survived: %+v", got)
	}
	got := appendProviderRecommendations(legacy, "codex", "gpt-new", "codex/gpt-new", authoritative)
	if len(got) != 1 || got[0].Role != "auto" || got[0].Thinking != "medium" {
		t.Fatalf("not authoritative: %+v", got)
	}
}

// Purpose: CatalogService.refresh must reject downgrade, split version/payload
// and invalid 304 responses without replacing any verified records. HTTP fakes
// and an isolated Pebble store exercise observable persistence, not source text.
func TestCatalogRefreshRejectsUnverifiedReplacement(t *testing.T) {
	for _, scenario := range []string{"downgrade", "mismatch", "payload304", "version304"} {
		t.Run(scenario, func(t *testing.T) {
			db, err := pebblestore.Open(filepath.Join(t.TempDir(), "catalog"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			catalog := NewCatalogService(pebblestore.NewModelCatalogStore(db))
			if err := catalog.EnsureBootDefaults(); err != nil {
				t.Fatal(err)
			}
			before, _, _ := catalog.Meta()
			records, err := catalog.List("codex", 2000)
			if err != nil {
				t.Fatal(err)
			}
			version := map[string]any{"snapshot_id": "new", "snapshot_version": "new", "snapshot_schema_version": "2026-07-01.1", "generated_at": "2099-01-01T00:00:00Z"}
			if scenario == "downgrade" {
				version["generated_at"] = "2000-01-01T00:00:00Z"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/version" {
					if scenario == "version304" {
						w.WriteHeader(304)
						return
					}
					_ = json.NewEncoder(w).Encode(version)
					return
				}
				if scenario == "payload304" {
					w.WriteHeader(304)
					return
				}
				_, _ = w.Write(pinnedSwarmSnapshotJSON)
			}))
			defer server.Close()
			catalog.versionURL, catalog.sourceURL = server.URL+"/version", server.URL+"/snapshot"
			result, err := catalog.Check(context.Background())
			if err == nil || !result.UsedCache || !result.UsingCacheFallback {
				t.Fatalf("false refresh success: %+v %v", result, err)
			}
			after, _, _ := catalog.Meta()
			actual, _ := catalog.List("codex", 2000)
			if before.SnapshotID != after.SnapshotID || before.FetchedAt != after.FetchedAt || after.LastError == "" || !reflect.DeepEqual(records, actual) {
				t.Fatal("verified records changed or failure not recorded")
			}
		})
	}
}
