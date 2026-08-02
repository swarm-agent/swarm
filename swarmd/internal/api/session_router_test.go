package api

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	modelruntime "swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/provider/codex"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
)

type sessionRouterRecordingRunner struct {
	id             string
	response       provideriface.Response
	err            error
	createCalls    int
	streamingCalls int
	requests       []provideriface.Request
	contexts       []context.Context
}

func (r *sessionRouterRecordingRunner) ID() string { return r.id }

func (r *sessionRouterRecordingRunner) CreateResponse(ctx context.Context, request provideriface.Request) (provideriface.Response, error) {
	r.createCalls++
	r.requests = append(r.requests, request)
	r.contexts = append(r.contexts, ctx)
	return r.response, r.err
}

func (r *sessionRouterRecordingRunner) CreateResponseStreaming(_ context.Context, _ provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.streamingCalls++
	return provideriface.Response{}, errors.New("streaming must not be used by Router")
}

func TestSessionRouterOnceUsesConfiguredToolFreeProviderAndServerBoundWorkspace(t *testing.T) {
	runner := &sessionRouterRecordingRunner{id: "recording", response: provideriface.Response{Text: `{"title":"Implement routing"}`}}
	server, principal, entries := newSessionRouterTestServer(t, runner, []sessionRouterWorkspace{{"/workspace/sole", "Sole", "Go API workspace"}})

	decision, err := server.routeSessionOnce(context.Background(), principal, "implement the Router bridge", false)
	if err != nil {
		t.Fatalf("route session once: %v", err)
	}
	if runner.createCalls != 1 || runner.streamingCalls != 0 {
		t.Fatalf("provider calls create=%d streaming=%d, want 1/0", runner.createCalls, runner.streamingCalls)
	}
	providerPrincipal, ok := identity.PrincipalFromContext(runner.contexts[0])
	if !ok || !reflect.DeepEqual(providerPrincipal, principal) {
		t.Fatalf("provider principal = %+v, ok=%v; want %+v", providerPrincipal, ok, principal)
	}
	if decision.Result.Title != "Implement routing" || decision.Workspace.WorkspaceID != entries[0].WorkspaceID || decision.Workspace.WorkspacePath != "/workspace/sole" {
		t.Fatalf("decision = %+v", decision)
	}
	if decision.Profile.Name != "system-router" || decision.Profile.Provider != "recording" || decision.Profile.Model != "router-model" || decision.Profile.Thinking != "high" || decision.Profile.AutoServiceTier != "priority" {
		t.Fatalf("compiled Router profile = %+v", decision.Profile)
	}
	if decision.Profile.ToolContract == nil || len(decision.Profile.ToolContract.Tools) != 0 {
		t.Fatalf("compiled Router profile is not tool-free: %+v", decision.Profile.ToolContract)
	}

	request := runner.requests[0]
	if request.Model != "router-model" || request.Thinking != "high" || request.ServiceTier != "priority" {
		t.Fatalf("provider settings = model %q thinking %q tier %q", request.Model, request.Thinking, request.ServiceTier)
	}
	catalog, ok := request.ModelCatalog.(pebblestore.ModelCatalogRecord)
	if !ok || catalog.Provider != "recording" || catalog.Model != "router-model" {
		t.Fatalf("Router model catalog = %#v, want exact recording/router-model record", request.ModelCatalog)
	}
	if request.ToolChoice != "none" || len(request.Tools) != 0 || request.ToolInvoker != nil {
		t.Fatalf("Router provider request exposed tools: %+v", request)
	}
	if strings.Contains(strings.ToLower(request.Instructions), `"mode"`) || strings.Contains(strings.ToLower(request.Instructions), `"plan"`) || !strings.Contains(request.Instructions, `"additionalProperties":false`) || !strings.Contains(request.Instructions, entries[0].WorkspaceID) {
		t.Fatalf("Router instructions leaked mode authority or omitted schema/bound workspace: %s", request.Instructions)
	}
	if strings.Contains(request.Instructions, "worktree") {
		t.Fatalf("unauthorized Router instructions include worktree contract: %s", request.Instructions)
	}
	if len(request.Input) != 1 {
		t.Fatalf("Router input count = %d", len(request.Input))
	}
	content, ok := request.Input[0]["content"].([]map[string]any)
	if !ok || len(content) != 1 || content[0]["text"] != "implement the Router bridge" {
		t.Fatalf("Router original user input = %#v", request.Input)
	}
}

