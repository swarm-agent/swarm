package run

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/modelpolicy"
	"swarm/packages/swarmd/internal/provider/codex"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/todo"
	"swarm/packages/swarmd/internal/uisettings"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

func TestValidateAITaskV2OriginWorkspaceRoutesManagedWorktreeToCanonicalWorkspace(t *testing.T) {
	const (
		accountID   = "account-worktree-task"
		userID      = "user-worktree-task"
		workspaceID = "workspace-worktree-task"
	)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: userID, AccountScopeID: accountID}
	canonicalPath := t.TempDir()
	worktreePath := t.TempDir()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "workspace.pebble"))
	if err != nil {
		t.Fatalf("open workspace store: %v", err)
	}
	defer store.Close()
	workspaceSvc := workspaceruntime.NewService(pebblestore.NewWorkspaceStore(store))
	if _, err := workspaceSvc.AddForPrincipal(principal, canonicalPath, "canonical", "", true); err != nil {
		t.Fatalf("add canonical workspace: %v", err)
	}
	scope, err := workspaceSvc.ScopeForPathForPrincipal(principal, canonicalPath)
	if err != nil || !scope.Matched || scope.WorkspaceID == "" {
		t.Fatalf("resolve canonical workspace: scope=%#v err=%v", scope, err)
	}
	svc := &Service{workspace: workspaceSvc}
	parent := pebblestore.SessionSnapshot{
		ID: "origin-worktree", UserID: userID, AccountScopeID: accountID,
		WorkspacePath: worktreePath, WorktreeEnabled: true,
		Metadata: map[string]any{
			"swarm_v3_source_workspace_id":   scope.WorkspaceID,
			"swarm_v3_source_workspace_path": canonicalPath,
		},
	}
	task := pebblestore.WorkspaceTodoItem{UserID: userID, AccountScopeID: accountID, WorkspaceID: scope.WorkspaceID, WorkspacePath: canonicalPath}
	if err := svc.validateAITaskV2OriginWorkspace(parent, task); err != nil {
		t.Fatalf("validate managed-worktree origin: %v", err)
	}

	parent.Metadata["swarm_v3_source_workspace_id"] = workspaceID
	if err := svc.validateAITaskV2OriginWorkspace(parent, task); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched source workspace error = %v", err)
	}
}

func TestValidateAITaskDeployBindingRoutesManagedWorktreeToCanonicalWorkspace(t *testing.T) {
	const (
		accountID = "account-deploy-binding"
		userID    = "user-deploy-binding"
	)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: userID, AccountScopeID: accountID}
	canonicalPath := t.TempDir()
	worktreePath := t.TempDir()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "workspace.pebble"))
	if err != nil {
		t.Fatalf("open workspace store: %v", err)
	}
	defer store.Close()
	workspaceSvc := workspaceruntime.NewService(pebblestore.NewWorkspaceStore(store))
	if _, err := workspaceSvc.AddForPrincipal(principal, canonicalPath, "canonical", "", true); err != nil {
		t.Fatalf("add canonical workspace: %v", err)
	}
	scope, err := workspaceSvc.ScopeForPathForPrincipal(principal, canonicalPath)
	if err != nil || !scope.Matched || scope.WorkspaceID == "" {
		t.Fatalf("resolve canonical workspace: scope=%#v err=%v", scope, err)
	}
	svc := &Service{workspace: workspaceSvc}
	parent := pebblestore.SessionSnapshot{
		ID: "origin-worktree", UserID: userID, AccountScopeID: accountID,
		WorkspacePath: worktreePath, WorktreeEnabled: true,
		Metadata: map[string]any{
			"swarm_v3_source_workspace_id":   scope.WorkspaceID,
			"swarm_v3_source_workspace_path": canonicalPath,
		},
	}
	binding := &AITaskDeployBinding{UserID: userID, AccountScopeID: accountID, WorkspacePath: canonicalPath}
	if err := svc.validateAITaskDeployBinding(parent, binding); err != nil {
		t.Fatalf("validate managed-worktree binding: %v", err)
	}

	parent.Metadata["swarm_v3_source_workspace_id"] = "wrong-workspace"
	if err := svc.validateAITaskDeployBinding(parent, binding); err == nil || !strings.Contains(err.Error(), "account-owned") {
		t.Fatalf("mismatched source workspace error = %v", err)
	}
	parent.Metadata["swarm_v3_source_workspace_id"] = scope.WorkspaceID
	binding.AccountScopeID = "other-account"
	if err := svc.validateAITaskDeployBinding(parent, binding); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("mismatched account error = %v", err)
	}
}

