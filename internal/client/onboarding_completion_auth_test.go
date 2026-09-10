package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Completion is a protected post-bootstrap mutation. SaveOnboarding must send
// the product session on HTTP instead of silently dropping authentication.
func TestOnboardingCompletionUsesProductSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Swarm-Token") != "test-product-session" {
			t.Error("completion request omitted product session")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"needs_onboarding":false}`))
	}))
	defer server.Close()
	api := New(server.URL)
	api.SetToken("test-product-session")
	complete := true
	if _, err := api.SaveOnboarding(context.Background(), SaveOnboardingInput{DesktopOnboardingComplete: &complete}); err != nil {
		t.Fatal(err)
	}
}