func TestSessionRouterOnceAcceptsSingleCompleteJSONFence(t *testing.T) {
	runner := &sessionRouterRecordingRunner{id: "recording", response: provideriface.Response{Text: "```json\n{\"title\":\"Implement routing\"}\n```"}}
	server, principal, _ := newSessionRouterTestServer(t, runner, []sessionRouterWorkspace{{"/workspace/sole", "Sole", "Go API workspace"}})

	decision, err := server.routeSessionOnce(context.Background(), principal, "implement the Router bridge", false)
	if err != nil {
		t.Fatalf("route fenced session once: %v", err)
	}
	if decision.Result.Title != "Implement routing" {
		t.Fatalf("fenced Router decision = %+v", decision)
	}
}

func TestNormalizeConfiguredRouterResultRejectsPartialOrCommentaryFences(t *testing.T) {
	for _, raw := range []string{
		"```json\n{\"title\":\"x\"}",
		"commentary\n```json\n{\"title\":\"x\"}\n```",
		"```json\n{\"title\":\"x\"}\n```\ncommentary",
	} {
		if got := normalizeConfiguredRouterJSONResponse(raw); got != strings.TrimSpace(raw) {
			t.Fatalf("normalized non-exact fence %q to %q", raw, got)
		}
	}
}

func TestSessionRouterOnceAttachesSelectedCatalogAcrossConfiguredProviders(t *testing.T) {
	for _, providerID := range []string{"anthropic", "codex", "fireworks", "google", "openai", "openrouter"} {
		t.Run(providerID, func(t *testing.T) {
			runner := &sessionRouterRecordingRunner{id: providerID, response: provideriface.Response{Text: `{"title":"Implement routing"}`}}
			server, principal, _ := newSessionRouterTestServer(t, runner, []sessionRouterWorkspace{{"/workspace/sole", "Sole", "Sole workspace"}})

			if _, err := server.routeSessionOnce(context.Background(), principal, "route this", false); err != nil {
				t.Fatalf("route session once: %v", err)
			}
			catalog, ok := runner.requests[0].ModelCatalog.(pebblestore.ModelCatalogRecord)
			if !ok || catalog.Provider != providerID || catalog.Model != "router-model" {
				t.Fatalf("Router model catalog = %#v, want exact %s/router-model record", runner.requests[0].ModelCatalog, providerID)
			}
		})
	}
}

func TestSessionRouterOnceUsesCatalogTranslationForConfiguredCodexModel(t *testing.T) {
	runner := &sessionRouterRecordingRunner{id: "codex", response: provideriface.Response{Text: `{"title":"Implement routing"}`}}
	server, principal, _ := newSessionRouterTestServer(t, runner, []sessionRouterWorkspace{{"/workspace/sole", "Sole", "Sole workspace"}})
	settings, err := server.agentModelSettings.GetForAccount(principal.AccountScopeID)
	if err != nil {
		t.Fatalf("get canonical Router settings: %v", err)
	}
	settings.SystemAgents.Router.ServiceTier = "fast"
	if _, err := server.agentModelSettings.UpdateSystemAgent(identity.ContextWithPrincipal(context.Background(), principal), pebblestore.SystemAgentRouter, settings.SystemAgents.Router); err != nil {
		t.Fatalf("set Router fast tier: %v", err)
	}

	if _, err := server.routeSessionOnce(context.Background(), principal, "route this", false); err != nil {
		t.Fatalf("route session once: %v", err)
	}
	request := runner.requests[0]
	if request.ServiceTier != "fast" {
		t.Fatalf("Router service tier = %q, want fast", request.ServiceTier)
	}
	if got := codex.ToRequest(request).ServiceTier; got != "priority" {
		t.Fatalf("Codex Router service tier = %q, want catalog-mapped priority", got)
	}
}