func TestParseAITaskPreparationStrict(t *testing.T) {
	got, err := ParseAITaskPreparation(`{"title":"Fix sidebar","worktree_name":"Fix Sidebar"}`)
	if err != nil || got.Title != "Fix sidebar" || got.WorktreeName != "fix-sidebar" {
		t.Fatalf("got %#v, %v", got, err)
	}
	for _, raw := range []string{
		`{"title":"x","worktree_name":"y","prompt":"escape"}`,
		`{"title":"x","worktree_name":"y","session_id":"escape"}`,
		`{"title":"","worktree_name":"y"}`,
		`{"title":"x","worktree_name":""}`,
		`{"title":"x","worktree_name":"---"}`,
	} {
		if _, err := ParseAITaskPreparation(raw); err == nil {
			t.Fatalf("expected rejection for %s", raw)
		}
	}
}

type principalCapturingAITaskRunner struct {
	id                   string
	principal            identity.Principal
	request              provideriface.Request
	requests             []provideriface.Request
	responses            []string
	calls                int
	convertedServiceTier string
}

func (r *principalCapturingAITaskRunner) ID() string {
	if strings.TrimSpace(r.id) == "" {
		return "codex"
	}
	return r.id
}

func (r *principalCapturingAITaskRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	principal, ok := identity.PrincipalFromContext(ctx)
	if !ok {
		return provideriface.Response{}, identity.ErrPrincipalRequired
	}
	r.principal, r.request = principal, req
	r.requests = append(r.requests, req)
	r.calls++
	r.convertedServiceTier = codex.ToRequest(req).ServiceTier
	response := `{"title":"Fix trusted task","worktree_name":"fix-trusted-task"}`
	if len(r.responses) >= r.calls {
		response = r.responses[r.calls-1]
	}
	return provideriface.Response{Text: response}, nil
}

func (r *principalCapturingAITaskRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
	return r.CreateResponse(ctx, req)
}

