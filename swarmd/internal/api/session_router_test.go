package api

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/modelprofile"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/uisettings"
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
	runner := &sessionRouterRecordingRunner{id: "recording", response: provideriface.Response{Text: `{"title":"Implement routing","mode":"plan","worktree":false}`}}
	server, principal, entries := newSessionRouterTestServer(t, runner, true, []sessionRouterWorkspace{{"/workspace/sole", "Sole", "Go API workspace"}})

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
	if decision.Result.Mode != "plan" || decision.Workspace.WorkspaceID != entries[0].WorkspaceID || decision.Workspace.WorkspacePath != "/workspace/sole" {
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
	if request.ToolChoice != "none" || len(request.Tools) != 0 || request.ToolInvoker != nil {
		t.Fatalf("Router provider request exposed tools: %+v", request)
	}
	if !strings.Contains(request.Instructions, `"plan"`) || !strings.Contains(request.Instructions, `"additionalProperties":false`) || !strings.Contains(request.Instructions, entries[0].WorkspaceID) {
		t.Fatalf("Router instructions missing enabled Plan/schema/bound workspace: %s", request.Instructions)
	}
	if len(request.Input) != 1 {
		t.Fatalf("Router input count = %d", len(request.Input))
	}
	content, ok := request.Input[0]["content"].([]map[string]any)
	if !ok || len(content) != 1 || content[0]["text"] != "implement the Router bridge" {
		t.Fatalf("Router original user input = %#v", request.Input)
	}
}

func TestSessionRouterOnceOffersMultipleWithoutDisabledPlan(t *testing.T) {
	runner := &sessionRouterRecordingRunner{id: "recording"}
	server, principal, entries := newSessionRouterTestServer(t, runner, false, []sessionRouterWorkspace{
		{"/workspace/alpha", "Alpha", "Frontend workspace"},
		{"/workspace/beta", "Beta", "Backend workspace"},
	})
	runner.response.Text = `{"title":"Fix backend","mode":"auto","workspace_id":"` + entries[1].WorkspaceID + `","worktree":true,"worktree_name":"backend-fix"}`

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
	if strings.Contains(instructions, `"plan"`) || !strings.Contains(instructions, entries[0].WorkspaceID) || !strings.Contains(instructions, entries[1].WorkspaceID) {
		t.Fatalf("disabled Plan or workspace choices encoded incorrectly: %s", instructions)
	}
}

func TestSessionRouterOnceReturnsProviderErrorWithoutRetryOrFallback(t *testing.T) {
	providerErr := errors.New("provider unavailable")
	runner := &sessionRouterRecordingRunner{id: "recording", err: providerErr}
	server, principal, _ := newSessionRouterTestServer(t, runner, false, []sessionRouterWorkspace{{"/workspace/sole", "Sole", "Sole workspace"}})

	_, err := server.routeSessionOnce(context.Background(), principal, "route this", false)
	if !errors.Is(err, providerErr) {
		t.Fatalf("route error = %v, want provider error", err)
	}
	if runner.createCalls != 1 || runner.streamingCalls != 0 {
		t.Fatalf("provider error caused retry/fallback: create=%d streaming=%d", runner.createCalls, runner.streamingCalls)
	}
}

func TestSessionRouterOnceRejectsZeroWorkspacesBeforeProvider(t *testing.T) {
	runner := &sessionRouterRecordingRunner{id: "recording", response: provideriface.Response{Text: `{"title":"Unexpected","mode":"auto","worktree":false}`}}
	server, principal, _ := newSessionRouterTestServer(t, runner, false, nil)

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

func newSessionRouterTestServer(t *testing.T, runner *sessionRouterRecordingRunner, planEnabled bool, candidates []sessionRouterWorkspace) (*Server, identity.Principal, []pebblestore.WorkspaceEntry) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "session-router.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "router-user", AccountScopeID: "router-account", AccountScopeSource: identity.AccountScopeSourceServerState}

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

	favoriteStore := pebblestore.NewModelProfileStore(store)
	favoriteService := modelprofile.NewService(favoriteStore)
	authorityContext := identity.ContextWithPrincipal(context.Background(), principal)
	actionFavorite, err := favoriteService.Create(authorityContext, modelprofile.Input{Name: "Action", Provider: "recording", Model: "action-model", Thinking: "medium"})
	if err != nil {
		t.Fatalf("create Action favorite: %v", err)
	}
	planFavoriteID := ""
	if planEnabled {
		planFavorite, createErr := favoriteService.Create(authorityContext, modelprofile.Input{Name: "Plan", Provider: "recording", Model: "plan-model", Thinking: "high"})
		if createErr != nil {
			t.Fatalf("create Plan favorite: %v", createErr)
		}
		planFavoriteID = planFavorite.ProfileID
	}
	swarmProfiles := modelprofile.NewSwarmService(pebblestore.NewSwarmModeSettingsStore(store), favoriteStore)
	if _, err := swarmProfiles.Put(authorityContext, modelprofile.SwarmSettingsInput{ActionFavoriteID: actionFavorite.ProfileID, PlanEnabled: planEnabled, PlanFavoriteID: planFavoriteID}); err != nil {
		t.Fatalf("put Swarm mode settings: %v", err)
	}

	uiSettings := uisettings.NewService(pebblestore.NewUISettingsStore(store))
	if _, err := uiSettings.SetForAccount(principal.AccountScopeID, uisettings.UISettings{Agents: uisettings.AgentSettings{Router: uisettings.CompactAgentSettings{
		Provider: "recording", Model: "router-model", Thinking: "high", ServiceTier: "priority",
	}}}); err != nil {
		t.Fatalf("set Router UI settings: %v", err)
	}
	providers := registry.New()
	providers.RegisterRunner(runner)
	return &Server{workspace: workspace.NewService(workspaceStore), providers: providers, uiSettings: uiSettings, swarmProfiles: swarmProfiles}, principal, entries
}
