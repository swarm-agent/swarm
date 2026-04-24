package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/provider/codex"
	"swarm/packages/swarmd/internal/provider/google"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
)

type stubRunner struct {
	id string
}

func (r stubRunner) ID() string { return r.id }

func (r stubRunner) CreateResponse(context.Context, provideriface.Request) (provideriface.Response, error) {
	return provideriface.Response{}, nil
}

func (r stubRunner) CreateResponseStreaming(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
	return provideriface.Response{}, nil
}

func TestAuthCredentialUpsertAppliesUtilityDefaultsOnce(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "auth-defaults.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	hub := stream.NewHub(nil)
	authStore := pebblestore.NewAuthStore(store)
	authSvc := auth.NewService(authStore, eventLog)
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), eventLog)
	if err := agentSvc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}
	modelSvc := model.NewService(pebblestore.NewModelStore(store), eventLog, nil)
	providers := registry.New(
		codex.NewAdapter(authStore),
		google.NewAdapter(authStore),
	)
	providers.RegisterRunner(stubRunner{id: "codex"})
	providers.RegisterRunner(stubRunner{id: "google"})

	server := NewServer("test", authSvc, agentSvc, modelSvc, nil, nil, nil, nil, nil, providers, nil, eventLog, hub)
	handler := server.Handler()

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/credentials",
		bytes.NewBufferString(`{"provider":"google","type":"api","api_key":"goog-test-key","active":true}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v1/auth/credentials status = %d, want 200", rec.Code)
	}

	var upsertResp struct {
		Provider     string                   `json:"provider"`
		AutoDefaults *auth.AutoDefaultsStatus `json:"auto_defaults"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &upsertResp); err != nil {
		t.Fatalf("decode upsert response: %v", err)
	}
	if upsertResp.Provider != "google" {
		t.Fatalf("upsert provider = %q, want google", upsertResp.Provider)
	}
	if upsertResp.AutoDefaults == nil || !upsertResp.AutoDefaults.Applied {
		t.Fatalf("expected auto defaults to apply on first provider add, got %#v", upsertResp.AutoDefaults)
	}
	if upsertResp.AutoDefaults.Provider != "google" || upsertResp.AutoDefaults.Model != "gemini-3.1-pro-preview" {
		t.Fatalf("auto default target = %s/%s, want google/gemini-3.1-pro-preview", upsertResp.AutoDefaults.Provider, upsertResp.AutoDefaults.Model)
	}
	if upsertResp.AutoDefaults.Thinking != "high" {
		t.Fatalf("primary thinking = %q, want high", upsertResp.AutoDefaults.Thinking)
	}
	if upsertResp.AutoDefaults.UtilityProvider != "google" || upsertResp.AutoDefaults.UtilityModel != "gemini-3-flash-preview" {
		t.Fatalf("utility default target = %s/%s, want google/gemini-3-flash-preview", upsertResp.AutoDefaults.UtilityProvider, upsertResp.AutoDefaults.UtilityModel)
	}
	if upsertResp.AutoDefaults.UtilityThinking != "high" {
		t.Fatalf("utility thinking = %q, want high", upsertResp.AutoDefaults.UtilityThinking)
	}
	if !upsertResp.AutoDefaults.GlobalModel {
		t.Fatalf("expected global model default to be set")
	}
	wantSubagents := map[string]struct{}{
		"explorer": {},
		"memory":   {},
		"parallel": {},
	}
	if len(upsertResp.AutoDefaults.Subagents) != len(wantSubagents) {
		t.Fatalf("subagents updated = %v, want 3 defaults", upsertResp.AutoDefaults.Subagents)
	}
	for _, name := range upsertResp.AutoDefaults.Subagents {
		if _, ok := wantSubagents[name]; !ok {
			t.Fatalf("unexpected subagent in defaults: %q", name)
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/model", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/model status = %d, want 200", rec.Code)
	}
	var modelResp struct {
		Preference struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
			Thinking string `json:"thinking"`
		} `json:"preference"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &modelResp); err != nil {
		t.Fatalf("decode model response: %v", err)
	}
	if modelResp.Preference.Provider != "google" || modelResp.Preference.Model != "gemini-3.1-pro-preview" {
		t.Fatalf("model preference = %s/%s, want google/gemini-3.1-pro-preview", modelResp.Preference.Provider, modelResp.Preference.Model)
	}
	if modelResp.Preference.Thinking != "high" {
		t.Fatalf("model thinking = %q, want high", modelResp.Preference.Thinking)
	}

	req = httptest.NewRequest(http.MethodGet, "/v2/agents", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v2/agents status = %d, want 200", rec.Code)
	}
	var agentsResp struct {
		State struct {
			Profiles []pebblestore.AgentProfile `json:"profiles"`
		} `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &agentsResp); err != nil {
		t.Fatalf("decode agents response: %v", err)
	}
	for _, profile := range agentsResp.State.Profiles {
		switch profile.Name {
		case "swarm":
			if profile.Provider != "google" {
				t.Fatalf("profile %q provider = %q, want google", profile.Name, profile.Provider)
			}
			if profile.Model != "gemini-3.1-pro-preview" {
				t.Fatalf("profile %q model = %q, want gemini-3.1-pro-preview", profile.Name, profile.Model)
			}
		case "explorer", "memory", "parallel":
			if profile.Provider != "google" {
				t.Fatalf("profile %q provider = %q, want google", profile.Name, profile.Provider)
			}
			if profile.Model != "gemini-3-flash-preview" {
				t.Fatalf("profile %q model = %q, want gemini-3-flash-preview", profile.Name, profile.Model)
			}
		case "clone":
			if profile.Provider != "" {
				t.Fatalf("profile %q provider = %q, want empty", profile.Name, profile.Provider)
			}
			if profile.Model != "" {
				t.Fatalf("profile %q model = %q, want empty", profile.Name, profile.Model)
			}
		}
	}

	for _, name := range []string{"explorer", "memory", "parallel"} {
		profile, ok, err := agentSvc.GetProfile(name)
		if err != nil {
			t.Fatalf("GetProfile(%q) error = %v", name, err)
		}
		if !ok {
			t.Fatalf("GetProfile(%q) missing", name)
		}
		enabled := profile.Enabled
		if _, _, _, err := agentSvc.Upsert(agentruntime.UpsertInput{
			Name:        profile.Name,
			Mode:        profile.Mode,
			Description: profile.Description,
			Provider:    "openai",
			Model:       "gpt-5-mini",
			Thinking:    "low",
			Prompt:      profile.Prompt,
			Enabled:     &enabled,
		}); err != nil {
			t.Fatalf("override profile %q: %v", name, err)
		}
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/auth/credentials",
		bytes.NewBufferString(`{"provider":"codex","type":"api","api_key":"codex-test-key","active":true}`))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v1/auth/credentials codex status = %d, want 200", rec.Code)
	}
	upsertResp = struct {
		Provider     string                   `json:"provider"`
		AutoDefaults *auth.AutoDefaultsStatus `json:"auto_defaults"`
	}{}
	if err := json.Unmarshal(rec.Body.Bytes(), &upsertResp); err != nil {
		t.Fatalf("decode second upsert response: %v", err)
	}
	if upsertResp.AutoDefaults != nil && upsertResp.AutoDefaults.Applied {
		t.Fatalf("expected no second defaults apply, got %#v", upsertResp.AutoDefaults)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/model", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/model after codex status = %d, want 200", rec.Code)
	}
	modelResp = struct {
		Preference struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
			Thinking string `json:"thinking"`
		} `json:"preference"`
	}{}
	if err := json.Unmarshal(rec.Body.Bytes(), &modelResp); err != nil {
		t.Fatalf("decode model response after codex: %v", err)
	}
	if modelResp.Preference.Provider != "google" || modelResp.Preference.Model != "gemini-3.1-pro-preview" {
		t.Fatalf("model preference after second provider = %s/%s, want google/gemini-3.1-pro-preview", modelResp.Preference.Provider, modelResp.Preference.Model)
	}

	req = httptest.NewRequest(http.MethodGet, "/v2/agents", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v2/agents after second provider status = %d, want 200", rec.Code)
	}
	agentsResp = struct {
		State struct {
			Profiles []pebblestore.AgentProfile `json:"profiles"`
		} `json:"state"`
	}{}
	if err := json.Unmarshal(rec.Body.Bytes(), &agentsResp); err != nil {
		t.Fatalf("decode agents response after second provider: %v", err)
	}
	for _, profile := range agentsResp.State.Profiles {
		switch profile.Name {
		case "swarm":
			if profile.Provider != "google" {
				t.Fatalf("profile %q provider after second provider = %q, want google", profile.Name, profile.Provider)
			}
			if profile.Model != "gemini-3.1-pro-preview" {
				t.Fatalf("profile %q model after second provider = %q, want gemini-3.1-pro-preview", profile.Name, profile.Model)
			}
		case "explorer", "memory", "parallel":
			if profile.Provider != "openai" {
				t.Fatalf("profile %q provider after second provider = %q, want openai", profile.Name, profile.Provider)
			}
			if profile.Model != "gpt-5-mini" {
				t.Fatalf("profile %q model after second provider = %q, want gpt-5-mini", profile.Name, profile.Model)
			}
			if profile.Thinking != "low" {
				t.Fatalf("profile %q thinking after second provider = %q, want low", profile.Name, profile.Thinking)
			}
		}
	}
}