func TestPrepareAITaskMetadataPropagatesTrustedPrincipalToCompact(t *testing.T) {
	svc, _, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	runner := &principalCapturingAITaskRunner{}
	svc.providers = registry.New()
	svc.providers.RegisterRunner(runner)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "test-user", AccountScopeID: "test-account", SessionID: "origin-session", AccountScopeSource: identity.AccountScopeSourceSession}
	settings, err := svc.uiSettings.GetForAccount(principal.AccountScopeID)
	if err != nil {
		t.Fatalf("read Compact settings: %v", err)
	}
	settings.Agents.Compact = uisettings.CompactAgentSettings{Provider: "codex", Model: "gpt-5.4", Thinking: "medium", ServiceTier: "fast"}
	if _, err = svc.uiSettings.SetForAccount(principal.AccountScopeID, settings); err != nil {
		t.Fatalf("set Compact settings: %v", err)
	}
	preparation, err := svc.PrepareAITaskMetadata(context.Background(), "task-1", "preserve this exact request", pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5.4", Thinking: "high"}, principal)
	if err != nil {
		t.Fatalf("prepare AI task metadata: %v", err)
	}
	if preparation.Title != "Fix trusted task" || preparation.WorktreeName != "fix-trusted-task" {
		t.Fatalf("preparation = %#v", preparation)
	}
	if runner.principal.UserID != principal.UserID || runner.principal.AccountScopeID != principal.AccountScopeID || runner.principal.SessionID != principal.SessionID {
		t.Fatalf("Compact principal = %#v, want %#v", runner.principal, principal)
	}
	if runner.request.Model != "gpt-5.4" || runner.request.Thinking != "medium" || runner.request.ServiceTier != "fast" {
		t.Fatalf("Compact configured preference not used: model=%q thinking=%q tier=%q", runner.request.Model, runner.request.Thinking, runner.request.ServiceTier)
	}
	catalog, ok := runner.request.ModelCatalog.(pebblestore.ModelCatalogRecord)
	if !ok || catalog.Provider != "codex" || catalog.Model != "gpt-5.4" {
		t.Fatalf("Compact request model catalog = %#v", runner.request.ModelCatalog)
	}
	fastMappingFound := false
	for _, mapping := range catalog.ServiceTierMappings {
		if mapping.Tier == "fast" && mapping.ProviderParameter == "service_tier" && mapping.ProviderValue == "priority" {
			fastMappingFound = true
			break
		}
	}
	if !fastMappingFound {
		t.Fatalf("Compact request fast service tier mapping missing: %#v", catalog.ServiceTierMappings)
	}
	if runner.convertedServiceTier != "priority" {
		t.Fatalf("Codex Compact service tier = %q, want catalog-mapped priority", runner.convertedServiceTier)
	}
	if runner.request.ToolChoice != "none" || len(runner.request.Tools) != 0 {
		t.Fatalf("Compact request gained tools: choice=%q tools=%#v", runner.request.ToolChoice, runner.request.Tools)
	}
	if runner.request.BoundaryReason != "ai_task_metadata" || !runner.request.ForceFreshProviderContext || runner.request.NativeContinuationAllowed {
		t.Fatalf("Compact request boundary = %#v", runner.request)
	}
	if !strings.Contains(runner.request.Instructions, "only title and worktree_name") || !strings.Contains(runner.request.Instructions, "preferably 3-5 words") || !strings.Contains(runner.request.Instructions, "not a hard word-count restriction") {
		t.Fatalf("Compact metadata instructions = %q", runner.request.Instructions)
	}
	if len(runner.request.Input) != 1 {
		t.Fatalf("Compact input = %#v", runner.request.Input)
	}
	if _, err := svc.PrepareAITaskMetadata(context.Background(), "task-2", "request", pebblestore.ModelPreference{Provider: "codex"}, identity.Principal{}); !errors.Is(err, identity.ErrPrincipalRequired) {
		t.Fatalf("untrusted preparation error = %v, want %v", err, identity.ErrPrincipalRequired)
	}
}

func TestPrepareAITaskMetadataRetryNamesTakenWorktreeInSecondPrompt(t *testing.T) {
	svc, _, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	runner := &principalCapturingAITaskRunner{responses: []string{
		`{"title":"First task title","worktree_name":"taken-worktree"}`,
		`{"title":"Alternate task title","worktree_name":"alternate-worktree"}`,
	}}
	svc.providers = registry.New()
	svc.providers.RegisterRunner(runner)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "test-user", AccountScopeID: "test-account"}

	first, err := svc.PrepareAITaskMetadata(context.Background(), "task-retry", "request", pebblestore.ModelPreference{Provider: "codex"}, principal)
	if err != nil {
		t.Fatalf("prepare first metadata: %v", err)
	}
	second, err := svc.PrepareAITaskMetadataRetry(context.Background(), "task-retry", "request", pebblestore.ModelPreference{Provider: "codex"}, principal, first.WorktreeName)
	if err != nil {
		t.Fatalf("prepare retry metadata: %v", err)
	}
	if runner.calls != 2 || second.WorktreeName != "alternate-worktree" {
		t.Fatalf("retry preparation=%#v calls=%d", second, runner.calls)
	}
	if len(runner.requests) != 2 || strings.Contains(runner.requests[0].Instructions, "already taken") || !strings.Contains(runner.requests[1].Instructions, `"taken-worktree" is already taken`) || !strings.Contains(runner.requests[1].Instructions, `do not return "taken-worktree" again`) {
		t.Fatalf("retry instructions=%#v", runner.requests)
	}
}

