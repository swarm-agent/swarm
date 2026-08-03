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
	allowStreaming bool
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
func (r *sessionRouterRecordingRunner) CreateResponseStreaming(ctx context.Context, request provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.streamingCalls++
	if r.allowStreaming {
		r.requests = append(r.requests, request)
		r.contexts = append(r.contexts, ctx)
		return r.response, r.err
	}
	return provideriface.Response{}, errors.New("streaming must not be used by Router")
}

func TestSessionRouterOnceUsesConfiguredToolFreeProviderWithoutWorkspaceContext(t *testing.T) {
	runner := &sessionRouterRecordingRunner{id: "recording", response: provideriface.Response{Text: `{"title":"Implement routing","worktree_name":"router-bridge"}`}}
	server, principal, entries := newSessionRouterTestServer(t, runner, []sessionRouterWorkspace{{"/workspace/sole", "Sole", "secret workspace definition"}})
	decision, err := server.routeSessionOnce(context.Background(), principal, "implement the Router bridge")
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
	if decision.Result.Title != "Implement routing" || decision.Result.WorktreeName == nil || *decision.Result.WorktreeName != "router-bridge" {
		t.Fatalf("decision = %+v", decision)
	}
	if decision.Profile.Name != "system-router" || decision.Profile.ToolContract == nil || len(decision.Profile.ToolContract.Tools) != 0 {
		t.Fatalf("compiled Router profile is not the tool-free system Router: %+v", decision.Profile)
	}
	request := runner.requests[0]
	if request.ToolChoice != "none" || len(request.Tools) != 0 || request.ToolInvoker != nil {
		t.Fatalf("Router provider request exposed tools: %+v", request)
	}
	for _, entry := range entries {
		for _, forbidden := range []string{entry.WorkspaceID, entry.Path, entry.Name, "secret workspace definition"} {
			if forbidden != "" && strings.Contains(request.Instructions, forbidden) {
				t.Fatalf("Router instructions leaked workspace material %q: %s", forbidden, request.Instructions)
			}
		}
	}
	if strings.Contains(request.Instructions, `"workspace_id"`) || strings.Contains(request.Instructions, `"worktree":`) || !strings.Contains(request.Instructions, `"worktree_name"`) {
		t.Fatalf("Router instructions do not use strict naming-only contract: %s", request.Instructions)
	}
	content, ok := request.Input[0]["content"].([]map[string]any)
	if !ok || len(content) != 1 || content[0]["text"] != "implement the Router bridge" {
		t.Fatalf("Router original user input = %#v", request.Input)
	}
}

func TestSessionRouterOnceReturnsProviderErrorWithoutRetry(t *testing.T) {
	providerErr := errors.New("provider unavailable")
	runner := &sessionRouterRecordingRunner{id: "recording", err: providerErr}
	server, principal, _ := newSessionRouterTestServer(t, runner, nil)
	_, err := server.routeSessionOnce(context.Background(), principal, "route this")
	if !errors.Is(err, providerErr) || runner.createCalls != 1 || runner.streamingCalls != 0 {
		t.Fatalf("provider error=%v create=%d streaming=%d", err, runner.createCalls, runner.streamingCalls)
	}
}

func TestSessionRouterOnceAcceptsSingleCompleteJSONFence(t *testing.T) {
	runner := &sessionRouterRecordingRunner{id: "recording", response: provideriface.Response{Text: "```json\n{\"title\":\"Implement routing\",\"worktree_name\":\"routing\"}\n```"}}
	server, principal, _ := newSessionRouterTestServer(t, runner, nil)
	if _, err := server.routeSessionOnce(context.Background(), principal, "implement the Router bridge"); err != nil {
		t.Fatalf("route fenced session once: %v", err)
	}
}

type sessionRouterWorkspace struct{ path, name, definition string }

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
	if err := catalogStore.SetRecord(pebblestore.ModelCatalogRecord{Provider: runner.id, Model: "router-model", ThinkingOptions: []string{"high"}, ServiceTiers: []string{"priority", "fast"}}); err != nil {
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
		pending, _ := workspaceStore.MarkDefinitionPendingForAccount(principal.AccountScopeID, entry.Path)
		entry, _, _ = workspaceStore.CompleteDefinitionForAccount(principal.AccountScopeID, entry.Path, pending.DefinitionGeneration, candidate.definition, 1)
		entries = append(entries, entry)
	}
	agentSettingsStore := pebblestore.NewAgentModelSettingsStore(store)
	agentSettings := testAgentModelSettingsRecord(principal.AccountScopeID)
	agentSettings.SystemAgents.Router = pebblestore.AgentModelAssignment{Provider: runner.id, Model: "router-model", Thinking: "high", ServiceTier: "priority"}
	if _, err := agentSettingsStore.PutForAccount(agentSettings); err != nil {
		t.Fatalf("set Router settings: %v", err)
	}
	agents := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	if err := agents.EnsureDefaults(); err != nil {
		t.Fatalf("ensure agents: %v", err)
	}
	providers := registry.New()
	providers.RegisterRunner(runner)
	return &Server{workspace: workspace.NewService(workspaceStore), providers: providers, agentModelSettings: agentmodelsettings.NewService(agentSettingsStore), model: modelService, agents: agents}, principal, entries
}
