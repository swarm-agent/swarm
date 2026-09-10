package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Requirement: explicit TUI setup uses the authenticated canonical route and
// exact stale-path guard. Transport failure must propagate rather than admitting
// a workspace. A loopback fake endpoint is the narrowest wire-contract test.
func TestOnboardingRepositorySetupRequestAndFailure(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")
	for _, status := range []int{200, 409} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req map[string]string
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if r.Method != "POST" || r.URL.Path != "/v1/workspace/repository/setup" || req["path"] != "/workspace" || req["expected_resolved_path"] != "/workspace" || r.Header.Get("X-Swarm-Token") != "test-token" {
				t.Error("invalid setup request")
			}
			w.WriteHeader(status)
			if status == 200 {
				w.Write([]byte(`{"ok":true}`))
			} else {
				w.Write([]byte(`{"error":"existing files require review"}`))
			}
		}))
		api := New(server.URL)
		api.SetToken("test-token")
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := api.SetupOnboardingRepository(ctx, "/workspace")
		cancel()
		server.Close()
		if (err != nil) != (status != 200) {
			t.Fatalf("status %d error %v", status, err)
		}
	}
}

// Requirement: TUI finalization must persist the shared completion flag through
// SaveOnboarding, not just hide the modal. Inspect the exact wire payload and
// propagate failure so the caller cannot report completion on a failed save.
func TestOnboardingCompletionPersistenceRequest(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/v1/onboarding" || req["desktop_onboarding_complete"] != true || req["child"] != nil {
			t.Errorf("wrong completion payload: %+v", req)
		}
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"completion failed"}`))
	}))
	defer server.Close()
	api := New(server.URL)
	api.SetToken("test-token")
	complete := true
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := api.SaveOnboarding(ctx, SaveOnboardingInput{DesktopOnboardingComplete: &complete}); err == nil {
		t.Fatal("completion failure swallowed")
	}
}