func TestPrepareAITaskMetadataAttachesSelectedCatalogForEveryRunnableModelProvider(t *testing.T) {
	for _, providerID := range []string{"anthropic", "codex", "fireworks", "google", "openai", "openrouter"} {
		t.Run(providerID, func(t *testing.T) {
			svc, _, cleanup := newTaskLaunchPermissionTestService(t)
			defer cleanup()

			_, _, utility, ok, err := svc.model.RecommendedCatalogDefaults(providerID)
			if err != nil || !ok || strings.TrimSpace(utility.Model) == "" {
				t.Fatalf("resolve %s utility catalog: ok=%t record=%#v err=%v", providerID, ok, utility, err)
			}
			thinking := ""
			if len(utility.ThinkingMappings) > 0 {
				thinking = utility.ThinkingMappings[0].SwarmSetting
			}
			if thinking == "" {
				thinking = utility.DefaultThinking
			}
			runner := &principalCapturingAITaskRunner{id: providerID}
			svc.providers = registry.New()
			svc.providers.RegisterRunner(runner)
			principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "test-user", AccountScopeID: "test-account-" + providerID}
			settings, err := svc.uiSettings.GetForAccount(principal.AccountScopeID)
			if err != nil {
				t.Fatalf("read Compact settings: %v", err)
			}
			settings.Agents.Compact = uisettings.CompactAgentSettings{Provider: providerID, Model: utility.Model, Thinking: thinking}
			if _, err = svc.uiSettings.SetForAccount(principal.AccountScopeID, settings); err != nil {
				t.Fatalf("set Compact settings: %v", err)
			}

			if _, err = svc.PrepareAITaskMetadata(context.Background(), "task-"+providerID, "request", pebblestore.ModelPreference{Provider: providerID, Model: utility.Model, Thinking: thinking}, principal); err != nil {
				t.Fatalf("prepare %s AI task metadata: %v", providerID, err)
			}
			catalog, ok := runner.request.ModelCatalog.(pebblestore.ModelCatalogRecord)
			if !ok || !strings.EqualFold(catalog.Provider, providerID) || catalog.Model != utility.Model {
				t.Fatalf("%s Compact catalog = %#v, want exact selected %s/%s record", providerID, runner.request.ModelCatalog, providerID, utility.Model)
			}
			if runner.request.Thinking != thinking {
				t.Fatalf("%s Compact thinking = %q, want catalog setting %q", providerID, runner.request.Thinking, thinking)
			}
		})
	}
}

func TestPrepareAITaskMetadataRejectsMissingSelectedModelCatalogBeforeDispatch(t *testing.T) {
	svc, _, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	runner := &principalCapturingAITaskRunner{}
	svc.providers = registry.New()
	svc.providers.RegisterRunner(runner)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "test-user", AccountScopeID: "test-account"}
	settings, err := svc.uiSettings.GetForAccount(principal.AccountScopeID)
	if err != nil {
		t.Fatalf("read Compact settings: %v", err)
	}
	settings.Agents.Compact = uisettings.CompactAgentSettings{Provider: "codex", Model: "missing-compact-model", Thinking: "medium", ServiceTier: "fast"}
	if _, err = svc.uiSettings.SetForAccount(principal.AccountScopeID, settings); err != nil {
		t.Fatalf("set Compact settings: %v", err)
	}

	_, err = svc.PrepareAITaskMetadata(context.Background(), "task-missing-catalog", "request", pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5.4", Thinking: "high"}, principal)
	if err == nil || !strings.Contains(err.Error(), `Compact model catalog record for provider "codex" model "missing-compact-model" is unavailable`) {
		t.Fatalf("missing catalog error = %v", err)
	}
	if runner.calls != 0 {
		t.Fatalf("provider dispatched %d times with missing selected-model catalog", runner.calls)
	}
}