func TestSessionRouterOnceRejectsUnresolvedConfiguredModelBeforeProvider(t *testing.T) {
	runner := &sessionRouterRecordingRunner{id: "recording", response: provideriface.Response{Text: `{"title":"Unexpected"}`}}
	server, principal, _ := newSessionRouterTestServer(t, runner, []sessionRouterWorkspace{{"/workspace/sole", "Sole", "Sole workspace"}})
	settings, err := server.agentModelSettings.GetForAccount(principal.AccountScopeID)
	if err != nil {
		t.Fatalf("get canonical Router settings: %v", err)
	}
	settings.SystemAgents.Router.Model = "missing-router-model"
	if _, err := server.agentModelSettings.UpdateSystemAgent(identity.ContextWithPrincipal(context.Background(), principal), pebblestore.SystemAgentRouter, settings.SystemAgents.Router); err != nil {
		t.Fatalf("set missing Router model: %v", err)
	}

	_, err = server.routeSessionOnce(context.Background(), principal, "route this", false)
	if err == nil || !strings.Contains(err.Error(), `Router model catalog record for provider "recording" model "missing-router-model" is unavailable`) {
		t.Fatalf("missing Router model error = %v", err)
	}
	if runner.createCalls != 0 {
		t.Fatalf("missing Router model reached provider: create=%d", runner.createCalls)
	}
}

func TestSessionRouterOnceOffersWorktreeAndMultipleWorkspaces(t *testing.T) {
	runner := &sessionRouterRecordingRunner{id: "recording"}
	server, principal, entries := newSessionRouterTestServer(t, runner, []sessionRouterWorkspace{
		{"/workspace/alpha", "Alpha", "Frontend workspace"},
		{"/workspace/beta", "Beta", "Backend workspace"},
	})
	runner.response.Text = `{"title":"Fix backend","workspace_id":"` + entries[1].WorkspaceID + `","worktree":true,"worktree_name":"backend-fix"}`

	decision, err := server.routeSessionOnce(context.Background(), principal, "fix the backend", true)
	if err != nil {
		t.Fatalf("route session once: %v", err)
	}
	if decision.Workspace.WorkspaceID != entries[1].WorkspaceID || decision.Result.WorktreeName == nil || *decision.Result.WorktreeName != "backend-fix" {
		t.Fatalf("decision = %+v", decision)
	}
	if runner.createCalls != 1 || runner.streamingCalls != 0 {
		t.Fatalf("provider calls create=%d streaming=%d", runner.createCalls, runner.streamingCalls)
	}
	instructions := runner.requests[0].Instructions
	if !strings.Contains(instructions, "explicitly authorized a managed worktree") || !strings.Contains(instructions, `"worktree_name"`) {
		t.Fatalf("authorized Router instructions omitted worktree naming contract: %s", instructions)
	}
	if strings.Contains(strings.ToLower(instructions), `"mode"`) || strings.Contains(strings.ToLower(instructions), `"plan"`) || !strings.Contains(instructions, entries[0].WorkspaceID) || !strings.Contains(instructions, entries[1].WorkspaceID) {
		t.Fatalf("mode authority leaked or workspace choices encoded incorrectly: %s", instructions)
	}
}

func TestSessionRouterOnceReturnsProviderErrorWithoutRetryOrFallback(t *testing.T) {
	providerErr := errors.New("provider unavailable")
	runner := &sessionRouterRecordingRunner{id: "recording", err: providerErr}
	server, principal, _ := newSessionRouterTestServer(t, runner, []sessionRouterWorkspace{{"/workspace/sole", "Sole", "Sole workspace"}})

	_, err := server.routeSessionOnce(context.Background(), principal, "route this", false)
	if !errors.Is(err, providerErr) {
		t.Fatalf("route error = %v, want provider error", err)
	}
	if runner.createCalls != 1 || runner.streamingCalls != 0 {
		t.Fatalf("provider error caused retry/fallback: create=%d streaming=%d", runner.createCalls, runner.streamingCalls)
	}
}