func TestAuthCredentialUpsertCodexFirstProviderEnforcesSparkUtilityDefaults(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "auth-defaults-codex-first.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	hub := stream.NewHub(nil)
	authStore := pebblestore.NewAuthStore(store)
	authSvc := auth.NewService(authStore, eventLog)
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), eventLog)
	if err := agentSvc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}
	modelSvc := model.NewService(pebblestore.NewModelStore(store), eventLog, nil)
	providers := registry.New(
		codex.NewAdapter(authStore),
		google.NewAdapter(authStore),
	)
	providers.RegisterRunner(stubRunner{id: "codex"})
	providers.RegisterRunner(stubRunner{id: "google"})

	server := NewServer("test", authSvc, agentSvc, modelSvc, nil, nil, nil, nil, nil, providers, nil, eventLog, hub)
	handler := server.Handler()

	// Simulate profiles that already have provider/model set before first auth onboarding.
	for _, name := range []string{"explorer", "memory", "parallel"} {
		profile, ok, err := agentSvc.GetProfile(name)
		if err != nil {
			t.Fatalf("GetProfile(%q) error = %v", name, err)
		}
		if !ok {
			t.Fatalf("GetProfile(%q) missing", name)
		}
		enabled := profile.Enabled
		if _, _, _, err := agentSvc.Upsert(agentruntime.UpsertInput{
			Name:        profile.Name,
			Mode:        profile.Mode,
			Description: profile.Description,
			Provider:    "codex",
			Model:       "gpt-5.4",
			Thinking:    "high",
			Prompt:      profile.Prompt,
			Enabled:     &enabled,
		}); err != nil {
			t.Fatalf("seed profile %q override: %v", name, err)
		}
	}

	pref, err := modelSvc.GetGlobalPreference()
	if err != nil {
		t.Fatalf("GetGlobalPreference() error = %v", err)
	}
	if pref.Provider != "" || pref.Model != "" {
		t.Fatalf("expected unset global preference before onboarding, got %s/%s", pref.Provider, pref.Model)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/credentials",
		bytes.NewBufferString(`{"provider":"codex","type":"api","api_key":"codex-test-key","active":true}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v1/auth/credentials status = %d, want 200", rec.Code)
	}

	var upsertResp struct {
		Provider     string                   `json:"provider"`
		AutoDefaults *auth.AutoDefaultsStatus `json:"auto_defaults"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &upsertResp); err != nil {
		t.Fatalf("decode upsert response: %v", err)
	}
	if upsertResp.AutoDefaults == nil || !upsertResp.AutoDefaults.Applied {
		t.Fatalf("expected auto defaults to apply, got %#v", upsertResp.AutoDefaults)
	}
	if upsertResp.AutoDefaults.Provider != "codex" || upsertResp.AutoDefaults.Model != "gpt-5.5" {
		t.Fatalf("auto default target = %s/%s, want codex/gpt-5.5", upsertResp.AutoDefaults.Provider, upsertResp.AutoDefaults.Model)
	}
	if !upsertResp.AutoDefaults.GlobalModel {
		t.Fatalf("expected first provider onboarding to set global model")
	}
	if upsertResp.AutoDefaults.UtilityProvider != "codex" {
		t.Fatalf("utility provider = %q, want codex", upsertResp.AutoDefaults.UtilityProvider)
	}
	if upsertResp.AutoDefaults.UtilityModel != "gpt-5.4-mini" {
		t.Fatalf("utility model = %q, want gpt-5.4-mini", upsertResp.AutoDefaults.UtilityModel)
	}

	req = httptest.NewRequest(http.MethodGet, "/v2/agents", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v2/agents status = %d, want 200", rec.Code)
	}
	var agentsResp struct {
		State struct {
			Profiles []pebblestore.AgentProfile `json:"profiles"`
		} `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &agentsResp); err != nil {
		t.Fatalf("decode agents response: %v", err)
	}
	for _, profile := range agentsResp.State.Profiles {
		switch profile.Name {
		case "explorer", "memory", "parallel":
			if profile.Provider != "codex" {
				t.Fatalf("profile %q provider = %q, want codex", profile.Name, profile.Provider)
			}
			if profile.Model != "gpt-5.4-mini" {
				t.Fatalf("profile %q model = %q, want gpt-5.4-mini", profile.Name, profile.Model)
			}
		}
	}
}
