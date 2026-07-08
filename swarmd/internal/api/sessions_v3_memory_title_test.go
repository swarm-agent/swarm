package api

import (
	"path/filepath"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	modelruntime "swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/provider/registry"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestGenerateSessionV3MemoryTitleIncludesModelCatalog(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)

	modelStore, err := pebblestore.Open(filepath.Join(t.TempDir(), "memory-title-model.pebble"))
	if err != nil {
		t.Fatalf("open model store: %v", err)
	}
	t.Cleanup(func() { _ = modelStore.Close() })
	catalogStore := pebblestore.NewModelCatalogStore(modelStore)
	if err := catalogStore.SetRecord(pebblestore.ModelCatalogRecord{
		Provider:        "anthropic",
		Model:           "claude-sonnet-5-test",
		ContextWindow:   200000,
		MaxOutputTokens: 32000,
		Reasoning:       true,
		ThinkingOptions: []string{"low", "medium", "high", "xhigh"},
		DefaultThinking: "high",
		ThinkingMappings: []pebblestore.ModelCatalogThinkingMapping{{
			SwarmSetting:           "high",
			ProviderParameter:      "output_config.effort",
			ProviderValue:          "high",
			EffectiveProviderValue: "high",
			Behavior:               "effort",
		}},
		Source: "test",
	}); err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	server.model = modelruntime.NewService(pebblestore.NewModelStore(modelStore), server.events, modelruntime.NewCatalogService(catalogStore))
	if profile, ok := agentruntime.DefaultProfileByName("memory"); ok {
		profile.Provider = "anthropic"
		profile.Model = "claude-sonnet-5-test"
		profile.Thinking = "high"
		if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: profile.Name, Mode: profile.Mode, Description: profile.Description, Provider: profile.Provider, Model: profile.Model, Thinking: profile.Thinking, Prompt: profile.Prompt, RuntimeMode: profile.RuntimeMode, ExecutionSetting: profile.ExecutionSetting, ExitPlanModeEnabled: profile.ExitPlanModeEnabled, ToolContract: profile.ToolContract, Enabled: pebblestore.BoolPtr(profile.Enabled)}); err != nil {
			t.Fatalf("seed memory profile: %v", err)
		}
	} else {
		t.Fatal("default memory profile not found")
	}
	runner := &sessionsV3RecordingProviderRunner{id: "anthropic", text: "Anthropic Titler"}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers

	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "memory-title-catalog-create", "memory title catalog", pebblestore.ModelPreference{Provider: "anthropic", Model: "claude-sonnet-5-test", Thinking: "high"})
	title, err := newSessionV3Executor(server).generateSessionV3MemoryTitle(created, "User asked Anthropic Sonnet 5 to speak back.", testPrincipal())
	if err != nil {
		t.Fatalf("generate title: %v", err)
	}
	if title != "Anthropic Titler" {
		t.Fatalf("title = %q, want %q", title, "Anthropic Titler")
	}
	runner.mu.Lock()
	lastReq := runner.lastRequest
	runner.mu.Unlock()
	if lastReq.ModelCatalog == nil {
		t.Fatal("memory title request ModelCatalog is nil")
	}
	record, ok := lastReq.ModelCatalog.(pebblestore.ModelCatalogRecord)
	if !ok {
		t.Fatalf("ModelCatalog type = %T, want pebblestore.ModelCatalogRecord", lastReq.ModelCatalog)
	}
	if record.Provider != "anthropic" || record.Model != "claude-sonnet-5-test" {
		t.Fatalf("ModelCatalog record = %s/%s", record.Provider, record.Model)
	}
	if lastReq.ContextWindow != 200000 {
		t.Fatalf("ContextWindow = %d, want 200000", lastReq.ContextWindow)
	}
}