func TestSessionRouterOnceRejectsZeroWorkspacesBeforeProvider(t *testing.T) {
	runner := &sessionRouterRecordingRunner{id: "recording", response: provideriface.Response{Text: `{"title":"Unexpected"}`}}
	server, principal, _ := newSessionRouterTestServer(t, runner, nil)

	if _, err := server.routeSessionOnce(context.Background(), principal, "route this", false); !errors.Is(err, workspace.ErrNoRoutableWorkspaces) {
		t.Fatalf("zero workspace error = %v", err)
	}
	if runner.createCalls != 0 || runner.streamingCalls != 0 {
		t.Fatalf("zero workspaces reached provider: create=%d streaming=%d", runner.createCalls, runner.streamingCalls)
	}
}

type sessionRouterWorkspace struct {
	path       string
	name       string
	definition string
}

func newSessionRouterTestServer(t *testing.T, runner *sessionRouterRecordingRunner, candidates []sessionRouterWorkspace) (*Server, identity.Principal, []pebblestore.WorkspaceEntry) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "session-router.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "router-user", AccountScopeID: "router-account", AccountScopeSource: identity.AccountScopeSourceServerState}
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("create event log: %v", err)
	}
	catalogStore := pebblestore.NewModelCatalogStore(store)
	if err := catalogStore.SetRecord(pebblestore.ModelCatalogRecord{
		Provider: runner.id, Model: "router-model", ThinkingOptions: []string{"high"}, ServiceTiers: []string{"priority", "fast"},
		ServiceTierMappings: []pebblestore.ModelCatalogServiceTierMapping{{Tier: "fast", SwarmSetting: "fast", ProviderParameter: "service_tier", ProviderValue: "priority"}},
	}); err != nil {
		t.Fatalf("seed Router model catalog: %v", err)
	}
	modelService := modelruntime.NewService(pebblestore.NewModelStore(store), events, modelruntime.NewCatalogService(catalogStore))

	workspaceStore := pebblestore.NewWorkspaceStore(store)
	entries := make([]pebblestore.WorkspaceEntry, 0, len(candidates))
	for _, candidate := range candidates {
		entry, addErr := workspaceStore.AddForAccount(principal.AccountScopeID, candidate.path, candidate.name)
		if addErr != nil {
			t.Fatalf("add workspace: %v", addErr)
		}
		pending, pendingErr := workspaceStore.MarkDefinitionPendingForAccount(principal.AccountScopeID, entry.Path)
		if pendingErr != nil {
			t.Fatalf("mark definition pending: %v", pendingErr)
		}
		entry, current, completeErr := workspaceStore.CompleteDefinitionForAccount(principal.AccountScopeID, entry.Path, pending.DefinitionGeneration, candidate.definition, 1)
		if completeErr != nil || !current {
			t.Fatalf("complete definition current=%v err=%v", current, completeErr)
		}
		entries = append(entries, entry)
	}

	agentSettingsStore := pebblestore.NewAgentModelSettingsStore(store)
	agentSettings := testAgentModelSettingsRecord(principal.AccountScopeID)
	agentSettings.SystemAgents.Router = pebblestore.AgentModelAssignment{Provider: runner.id, Model: "router-model", Thinking: "high", ServiceTier: "priority"}
	if _, err := agentSettingsStore.PutForAccount(agentSettings); err != nil {
		t.Fatalf("set canonical Router settings: %v", err)
	}
	agentSettingsService := agentmodelsettings.NewService(agentSettingsStore)
	agents := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	if err := agents.EnsureDefaults(); err != nil {
		t.Fatalf("ensure compiled agent defaults: %v", err)
	}
	providers := registry.New()
	providers.RegisterRunner(runner)
	return &Server{workspace: workspace.NewService(workspaceStore), providers: providers, agentModelSettings: agentSettingsService, model: modelService, agents: agents}, principal, entries
}