func TestExecutePreparedAITaskWithoutOriginCreatesManagedWorktreeSessionAndDurableLinkage(t *testing.T) {
	svc, _, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	const (
		accountScopeID = "account-ai-task"
		userID         = "user-ai-task"
	)
	if _, _, _, err := svc.agents.RestoreDefaultsForAccount(accountScopeID); err != nil {
		t.Fatalf("restore account agents: %v", err)
	}
	if _, _, _, err := svc.agents.UpsertForAccount(accountScopeID, agentruntime.UpsertInput{
		Name: "swarm", Mode: agentruntime.ModePrimary, ModelMode: "split",
		PlanProvider: "codex", PlanModel: "configured-plan-model", PlanThinking: "high",
		AutoProvider: "openai", AutoModel: "configured-auto-model", AutoThinking: "medium",
		RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, Enabled: pebblestore.BoolPtr(true),
	}); err != nil {
		t.Fatalf("configure account Swarm: %v", err)
	}
	if _, _, _, err := svc.agents.UpsertForAccount(accountScopeID, agentruntime.UpsertInput{
		Name: "other-primary", Mode: agentruntime.ModePrimary, ModelMode: "split",
		PlanProvider: "codex", PlanModel: "other-plan-model", PlanThinking: "low",
		AutoProvider: "openai", AutoModel: "other-auto-model", AutoThinking: "high",
		Prompt: "Other primary.", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto,
		ToolContract: &pebblestore.AgentToolContract{Preset: "read_only"}, Enabled: pebblestore.BoolPtr(true),
	}); err != nil {
		t.Fatalf("configure alternate account primary: %v", err)
	}
	if _, _, _, err := svc.agents.ActivatePrimaryForAccount(accountScopeID, "other-primary"); err != nil {
		t.Fatalf("activate alternate account primary: %v", err)
	}
	state, err := svc.agents.ListStateForAccount(accountScopeID, 20)
	if err != nil {
		t.Fatalf("list account agents: %v", err)
	}
	if state.ActivePrimary != "other-primary" {
		t.Fatalf("active account primary = %q, profiles=%#v", state.ActivePrimary, state.Profiles)
	}
	resolvedSwarm, err := svc.agents.ResolveSystemAgent("swarm", pebblestore.AgentProfile{})
	if err != nil || !resolvedSwarm.Enabled || resolvedSwarm.Mode != agentruntime.ModePrimary {
		t.Fatalf("resolved Swarm = %#v err=%v", resolvedSwarm, err)
	}

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: userID, AccountScopeID: accountScopeID}
	workspacePath := t.TempDir()
	workspaceStore, err := pebblestore.Open(filepath.Join(t.TempDir(), "workspace.pebble"))
	if err != nil {
		t.Fatalf("open workspace store: %v", err)
	}
	defer workspaceStore.Close()
	workspaceSvc := workspaceruntime.NewService(pebblestore.NewWorkspaceStore(workspaceStore))
	if _, err := workspaceSvc.AddForPrincipal(principal, workspacePath, "AI task workspace", "", true); err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	scope, err := workspaceSvc.ScopeForPathForPrincipal(principal, workspacePath)
	if err != nil || !scope.Matched {
		t.Fatalf("resolve workspace scope: matched=%t err=%v", scope.Matched, err)
	}
	svc.SetWorkspaceService(workspaceSvc)

	parent, _, err := svc.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		UserID: userID, AccountScopeID: accountScopeID, Title: "AI task parent",
		WorkspacePath: workspacePath, WorkspaceName: "AI task workspace", Mode: sessionruntime.ModeAuto,
		Preference: &pebblestore.ModelPreference{Provider: "codex", Model: "configured-auto-model", Thinking: "high"},
	})
	if err != nil {
		t.Fatalf("create parent session: %v", err)
	}

	todoStore, err := pebblestore.Open(filepath.Join(t.TempDir(), "todo.pebble"))
	if err != nil {
		t.Fatalf("open todo store: %v", err)
	}
	defer todoStore.Close()
	todoEvents, err := pebblestore.NewEventLog(todoStore)
	if err != nil {
		t.Fatalf("open todo event log: %v", err)
	}
	todoSvc := todo.NewService(pebblestore.NewWorkspaceTodoStore(todoStore), todoEvents, nil, svc.sessions)
	queuedProfile := &pebblestore.SessionModelProfileSnapshot{Source: pebblestore.SessionModelProfileSourceSaved, Action: pebblestore.ModelProfileSelection{Provider: "openai", Model: "other-auto-model", Thinking: "high"}, Plan: &pebblestore.ModelProfileSelection{Provider: "codex", Model: "other-plan-model", Thinking: "high"}, AppliedAt: 1}
	queued, _, _, err := todoSvc.CreateAITask(todo.CreateAITaskInput{AccountScopeID: parent.AccountScopeID, UserID: parent.UserID, WorkspaceID: "workspace-test", WorkspacePath: workspacePath, ModelProfile: queuedProfile, Request: "Fix the queued task", Mode: sessionruntime.ModeAuto, IdempotencyKey: "request-1"})
	if err != nil {
		t.Fatalf("create queued AI task: %v", err)
	}
	svc.SetAITaskBinder(todoSvc)

	managedPath := filepath.Join(t.TempDir(), "managed-worktree")
	worktrees := &taskLaunchWorktreeStub{
		config: worktreeruntime.Config{WorkspacePath: workspacePath, BaseBranch: "release", BranchName: "feature/<id>"},
		allocation: worktreeruntime.Allocation{
			RepoRoot: workspacePath, WorkspacePath: managedPath, WorkspaceID: "ws_ai_task",
			BaseBranch: "release", BranchName: "feature/fix-queued-task",
		},
	}
	svc.SetWorktreeService(worktrees)
	var canonicalProfiles []pebblestore.AgentProfile
	svc.SetSessionDeployCanonicalizer(func(input SessionDeployCanonicalizeInput) (SessionDeployCanonicalization, error) {
		canonicalProfiles = append(canonicalProfiles, input.AgentProfile)
		metadata := input.Metadata
		metadata["swarm_v3_source_workspace_path"] = workspacePath
		metadata["swarm_v3_source_workspace_name"] = "AI task workspace"
		return SessionDeployCanonicalization{
			Metadata: metadata, SourceWorkspaceID: scope.WorkspaceID,
			SourceWorkspaceGeneration: scope.WorkspaceGeneration,
			SourceWorkspaceName:       "AI task workspace", SourceWorkspacePath: workspacePath,
			RuntimeWorkspacePath: workspacePath,
		}, nil
	})
	var enqueuedSessionID, enqueuedRunID, enqueuedParentID string
	svc.SetSessionDeployEnqueuer(func(got identity.Principal, sessionID, runID, parentSessionID string) bool {
		if got.UserID != userID || got.AccountScopeID != accountScopeID {
			t.Fatalf("enqueue principal = %#v", got)
		}
		enqueuedSessionID, enqueuedRunID, enqueuedParentID = sessionID, runID, parentSessionID
		return true
	})

	preparation := AITaskPreparation{Title: "Fix queued task", WorktreeName: "fix-queued-task"}
	if err := todoSvc.BindAITask(parent.AccountScopeID, workspacePath, queued.ID, "queued", "preparing", "", false, "", ""); err != nil {
		t.Fatalf("claim queued AI task: %v", err)
	}
	if _, err := svc.ExecutePreparedAITask(context.Background(), "", parent.UserID, parent.AccountScopeID, workspacePath, queued.ID, queued.AIRequest, queued.AIMode, queued.AIModelProfile, preparation, svc.sessions.ApplySessionMutation); err != nil {
		t.Fatalf("execute prepared AI task: %v", err)
	}

	linked, ok, err := pebblestore.NewWorkspaceTodoStore(todoStore).GetForAccount(parent.AccountScopeID, workspacePath, queued.ID)
	if err != nil || !ok {
		t.Fatalf("load linked task: ok=%t err=%v", ok, err)
	}
	if linked.AIState != pebblestore.WorkspaceTodoAIStateInProgress || !linked.InProgress || linked.AIMode != sessionruntime.ModeAuto || !linked.AIWorktree {
		t.Fatalf("task execution state = %#v", linked)
	}
	if linked.ManagedSessionID == "" || linked.ManagedSessionID != enqueuedSessionID || enqueuedRunID == "" || enqueuedParentID != linked.ManagedSessionID {
		t.Fatalf("task/run linkage: task=%q enqueue=%q/%q parent=%q", linked.ManagedSessionID, enqueuedSessionID, enqueuedRunID, enqueuedParentID)
	}
	managed, ok, err := svc.sessions.GetSession(linked.ManagedSessionID)
	if err != nil || !ok {
		t.Fatalf("load managed session: ok=%t err=%v", ok, err)
	}
	if !managed.WorktreeEnabled || managed.WorkspacePath != managedPath || managed.WorktreeBaseBranch != "release" || managed.WorktreeBranch != "feature/fix-queued-task" {
		t.Fatalf("managed worktree session = %#v", managed)
	}
	if managed.Mode != sessionruntime.ModeAuto || managed.Preference.Provider != "openai" || managed.Preference.Model != "other-auto-model" || managed.Preference.Thinking != "high" {
		t.Fatalf("active split-primary auto session = %#v", managed)
	}
	if len(canonicalProfiles) == 0 || canonicalProfiles[0].Name != "other-primary" || canonicalProfiles[0].Mode != agentruntime.ModePrimary || canonicalProfiles[0].RuntimeMode != pebblestore.AgentRuntimeModePlanAuto || canonicalProfiles[0].ToolContract == nil {
		t.Fatalf("AI task active split execution profile = %#v", canonicalProfiles)
	}
	if worktrees.requestedBase != "release" || worktrees.requestedBranch != "feature/fix-queued-task" {
		t.Fatalf("worktree allocation used base=%q branch=%q", worktrees.requestedBase, worktrees.requestedBranch)
	}
	if mapString(managed.Metadata, "ai_task_id") != queued.ID || mapString(managed.Metadata, "ai_task_workspace_path") != workspacePath {
		t.Fatalf("reciprocal AI task metadata = %#v", managed.Metadata)
	}
	if mapString(managed.Metadata, "parent_session_id") != "" || mapString(managed.Metadata, "lineage_kind") != "" {
		t.Fatalf("sessionless AI task gained origin lineage = %#v", managed.Metadata)
	}
	messages, err := svc.sessions.ListSessionMessages(managed.ID, 0, 10)
	if err != nil || len(messages) != 1 || messages[0].Content != queued.AIRequest {
		t.Fatalf("managed prompt messages=%#v err=%v", messages, err)
	}
	firstSessionID := managed.ID
	if _, err := svc.ExecutePreparedAITask(context.Background(), "", parent.UserID, parent.AccountScopeID, workspacePath, queued.ID, queued.AIRequest, queued.AIMode, queued.AIModelProfile, preparation, svc.sessions.ApplySessionMutation); err != nil {
		t.Fatalf("replay prepared AI task: %v", err)
	}
	if enqueuedSessionID != firstSessionID {
		t.Fatalf("replay created a different visible session: first=%q replay=%q", firstSessionID, enqueuedSessionID)
	}

	originModelProfile := &pebblestore.SessionModelProfileSnapshot{
		Source:             pebblestore.SessionModelProfileSourceSaved,
		UseAccountDefault:  false,
		ActionFavoriteID:   "favorite-action",
		ActionFavoriteName: "Action Favorite",
		Action:              pebblestore.ModelProfileSelection{Provider: "openai", Model: "saved-auto-model", Thinking: "medium", ServiceTier: "flex", ContextMode: "compact"},
		PlanFavoriteID:     "favorite-plan",
		PlanFavoriteName:   "Plan Favorite",
		Plan:                &pebblestore.ModelProfileSelection{Provider: "codex", Model: "saved-plan-model", Thinking: "xhigh", ServiceTier: "fast", ContextMode: "full"},
		AppliedAt:           77,
	}
	linkedOriginTask, _, _, err := todoSvc.CreateAITask(todo.CreateAITaskInput{AccountScopeID: parent.AccountScopeID, UserID: parent.UserID, WorkspaceID: "workspace-test", WorkspacePath: workspacePath, OriginSessionID: parent.ID, ModelProfile: originModelProfile, Request: "Fix a linked task", Mode: sessionruntime.ModePlan, IdempotencyKey: "request-2"})
	if err != nil {
		t.Fatalf("create origin-linked AI task: %v", err)
	}
	if err := todoSvc.BindAITask(parent.AccountScopeID, workspacePath, linkedOriginTask.ID, "queued", "preparing", "", false, "", ""); err != nil {
		t.Fatalf("claim origin-linked AI task: %v", err)
	}
	if _, err := svc.ExecutePreparedAITask(context.Background(), parent.ID, parent.UserID, parent.AccountScopeID, workspacePath, linkedOriginTask.ID, linkedOriginTask.AIRequest, linkedOriginTask.AIMode, linkedOriginTask.AIModelProfile, AITaskPreparation{Title: "Fix linked task", WorktreeName: "fix-linked-task"}, svc.sessions.ApplySessionMutation); err != nil {
		t.Fatalf("execute origin-linked AI task: %v", err)
	}
	originLinkedSession, ok, err := svc.sessions.GetSession(enqueuedSessionID)
	if err != nil || !ok {
		t.Fatalf("load origin-linked managed session: ok=%t err=%v", ok, err)
	}
	if originLinkedSession.Mode != sessionruntime.ModePlan {
		t.Fatalf("origin-linked managed session mode = %q, want plan", originLinkedSession.Mode)
	}
	if originLinkedSession.Preference.Provider != "codex" || originLinkedSession.Preference.Model != "saved-plan-model" || originLinkedSession.Preference.Thinking != "xhigh" || originLinkedSession.Preference.ServiceTier != "fast" || originLinkedSession.Preference.ContextMode != "full" {
		t.Fatalf("saved model-profile plan session = %#v", originLinkedSession)
	}
	if originLinkedSession.ModelProfile == nil || originLinkedSession.ModelProfile.Plan == nil || originLinkedSession.ModelProfile.ActionFavoriteID != "favorite-action" || originLinkedSession.ModelProfile.Action.Model != "saved-auto-model" || originLinkedSession.ModelProfile.Plan.Model != "saved-plan-model" {
		t.Fatalf("saved model profile was not persisted on child: child=%#v origin=%#v", originLinkedSession.ModelProfile, originModelProfile)
	}
	transition, transitionErr := modelpolicy.ResolveModeTransition(originLinkedSession, pebblestore.AgentProfile{Name: "other-primary"}, sessionruntime.ModeAuto, func(preference pebblestore.ModelPreference) (modelpolicy.ResolvedPreference, error) {
		return modelpolicy.ResolvedPreference{Preference: preference, ContextWindow: 180000, MaxOutputTokens: 12000}, nil
	})
	if transitionErr != nil {
		t.Fatalf("resolve child plan-to-auto transition: %v", transitionErr)
	}
	if transition.Preference.Provider != "openai" || transition.Preference.Model != "saved-auto-model" || transition.Preference.Thinking != "medium" || transition.Preference.ServiceTier != "flex" || transition.Preference.ContextMode != "compact" || transition.AgentModelPolicy.Source != "saved_model_profile" || !transition.AgentModelPolicy.Locked {
		t.Fatalf("saved model-profile auto transition = %#v", transition)
	}
	if enqueuedParentID != parent.ID || mapString(originLinkedSession.Metadata, "parent_session_id") != parent.ID || mapString(originLinkedSession.Metadata, "lineage_kind") != "session_deploy" {
		t.Fatalf("optional origin linkage was not preserved: parent=%q metadata=%#v", enqueuedParentID, originLinkedSession.Metadata)
	}

	planDisabled := &pebblestore.SessionModelProfileSnapshot{Source: pebblestore.SessionModelProfileSourceSaved, Action: pebblestore.ModelProfileSelection{Provider: "anthropic", Model: "action-only", Thinking: "low"}, AppliedAt: 88}
	disabledTask, _, _, createErr := todoSvc.CreateAITask(todo.CreateAITaskInput{AccountScopeID: parent.AccountScopeID, UserID: parent.UserID, WorkspaceID: "workspace-test", WorkspacePath: workspacePath, ModelProfile: planDisabled, Request: "reject disabled Plan", Mode: sessionruntime.ModePlan, IdempotencyKey: "plan-disabled"})
	if createErr != nil {
		t.Fatalf("create Plan-disabled task: %v", createErr)
	}
	if bindErr := todoSvc.BindAITask(parent.AccountScopeID, workspacePath, disabledTask.ID, "queued", "preparing", "", false, "", ""); bindErr != nil {
		t.Fatalf("claim Plan-disabled task: %v", bindErr)
	}
	if _, executeErr := svc.ExecutePreparedAITask(context.Background(), "", parent.UserID, parent.AccountScopeID, workspacePath, disabledTask.ID, disabledTask.AIRequest, disabledTask.AIMode, disabledTask.AIModelProfile, AITaskPreparation{Title: "Disabled Plan", WorktreeName: "disabled-plan"}, svc.sessions.ApplySessionMutation); executeErr == nil || !strings.Contains(executeErr.Error(), "Plan mode disabled") {
		t.Fatalf("Plan-disabled execution error = %v", executeErr)
	}
}
