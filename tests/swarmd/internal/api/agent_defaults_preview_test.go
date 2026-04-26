package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	api "swarm/packages/swarmd/internal/api"
	"swarm/packages/swarmd/internal/model"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
)

func TestProviderDefaultsPreviewWarnsOnlyForStaleInheritedUtilityAgents(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "agent-defaults-preview.pebble"))
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
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), eventLog)
	if err := agentSvc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}
	modelSvc := model.NewService(pebblestore.NewModelStore(store), eventLog, nil)
	if _, _, err := modelSvc.SetGlobalPreference("codex", "gpt-5.5", "high"); err != nil {
		t.Fatalf("SetGlobalPreference() error = %v", err)
	}

	server := api.NewServer("test", nil, agentSvc, modelSvc, nil, nil, nil, nil, nil, nil, nil, eventLog, stream.NewHub(nil))
	handler := server.Handler()

	req := httptest.NewRequest(http.MethodGet, "/v2/agents", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v2/agents status = %d, want 200", rec.Code)
	}
	preview := decodeAgentDefaultsPreview(t, rec)
	wantStale := map[string]struct{}{"explorer": {}, "memory": {}, "parallel": {}}
	if len(preview.StaleInheritedAgents) != len(wantStale) {
		t.Fatalf("stale inherited agents = %v, want %v", preview.StaleInheritedAgents, wantStale)
	}
	for _, name := range preview.StaleInheritedAgents {
		if _, ok := wantStale[name]; !ok {
			t.Fatalf("unexpected stale inherited agent %q", name)
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
			Provider:    "codex",
			ProviderSet: true,
			Model:       "gpt-5.4",
			ModelSet:    true,
			Thinking:    "low",
			ThinkingSet: true,
			Prompt:      profile.Prompt,
			Enabled:     &enabled,
		}); err != nil {
			t.Fatalf("set intentional custom utility model for %q: %v", name, err)
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/v2/agents", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v2/agents status after explicit utility settings = %d, want 200", rec.Code)
	}
	preview = decodeAgentDefaultsPreview(t, rec)
	if len(preview.StaleInheritedAgents) != 0 {
		t.Fatalf("stale inherited agents after explicit utility settings = %v, want none", preview.StaleInheritedAgents)
	}
	if len(preview.OutOfSyncAgents) != 0 {
		t.Fatalf("out of sync agents after explicit utility settings = %v, want none", preview.OutOfSyncAgents)
	}
	if len(preview.UtilityBaselineAgents) != len(wantStale) {
		t.Fatalf("utility baseline agents after explicit utility settings = %v, want %v", preview.UtilityBaselineAgents, wantStale)
	}
	if len(preview.CustomUtilityAgents) != 0 {
		t.Fatalf("custom utility agents after explicit utility settings = %v, want none", preview.CustomUtilityAgents)
	}
}

func TestRestoreDefaultsSetsUtilityAIBaselineAndPreservesCustomProfiles(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "agent-utility-restore.pebble"))
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
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), eventLog)
	if err := agentSvc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}
	modelSvc := model.NewService(pebblestore.NewModelStore(store), eventLog, nil)
	if _, _, err := modelSvc.SetGlobalPreference("codex", "gpt-5.5", "high"); err != nil {
		t.Fatalf("SetGlobalPreference() error = %v", err)
	}

	server := api.NewServer("test", nil, agentSvc, modelSvc, nil, nil, nil, nil, nil, nil, nil, eventLog, stream.NewHub(nil))
	handler := server.Handler()

	req := httptest.NewRequest(http.MethodPost, "/v2/agents/defaults/restore", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v2/agents/defaults/restore status = %d, want 200", rec.Code)
	}
	for _, name := range []string{"explorer", "memory", "parallel"} {
		profile, ok, err := agentSvc.GetProfile(name)
		if err != nil {
			t.Fatalf("GetProfile(%q) error = %v", name, err)
		}
		if !ok {
			t.Fatalf("GetProfile(%q) missing", name)
		}
		if profile.Provider != "codex" {
			t.Fatalf("profile %q provider = %q, want codex", name, profile.Provider)
		}
		if profile.Model != "gpt-5.4-mini" {
			t.Fatalf("profile %q model = %q, want gpt-5.4-mini", name, profile.Model)
		}
	}

	// A custom Utility AI selection should update only built-in utility agents,
	// not regenerate all built-ins or overwrite unrelated user choices.
	enabled := true
	if _, _, _, err := agentSvc.Upsert(agentruntime.UpsertInput{
		Name:        "custom-helper",
		Mode:        agentruntime.ModeSubagent,
		Description: "custom helper",
		Provider:    "codex",
		ProviderSet: true,
		Model:       "gpt-5.5",
		ModelSet:    true,
		Thinking:    "high",
		ThinkingSet: true,
		Prompt:      "custom prompt",
		Enabled:     &enabled,
	}); err != nil {
		t.Fatalf("create custom helper: %v", err)
	}
	req = httptest.NewRequest(http.MethodPost, "/v2/agents/defaults/restore", strings.NewReader(`{"utility_provider":"codex","utility_model":"gpt-5.4","utility_thinking":"low"}`))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v2/agents/defaults/restore custom Utility AI status = %d, want 200", rec.Code)
	}
	customProfile, ok, err := agentSvc.GetProfile("custom-helper")
	if err != nil {
		t.Fatalf("GetProfile(custom-helper) error = %v", err)
	}
	if !ok {
		t.Fatalf("GetProfile(custom-helper) missing")
	}
	if customProfile.Description != "custom helper" || customProfile.Prompt != "custom prompt" {
		t.Fatalf("custom Utility AI reset custom helper profile = %+v, want custom fields preserved", customProfile)
	}
	for _, name := range []string{"explorer", "memory", "parallel"} {
		profile, ok, err := agentSvc.GetProfile(name)
		if err != nil {
			t.Fatalf("GetProfile(%q) after custom Utility AI error = %v", name, err)
		}
		if !ok {
			t.Fatalf("GetProfile(%q) after custom Utility AI missing", name)
		}
		if profile.Provider != "codex" || profile.Model != "gpt-5.4" || profile.Thinking != "low" {
			t.Fatalf("profile %q Utility AI = %s/%s/%s, want codex/gpt-5.4/low", name, profile.Provider, profile.Model, profile.Thinking)
		}
	}
}

