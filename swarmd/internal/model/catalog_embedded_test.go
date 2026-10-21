package model

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: with SWARM_MODEL_CATALOG=embedded a box with no internet access
// refreshes from the snapshot built into the binary and never calls the
// remote catalog, so first-provider onboarding works offline. Without it the
// remote fetch still happens and its failure is still reported.
func TestCatalogEmbeddedOnlyNeverFetches(t *testing.T) {
	var calls atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unreachable", http.StatusServiceUnavailable)
	}))
	defer remote.Close()
	newService := func(name string) *CatalogService {
		store, err := pebblestore.Open(filepath.Join(t.TempDir(), name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		s := NewCatalogService(pebblestore.NewModelCatalogStore(store))
		s.sourceURL, s.versionURL = remote.URL+"/snapshot.json", remote.URL+"/snapshot-version.json"
		return s
	}

	offline := newService("offline.pebble")
	offline.UseEmbeddedOnly()
	result, err := offline.Refresh(context.Background())
	if err != nil {
		t.Fatalf("embedded-only refresh failed: %v", err)
	}
	if !result.UsedPinned || calls.Load() != 0 {
		t.Fatalf("embedded-only refresh: used_pinned=%v remote calls=%d", result.UsedPinned, calls.Load())
	}
	if _, err := offline.RefreshManual(context.Background()); err != nil || calls.Load() != 0 {
		t.Fatalf("manual embedded-only refresh: err=%v remote calls=%d", err, calls.Load())
	}

	online := newService("online.pebble")
	if _, err := online.Refresh(context.Background()); err == nil || calls.Load() == 0 {
		t.Fatalf("default refresh did not fetch or did not report the failure: err=%v calls=%d", err, calls.Load())
	}
}
