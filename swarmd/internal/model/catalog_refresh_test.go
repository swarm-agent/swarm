package model

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: CatalogService.refresh must install recommendation-only releases
// with a different snapshot ID and equal generated_at, reject older releases
// without changing cached records, and leave identical snapshots unchanged.
// This service-level test uses real decoding and Pebble replacement plus a local
// HTTP server: the narrowest layer exercising advertisement, persistence, and
// RecommendedRoleDefaults together without depending on a live catalog.
func TestCatalogRefreshSnapshotGeneration(t *testing.T) {
	const generatedAt = "2026-10-22T12:50:06.292Z"
	for _, tc := range []struct {
		name        string
		snapshotID  string
		generatedAt string
		wantError   bool
		unchanged   bool
	}{
		{name: "different snapshot equal timestamp", snapshotID: "catalog-new", generatedAt: generatedAt},
		{name: "different snapshot older timestamp", snapshotID: "catalog-old", generatedAt: "2026-10-21T12:50:06.292Z", wantError: true},
		{name: "same snapshot unchanged", snapshotID: "catalog-current", generatedAt: generatedAt, unchanged: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := pebblestore.Open(filepath.Join(t.TempDir(), "catalog.pebble"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			s := NewCatalogService(pebblestore.NewModelCatalogStore(store))
			s.now = func() time.Time { return time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC) }
			initial := swarmSnapshot{
				SnapshotID: "catalog-current", SnapshotVersion: "v1", GeneratedAt: generatedAt,
				Models: []swarmSnapshotModel{{ProviderID: "openai", ModelID: "test-model"}},
			}
			contextWindow := 8192
			initial.Models[0].Limits.ContextWindowTokens = &contextWindow
			payload, err := json.Marshal(initial)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.replaceSnapshotLocked(payload, catalogSourceLive, "", "", "", "", "test_seed"); err != nil {
				t.Fatal(err)
			}
			before, ok, err := s.Meta()
			if err != nil || !ok {
				t.Fatalf("seed meta: ok=%v err=%v", ok, err)
			}
			beforeRecords, err := s.store.ListProvider("openai", 10)
			if err != nil {
				t.Fatal(err)
			}
			if _, found, err := s.RecommendedRoleDefaults("openai", "review"); err != nil || found {
				t.Fatalf("seed unexpectedly has review recommendation: found=%v err=%v", found, err)
			}

			next := initial
			next.SnapshotID, next.GeneratedAt = tc.snapshotID, tc.generatedAt
			if !tc.unchanged {
				next.Recommendations = map[string]map[string]swarmSnapshotProviderRecommendation{
					"openai": {"review": {Model: "test-model"}},
				}
			}
			var versionCalls, snapshotCalls atomic.Int32
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/snapshot-version.json":
					versionCalls.Add(1)
					_ = json.NewEncoder(w).Encode(next.version())
				case "/v1/snapshot.json":
					snapshotCalls.Add(1)
					_ = json.NewEncoder(w).Encode(next)
				default:
					http.NotFound(w, r)
				}
			}))
			defer remote.Close()
			s.sourceURL, s.versionURL = remote.URL+"/v1/snapshot.json", remote.URL+"/snapshot-version.json"
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var result CatalogRefreshResult
			if tc.unchanged {
				result, err = s.Refresh(ctx)
			} else {
				result, err = s.RefreshManual(ctx)
			}
			const refusal = "advertised snapshot is not newer than the verified catalog"
			if tc.wantError {
				if err == nil || err.Error() != refusal {
					t.Fatalf("expected rollback refusal, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			meta, ok, err := s.Meta()
			if err != nil || !ok {
				t.Fatalf("refreshed meta: ok=%v err=%v", ok, err)
			}
			if versionCalls.Load() != 1 {
				t.Fatalf("version requests = %d, want 1", versionCalls.Load())
			}
			if tc.wantError || tc.unchanged {
				records, err := s.store.ListProvider("openai", 10)
				if err != nil || !reflect.DeepEqual(records, beforeRecords) {
					t.Fatalf("cached records changed: records=%+v err=%v", records, err)
				}
				if meta.SnapshotID != before.SnapshotID || meta.GeneratedAt != before.GeneratedAt || snapshotCalls.Load() != 0 || !result.UsedCache {
					t.Fatalf("cached snapshot not preserved: meta=%+v result=%+v fetches=%d", meta, result, snapshotCalls.Load())
				}
				if tc.wantError && (meta.LastError != refusal || !meta.UsingCacheFallback || meta.LastErrorAt == 0) {
					t.Fatalf("rollback error not persisted: %+v", meta)
				}
				if tc.unchanged && (!result.NotModified || meta.LastError != "" || meta.UsingCacheFallback) {
					t.Fatalf("same snapshot not unchanged: meta=%+v result=%+v", meta, result)
				}
				return
			}
			if meta.SnapshotID != tc.snapshotID || meta.GeneratedAt != generatedAt || meta.LastError != "" || meta.UsingCacheFallback || result.NotModified || snapshotCalls.Load() != 1 {
				t.Fatalf("new snapshot not installed: meta=%+v result=%+v fetches=%d", meta, result, snapshotCalls.Load())
			}
			recommended, found, err := s.RecommendedRoleDefaults("openai", "review")
			if err != nil || !found || recommended["review"].Model != "test-model" || recommended["review"].SourceSnapshotID != tc.snapshotID {
				t.Fatalf("new review recommendation not available: records=%+v found=%v err=%v", recommended, found, err)
			}
		})
	}
}