func TestSetUtilityAIFillsBlankAgentsAndPreservesPerAgentOverrides(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "agent-utility-overrides.pebble"))
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
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), eventLog)
	if err := agentSvc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}
	modelSvc := model.NewService(pebblestore.NewModelStore(store), eventLog, nil)
	if _, _, err := modelSvc.SetGlobalPreference("codex", "gpt-5.5", "high"); err != nil {
		t.Fatalf("SetGlobalPreference() error = %v", err)
	}

	memory, ok, err := agentSvc.GetProfile("memory")
	if err != nil {
		t.Fatalf("GetProfile(memory) error = %v", err)
	}
	if !ok {
		t.Fatalf("GetProfile(memory) missing")
	}
	enabled := memory.Enabled
	if _, _, _, err := agentSvc.Upsert(agentruntime.UpsertInput{
		Name:        memory.Name,
		Mode:        memory.Mode,
		Description: memory.Description,
		Provider:    "codex",
		ProviderSet: true,
		Model:       "gpt-5.5",
		ModelSet:    true,
		Thinking:    "high",
		ThinkingSet: true,
		Prompt:      memory.Prompt,
		Enabled:     &enabled,
	}); err != nil {
		t.Fatalf("set memory override: %v", err)
	}

	server := api.NewServer("test", nil, agentSvc, modelSvc, nil, nil, nil, nil, nil, nil, nil, eventLog, stream.NewHub(nil))
	handler := server.Handler()
	req := httptest.NewRequest(http.MethodPost, "/v2/agents/defaults/restore", strings.NewReader(`{"utility_provider":"codex","utility_model":"gpt-5.4","utility_thinking":"low"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v2/agents/defaults/restore custom Utility AI status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	want := map[string]string{
		"explorer": "gpt-5.4",
		"memory":   "gpt-5.5",
		"parallel": "gpt-5.4",
	}
	for name, wantModel := range want {
		profile, ok, err := agentSvc.GetProfile(name)
		if err != nil {
			t.Fatalf("GetProfile(%q) error = %v", name, err)
		}
		if !ok {
			t.Fatalf("GetProfile(%q) missing", name)
		}
		if profile.Model != wantModel {
			t.Fatalf("profile %q model = %q, want %q", name, profile.Model, wantModel)
		}
	}
	preview := decodeAgentDefaultsPreview(t, rec)
	if strings.Join(preview.CustomUtilityAgents, ",") != "memory" {
		t.Fatalf("custom utility agents = %v, want [memory]", preview.CustomUtilityAgents)
	}
	if strings.Join(preview.UtilityBaselineAgents, ",") != "explorer,parallel" {
		t.Fatalf("utility baseline agents = %v, want [explorer parallel]", preview.UtilityBaselineAgents)
	}

	req = httptest.NewRequest(http.MethodPost, "/v2/agents/defaults/restore", strings.NewReader(`{"utility_provider":"codex","utility_model":"gpt-5.4","utility_thinking":"low","overwrite_explicit":true}`))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v2/agents/defaults/restore overwrite Utility AI status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	for _, name := range []string{"explorer", "memory", "parallel"} {
		profile, ok, err := agentSvc.GetProfile(name)
		if err != nil {
			t.Fatalf("GetProfile(%q) after overwrite error = %v", name, err)
		}
		if !ok {
			t.Fatalf("GetProfile(%q) after overwrite missing", name)
		}
		if profile.Provider != "codex" || profile.Model != "gpt-5.4" || profile.Thinking != "low" {
			t.Fatalf("profile %q overwritten Utility AI = %s/%s/%s, want codex/gpt-5.4/low", name, profile.Provider, profile.Model, profile.Thinking)
		}
	}
	preview = decodeAgentDefaultsPreview(t, rec)
	if len(preview.CustomUtilityAgents) != 0 {
		t.Fatalf("custom utility agents after overwrite = %v, want none", preview.CustomUtilityAgents)
	}
}

type providerDefaultsPreviewPayload struct {
	Provider              string   `json:"provider"`
	UtilityProvider       string   `json:"utility_provider"`
	UtilityModel          string   `json:"utility_model"`
	UtilityAgents         []string `json:"utility_agents"`
	OutOfSyncAgents       []string `json:"out_of_sync_agents"`
	StaleInheritedAgents  []string `json:"stale_inherited_agents"`
	CustomUtilityAgents   []string `json:"custom_utility_agents"`
	UtilityBaselineAgents []string `json:"utility_baseline_agents"`
}

func decodeAgentDefaultsPreview(t *testing.T, rec *httptest.ResponseRecorder) providerDefaultsPreviewPayload {
	t.Helper()
	var payload struct {
		ProviderDefaultsPreview providerDefaultsPreviewPayload `json:"provider_defaults_preview"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode agents response: %v", err)
	}
	return payload.ProviderDefaultsPreview
}
