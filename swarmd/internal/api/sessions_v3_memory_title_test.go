package api

import (
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/agentmodelsettings"
	modelruntime "swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/provider/registry"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestGenerateSessionV3CompactTitleUsesUtilityRecommendationAndTitleOnlyPrompt(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)

	modelStore, err := pebblestore.Open(filepath.Join(t.TempDir(), "compact-title-model.pebble"))
	if err != nil {
		t.Fatalf("open model store: %v", err)
	}
	t.Cleanup(func() { _ = modelStore.Close() })
	catalogStore := pebblestore.NewModelCatalogStore(modelStore)
	if err := catalogStore.SetRecord(pebblestore.ModelCatalogRecord{
		Provider: "anthropic", Model: "claude-sonnet-5-test", ContextWindow: 200000, MaxOutputTokens: 32000,
		Reasoning: true, ThinkingOptions: []string{"low", "medium", "high"}, DefaultThinking: "high",
		Recommendations: []pebblestore.ModelCatalogRecommendation{{Role: "main", Thinking: "high"}, {Role: "plan", Thinking: "high"}, {Role: "utility", Thinking: "medium"}},
		Source:          "test",
	}); err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	server.model = modelruntime.NewService(pebblestore.NewModelStore(modelStore), server.events, modelruntime.NewCatalogService(catalogStore))
	runner := &sessionsV3RecordingProviderRunner{id: "anthropic", text: "Anthropic Titler"}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	settingsStore := pebblestore.NewAgentModelSettingsStore(modelStore)
	settings := testAgentModelSettingsRecord(testPrincipal().AccountScopeID)
	settings.SystemAgents.Compact = pebblestore.AgentModelAssignment{Provider: "anthropic", Model: "claude-sonnet-5-test", Thinking: "low"}
	if _, err = settingsStore.PutForAccount(settings); err != nil {
		t.Fatalf("set canonical Compact settings: %v", err)
	}
	server.SetAgentModelSettingsService(agentmodelsettings.NewService(settingsStore))

	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "compact-title-catalog-create", "compact title catalog", pebblestore.ModelPreference{Provider: "anthropic", Model: "claude-sonnet-5-test", Thinking: "high"})
	title, err := newSessionV3Executor(server).generateSessionV3CompactTitle(created, "User asked Anthropic Sonnet 5 to speak back.", testPrincipal())
	if err != nil {
		t.Fatalf("generate title: %v", err)
	}
	if title != "Anthropic Titler" {
		t.Fatalf("title = %q, want %q", title, "Anthropic Titler")
	}
	runner.mu.Lock()
	lastReq := runner.lastRequest
	runner.mu.Unlock()
	if lastReq.ModelCatalog == nil || lastReq.ToolChoice != "none" {
		t.Fatalf("Compact title request catalog=%T tool_choice=%q", lastReq.ModelCatalog, lastReq.ToolChoice)
	}
	if lastReq.Model != "claude-sonnet-5-test" || lastReq.Thinking != "low" || lastReq.ContextWindow != 200000 {
		t.Fatalf("Compact title settings model=%q thinking=%q context=%d", lastReq.Model, lastReq.Thinking, lastReq.ContextWindow)
	}
	if !strings.Contains(lastReq.Instructions, "Title-only case") || strings.Contains(lastReq.Instructions, "Required sections:") || strings.Contains(lastReq.Instructions, "Compaction mode:") {
		t.Fatalf("Compact title instructions mixed cases:\n%s", lastReq.Instructions)
	}
}
