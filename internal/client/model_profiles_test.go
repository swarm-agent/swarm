package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListModelProfilesNormalizesCanonicalFlatFavorite(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model_profiles":[{"profile_id":"focus","name":"Focus","provider":"codex","model":"gpt-focus","thinking":"high","service_tier":"fast","context_mode":"full"}],"default_profile_id":"focus"}`))
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("token")
	state, err := api.ListModelProfiles(context.Background())
	if err != nil {
		t.Fatalf("list profiles: %v", err)
	}
	if len(state.Profiles) != 1 {
		t.Fatalf("profiles = %#v", state.Profiles)
	}
	profile := state.Profiles[0]
	if profile.ModelMode != "single" || profile.Single == nil {
		t.Fatalf("flat profile was not normalized: %#v", profile)
	}
	if profile.Single.Provider != "codex" || profile.Single.Model != "gpt-focus" || profile.Single.Thinking != "high" || profile.Single.ServiceTier != "fast" || profile.Single.ContextMode != "full" {
		t.Fatalf("normalized selection = %#v", profile.Single)
	}
	if !profile.IsDefault {
		t.Fatal("default profile marker was not projected")
	}
}
