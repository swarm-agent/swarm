package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"os/exec"
	"reflect"
	"strings"
	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	sessions "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
	"swarm/packages/swarmd/internal/worktree"
)

// Purpose: schema-constrained provider dispatch -> pending review -> edited
// explicit acceptance -> scheduler -> execution host -> canonical plan
// lifecycle/run intents must execute each immutable slot once in an isolated
// session. Prevent author-chat restarts, lost wake recovery, snapshot drift and
// false success. Temp Pebble/Git and an executor callback are the narrowest
// hermetic proof; the callback executes the real checkpoint lifecycle, not a
// recording-only runtime. Provider/browser behavior is deliberately not claimed.
func TestAutomationV2ScheduledCheckpointExecution(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "dev"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "fixture"}} {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ids := store.NewIdentityStore(db)
	if _, err = ids.PutUser(store.UserRecord{ID: "owner", Username: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ids.PutAccountScope(store.AccountScopeRecord{ID: "account", Type: store.AccountScopeTypePersonal, CreatedByUserID: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ids.PutAccountUser(store.AccountUserRecord{ID: "member", AccountScopeID: "account", UserID: "owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	ws := workspace.NewService(store.NewWorkspaceStore(db))
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "owner"}
	w, err := ws.AddForPrincipal(p, repo, "fixture", "", false)
	if err != nil {
		t.Fatal(err)
	}
	ss := store.NewSessionStore(db)
	if err = ss.CompleteRepositoryHistoryMaintenance(ctx); err != nil {
		t.Fatal(err)
	}
	yes := true
	if err = ss.CreateSession(store.SessionSnapshot{ID: "author", AccountScopeID: "account", UserID: "owner", Mode: "auto", WorkspacePath: repo, WorkspaceGrants: []store.WorkspaceGrant{{Kind: store.WorkspaceGrantPrimary, WorkspaceID: w.WorkspaceID, Path: repo, Available: &yes}}}); err != nil {
		t.Fatal(err)
	}
	events, err := store.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	service := sessions.NewService(ss, events)
	doc := store.SessionPlanDocument{Title: "Harmless report", Info: store.SessionPlanInfo{Goal: "Return the exact accepted instruction"}, WorkerV2: &store.AutomationV2Settings{SchemaVersion: 2, Schedule: store.AutomationV2Schedule{Kind: "interval", IntervalSeconds: 60}, Missed: "coalesce", Overlap: "independent", ActivateOnAccept: true}, Checkpoints: []store.SessionPlanCheckpoint{{ID: "report", Title: "Report", Status: "pending", Order: 1, Tasks: []string{"Return ONLY: fixture acknowledged"}, AcceptanceCriteria: []string{"Exact fixture phrase returned"}}}}
	permissions := permission.NewService(store.NewPermissionStore(db), events, nil)
	permissions.SetBypassPermissions(true)
	authoring := NewService(service, nil, nil, tool.NewRuntime(1), permissions, nil, nil, events)
	profile := agent.SwarmAgentProfileForContext(store.AgentProfile{})
	_, policy, disabled, err := authoring.compileResolvedAgentToolContract("account", profile)
	if err != nil {
		t.Fatal(err)
	}
	args, err := json.Marshal(map[string]any{"action": "propose", "document": doc})
	if err != nil {
		t.Fatal(err)
	}
	var instance any
	if err = json.Unmarshal(args, &instance); err != nil {
		t.Fatal(err)
	}
	// Provider JSON omits unspecified expiration rather than serializing a Go
	// zero-value struct; the advertised optional field must normalize indefinite.
	delete(instance.(map[string]any)["document"].(map[string]any)["worker_v2"].(map[string]any), "expiration")
	args, err = json.Marshal(instance)
	if err != nil {
		t.Fatal(err)
	}
	validated := false
	for _, definition := range filterToolDefinitions(convertToolDefinitions(authoring.ListAgentToolDefinitionsForAccount("account")), disabled) {
		if definition.Name != "manage_workers" {
			continue
		}
		raw, _ := json.Marshal(definition.Parameters)
		var schema jsonschema.Schema
		if err = json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		resolved, resolveErr := schema.Resolve(nil)
		if resolveErr != nil {
			t.Fatal(resolveErr)
		}
		if err = resolved.Validate(instance); err != nil {
			t.Fatal(err)
		}
		validated = true
	}
	if !validated {
		t.Fatal("actual primary tool schema missing")
	}
	if _, found, err := service.GetActivePlan("author"); err != nil || found {
		t.Fatal("fixture must start without a plan", err)
	}
	if rows, _, err := service.ListAutomationV2Records("account", "owner", w.WorkspaceID, "", 20); err != nil || len(rows) != 0 {
		t.Fatal("fixture must start without automation", err)
	}
	invoker := authoring.newProviderToolInvoker(providerToolInvokerConfig{sessionID: "author", principal: p, sessionMode: "auto", runID: "authoring", providerManagedV3: true, applySessionMutation: service.ApplySessionMutation, agentProfile: profile, policy: policy, terminalPlanState: &terminalPlanToolState{}})
	result, err := invoker.ExecuteTool(ctx, provideriface.ToolInvocation{Name: "manage_workers", CallID: "fresh-proposal", Arguments: string(args)})
	if err != nil || result.Error != "" || !result.RestartTurn {
		t.Fatalf("fresh dispatch: %+v %v", result, err)
	}
	proposal, found, err := service.GetAutomationV2Proposal("account", "owner", w.WorkspaceID, "author")
	if err != nil || !found {
		t.Fatal("pending review missing", err)
	}
	reviews, err := store.NewPermissionStore(db).ListPendingPermissions("author", 10)
	if err != nil || len(reviews) != 1 || reviews[0].Requirement != "automation_v2_acceptance" {
		t.Fatal("canonical permission missing", err)
	}
	if rows, _, err := service.ListAutomationV2Records("account", "owner", w.WorkspaceID, "", 20); err != nil || len(rows) != 0 {
		t.Fatal("proposal created automation", err)
	}
	if _, found, err := ss.GetV3SessionActiveRunIntent("author"); err != nil || found {
		t.Fatal("proposal started authoring plan", err)
	}
	// Same canonical revision boundary used by the editable card. This is not a
	// browser gesture: the browser-to-server proof remains a separate live gate.
	oldReview := proposal.AutomationV2Review
	doc.Checkpoints[0].Tasks = []string{"Return ONLY: user-edited fixture acknowledged"}
	doc.WorkerV2.Schedule.IntervalSeconds = 120
	proposal, err = service.ProposeAutomationV2("account", "owner", w.WorkspaceID, "author", &doc, oldReview)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.AcceptAutomationV2("account", "owner", w.WorkspaceID, "author", oldReview); !errors.Is(err, store.ErrAutomationV2Conflict) {
		t.Fatal("stale acceptance allowed", err)
	}
	accepted, err := service.AcceptAutomationV2("account", "owner", w.WorkspaceID, "author", proposal.AutomationV2Review)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(accepted.Document, proposal.Document) || accepted.Authorization.Kind != "indefinite" || accepted.NextDueAt != accepted.AcceptedAt+120000 {
		t.Fatal("edited settings or anchor drift")
	}
	step := doc.WorkerV2.Schedule.IntervalSeconds * 1000
	agents := agent.NewService(store.NewAgentStore(db), events)
	if err = agents.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	models := model.NewService(store.NewModelStore(db), events, model.NewCatalogService(store.NewModelCatalogStore(db)))
	if err = models.EnsureBootDefaults(); err != nil {
		t.Fatal(err)
	}
	_, _, utility, ok, err := models.RecommendedCatalogDefaults("codex")
	if err != nil || !ok {
		t.Fatal("model defaults", err)
	}
	assignment := store.AgentModelAssignment{Provider: "codex", Model: utility.Model, Thinking: utility.DefaultThinking}
	settings := store.NewAgentModelSettingsStore(db)
	if _, err = settings.PutForAccount(store.AgentModelSettingsRecord{AccountScopeID: "account", Swarm: store.SwarmAgentModelAssignments{Action: assignment, Plan: assignment}, SystemAgents: store.SystemAgentModelAssignments{Compact: assignment, Finder: assignment, Coder: assignment, Designer: assignment, Router: assignment}}); err != nil {
		t.Fatal(err)
	}
	runs := &Service{tools: tool.NewRuntime(1), sessions: service, workspace: ws, agents: agents, agentModelSettings: agentmodelsettings.NewService(settings)}
	runs.sessionDeployCanonicalize = func(in SessionDeployCanonicalizeInput) (SessionDeployCanonicalization, error) {
		if in.Principal.AccountScopeID != "account" || in.WorkspacePath != repo {
			t.Fatal("foreign canonicalization")
		}
		return SessionDeployCanonicalization{SourceWorkspaceID: w.WorkspaceID, SourceWorkspaceGeneration: 1, SourceWorkspacePath: repo, SourceWorkspaceName: "fixture", Metadata: map[string]any{}}, nil
	}
	trees := worktree.NewService(store.NewWorktreeStore(db), ws, nil)
	failWake := true
	failPublish := true
	failCheckpoint := false
	executed := map[string]bool{}
	enqueue := func(principal identity.Principal, intent store.V3SessionRunIntent) bool {
		if failWake {
			return false
		}
		if intent.SessionID == "author" || intent.PlanID == "" || intent.CheckpointID != "report" || principal.AccountScopeID != "account" {
			t.Fatal("wrong canonical execution", intent)
		}
		if executed[intent.RunID] {
			t.Fatal("duplicate executed run")
		}
		plan, ok, err := service.GetActivePlan(intent.SessionID)
		if err != nil || !ok {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(plan.Document.Checkpoints[0].Tasks, doc.Checkpoints[0].Tasks) || plan.Document.AutomationV2 != nil || plan.Document.WorkerV2 != nil {
			t.Fatal("instruction drift")
		}
		intent.Status = sessions.RunIntentRunning
		request := "fixture-running:" + intent.RunID
		if _, err = service.ApplySessionMutation(sessions.SessionMutationInput{SessionID: intent.SessionID, AccountScopeID: "account", UserID: "owner", Kind: sessions.SessionMutationRecordRunIntent, ClientRequestID: request, IdempotencyKey: request, PayloadHash: request, RequestHash: request, RunIntent: &intent}); err != nil {
			t.Fatal(err)
		}
		operation := "complete_checkpoint"
		if failCheckpoint {
			operation = "mark_failed"
		}
		_, _, err = service.PatchPlan(intent.SessionID, sessions.PlanPatchOptions{PlanID: intent.PlanID, DocumentPatch: &sessions.PlanDocumentPatch{Operation: operation, CheckpointID: intent.CheckpointID, AttemptID: intent.AttemptID, RunID: intent.RunID, RunSessionID: intent.SessionID, ParentSessionID: intent.SessionID, Report: "fixture acknowledged", Result: "fixture acknowledged"}})
		if err != nil {
			t.Fatal(err)
		}
		executed[intent.RunID] = true
		return true
	}
	apply := func(in sessions.SessionMutationInput) (sessions.SessionMutationResult, error) {
		if failPublish && in.Kind == sessions.SessionMutationCreateSession {
			return sessions.SessionMutationResult{}, errors.New("injected session publication failure")
		}
		return service.ApplySessionMutation(in)
	}
	host, err := NewAutomationV2ExecutionHost(runs, ss, trees, apply, enqueue)
	if err != nil {
		t.Fatal(err)
	}
	scheduler := sessions.NewAutomationV2Scheduler(service, host)
	if err = scheduler.Tick(ctx, accepted, accepted.NextDueAt-1); err != nil {
		t.Fatal(err)
	}
	rows, _, err := ss.ListAutomationV2Occurrences("account", "owner", w.WorkspaceID, "author", "", false, 25)
	if err != nil || len(rows) != 0 {
		t.Fatal("early admission", err)
	}
	if err = scheduler.Tick(ctx, accepted, accepted.NextDueAt); err == nil {
		t.Fatal("wake failure hidden")
	} else {
		t.Logf("expected wake failure: %v", err)
	}
	rows, _, err = ss.ListAutomationV2Occurrences("account", "owner", w.WorkspaceID, "author", "", false, 25)
	if err != nil || len(rows) != 1 {
		t.Fatal("missing admitted receipt", err)
	}
	first := rows[0]
	if first.Preparation == nil {
		t.Fatal("allocation/model journal missing")
	}
	if _, found, _ := service.GetSession(first.SessionID); found {
		t.Fatal("injected creation persisted session")
	}
	failPublish = false
	scheduler = sessions.NewAutomationV2Scheduler(service, host)
	if err = scheduler.Tick(ctx, accepted, first.NextRetryAt); err == nil {
		t.Fatal("wake failure hidden after preparation replay")
	}
	intent, ok, err := service.GetSessionRunIntent(first.SessionID, first.RunID)
	if err != nil || !ok || intent.Status != sessions.RunIntentPendingExecutor {
		t.Fatalf("durable wake recovery missing: found=%v status=%q err=%v", ok, intent.Status, err)
	}
	execSession, found, err := service.GetSession(first.SessionID)
	if err != nil || !found {
		t.Fatalf("prepared execution session missing: %v", err)
	}
	if execSession.Metadata["navigation_hidden"] != true {
		t.Fatalf("execution session not navigation_hidden: %+v", execSession.Metadata)
	}
	if execSession.Metadata[store.SessionPurposeMetadataKey] != store.SessionPurposeAutomationExecution {
		t.Fatalf("execution session purpose missing: %+v", execSession.Metadata)
	}
	if execSession.Metadata[store.SessionPurposeWorkspaceMetadataKey] != w.WorkspaceID {
		t.Fatalf("execution session purpose workspace missing: %+v", execSession.Metadata)
	}
	if !store.V3SessionNavigationHidden(execSession) {
		t.Fatalf("expected V3SessionNavigationHidden true for execution session")
	}
	topList, err := service.ListTopSessionsByWorkspace([]string{repo}, 20)
	if err != nil {
		t.Fatalf("list top sessions failed: %v", err)
	}
	for _, group := range topList {
		for _, s := range group.Sessions {
			if s.ID == first.SessionID {
				t.Fatalf("execution session %q leaked into workspace visible sessions", first.SessionID)
			}
		}
	}
	failWake = false
	scheduler = sessions.NewAutomationV2Scheduler(service, host)
	if err = scheduler.Tick(ctx, accepted, accepted.NextDueAt+1); err != nil {
		t.Fatal(err)
	}
	if err = scheduler.Tick(ctx, accepted, accepted.NextDueAt+2); err != nil {
		t.Fatal(err)
	}
	secondRecord, _, err := service.GetAutomationV2Record("account", "owner", w.WorkspaceID, "author")
	if err != nil {
		t.Fatal(err)
	}
	if err = scheduler.Tick(ctx, accepted, secondRecord.NextDueAt); err != nil {
		t.Fatal(err)
	}
	if len(executed) != 2 {
		observed, _, _ := ss.ListAutomationV2Occurrences("account", "owner", w.WorkspaceID, "author", "", false, 25)
		for _, o := range observed {
			plan, _, _ := service.GetActivePlan(o.SessionID)
			t.Logf("state=%s detail=%s summary=%+v", o.State, o.Detail, sessions.SummarizePlanExecution(plan.Document))
		}
		t.Fatalf("executed %d distinct slots", len(executed))
	}
	rows, _, err = ss.ListAutomationV2Occurrences("account", "owner", w.WorkspaceID, "author", "", false, 25)
	if err != nil || len(rows) != 2 {
		t.Fatal(err)
	}
	for _, o := range rows {
		if o.State != "succeeded" || o.Record.Digest != accepted.Digest {
			t.Fatal("wrong outcome", o.State)
		}
	}
	if _, ok, err = service.GetActivePlan("author"); err != nil || ok {
		t.Fatal("author chat started", err)
	}
	failCheckpoint = true
	if err = scheduler.Tick(ctx, accepted, accepted.NextDueAt+2*step); err != nil {
		t.Fatal(err)
	}
	rows, _, _ = ss.ListAutomationV2Occurrences("account", "owner", w.WorkspaceID, "author", "", false, 25)
	failed := 0
	for _, o := range rows {
		if o.State == "failed" {
			failed++
		}
	}
	if failed != 1 || len(executed) != 3 {
		t.Fatal("canonical failure attribution")
	}
	failWake = true
	if err = scheduler.Tick(ctx, accepted, accepted.NextDueAt+3*step); err == nil {
		t.Fatal("pending fourth wake")
	}
	pending, _, err := ss.ListAutomationV2Occurrences("account", "owner", w.WorkspaceID, "author", "", true, 25)
	if err != nil || len(pending) != 1 {
		t.Fatal("pending cancellation fixture", err)
	}
	snapshot, _, _ := service.GetSession(pending[0].SessionID)
	if err = runs.validateAutomationV2Execution(snapshot); err != nil {
		t.Fatal("valid occurrence denied", err)
	}
	current, _, err := service.GetAutomationV2Record("account", "owner", w.WorkspaceID, "author")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ss.ControlAutomationV2("account", "foreign", w.WorkspaceID, "author", current.Generation, "pause", time.Now().UnixMilli()); !errors.Is(err, store.ErrAutomationV2Conflict) {
		t.Fatal("foreign pause", err)
	}
	stopped, err := ss.ControlAutomationV2("account", "owner", w.WorkspaceID, "author", current.Generation, "cancel_all", accepted.NextDueAt+3*step+1)
	if err != nil {
		t.Fatal(err)
	}
	if err = runs.validateAutomationV2Execution(snapshot); !errors.Is(err, store.ErrAutomationV2Conflict) {
		t.Fatal("cancelled executor/tool authorized", err)
	}
	if err = scheduler.Tick(ctx, stopped, accepted.NextDueAt+3*step+2); err != nil {
		t.Fatal(err)
	}
	cancelled, _, err := ss.GetAutomationV2Occurrence("account", "owner", w.WorkspaceID, "author", pending[0].ID)
	if err != nil || cancelled.State != "cancelled" {
		t.Fatal("cancelled host outcome", err)
	}
	if err = host.Start(ctx, cancelled); !errors.Is(err, store.ErrAutomationV2Conflict) {
		t.Fatal("recovered start bypassed stop", err)
	}
}

// Purpose: automation occurrence execution sessions must be created with
// navigation_hidden and automation_execution purpose metadata, and must be
// isolated from user sidebar/chat lists while remaining durable, hydrated,
// and inspectable by ID.
func TestAutomationV2ExecutionSessionSidebarIsolation(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "dev"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "fixture"}} {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ids := store.NewIdentityStore(db)
	if _, err = ids.PutUser(store.UserRecord{ID: "owner", Username: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ids.PutAccountScope(store.AccountScopeRecord{ID: "account", Type: store.AccountScopeTypePersonal, CreatedByUserID: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ids.PutAccountUser(store.AccountUserRecord{ID: "member", AccountScopeID: "account", UserID: "owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	ws := workspace.NewService(store.NewWorkspaceStore(db))
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "owner"}
	w, err := ws.AddForPrincipal(p, repo, "fixture", "", false)
	if err != nil {
		t.Fatal(err)
	}
	ss := store.NewSessionStore(db)
	if err = ss.CompleteRepositoryHistoryMaintenance(ctx); err != nil {
		t.Fatal(err)
	}
	events, err := store.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	service := sessions.NewService(ss, events)
	yes := true
	authorSession := store.SessionSnapshot{
		ID:              "chat-sidebar-user-session",
		AccountScopeID:  "account",
		UserID:          "owner",
		Title:           "User chat in sidebar",
		Mode:            "auto",
		WorkspacePath:   repo,
		WorkspaceGrants: []store.WorkspaceGrant{{Kind: store.WorkspaceGrantPrimary, WorkspaceID: w.WorkspaceID, Path: repo, Available: &yes}},
	}
	if err = ss.CreateSession(authorSession); err != nil {
		t.Fatal(err)
	}

	doc := store.SessionPlanDocument{
		Title: "Periodic Task",
		Info:  store.SessionPlanInfo{Goal: "Run isolated work"},
		AutomationV2: &store.AutomationV2Settings{
			SchemaVersion:    2,
			Schedule:         store.AutomationV2Schedule{Kind: "interval", IntervalSeconds: 60},
			Missed:           "skip",
			Overlap:          "serialize",
			ActivateOnAccept: true,
		},
		Checkpoints: []store.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Work", Status: "pending", Order: 1, Tasks: []string{"Task 1"}, AcceptanceCriteria: []string{"Done"}},
		},
	}
	agents := agent.NewService(store.NewAgentStore(db), events)
	if err = agents.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	models := model.NewService(store.NewModelStore(db), events, model.NewCatalogService(store.NewModelCatalogStore(db)))
	if err = models.EnsureBootDefaults(); err != nil {
		t.Fatal(err)
	}
	_, _, utility, ok, err := models.RecommendedCatalogDefaults("codex")
	if err != nil || !ok {
		t.Fatal("model defaults", err)
	}
	assignment := store.AgentModelAssignment{Provider: "codex", Model: utility.Model, Thinking: utility.DefaultThinking}
	settings := store.NewAgentModelSettingsStore(db)
	if _, err = settings.PutForAccount(store.AgentModelSettingsRecord{
		AccountScopeID: "account",
		Swarm:          store.SwarmAgentModelAssignments{Action: assignment, Plan: assignment},
		SystemAgents:   store.SystemAgentModelAssignments{Compact: assignment, Finder: assignment, Coder: assignment, Designer: assignment, Router: assignment},
	}); err != nil {
		t.Fatal(err)
	}
	runs := &Service{tools: tool.NewRuntime(1), sessions: service, workspace: ws, agents: agents, agentModelSettings: agentmodelsettings.NewService(settings)}
	runs.sessionDeployCanonicalize = func(in SessionDeployCanonicalizeInput) (SessionDeployCanonicalization, error) {
		return SessionDeployCanonicalization{SourceWorkspaceID: w.WorkspaceID, SourceWorkspaceGeneration: 1, SourceWorkspacePath: repo, SourceWorkspaceName: "fixture", Metadata: map[string]any{}}, nil
	}
	trees := worktree.NewService(store.NewWorktreeStore(db), ws, nil)
	enqueue := func(principal identity.Principal, intent store.V3SessionRunIntent) bool {
		return true
	}
	host, err := NewAutomationV2ExecutionHost(runs, ss, trees, service.ApplySessionMutation, enqueue)
	if err != nil {
		t.Fatal(err)
	}

	proposal, err := service.ProposeAutomationV2("account", "owner", w.WorkspaceID, authorSession.ID, &doc, store.AutomationV2Review{})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := service.AcceptAutomationV2("account", "owner", w.WorkspaceID, authorSession.ID, proposal.AutomationV2Review)
	if err != nil {
		t.Fatal(err)
	}
	scheduler := sessions.NewAutomationV2Scheduler(service, host)
	if err = scheduler.Tick(ctx, accepted, accepted.NextDueAt); err != nil {
		t.Fatal(err)
	}
	occurrences, _, err := ss.ListAutomationV2Occurrences("account", "owner", w.WorkspaceID, authorSession.ID, "", false, 10)
	if err != nil || len(occurrences) != 1 {
		t.Fatalf("expected 1 occurrence, got %d: %v", len(occurrences), err)
	}
	occ := occurrences[0]
	execSession, found, err := service.GetSession(occ.SessionID)
	if err != nil || !found {
		t.Fatalf("execution session not found: %v", err)
	}
	if execSession.Metadata["navigation_hidden"] != true {
		t.Errorf("expected navigation_hidden: true, got %+v", execSession.Metadata["navigation_hidden"])
	}
	if execSession.Metadata[store.SessionPurposeMetadataKey] != store.SessionPurposeAutomationExecution {
		t.Errorf("expected purpose %s, got %+v", store.SessionPurposeAutomationExecution, execSession.Metadata[store.SessionPurposeMetadataKey])
	}
	if execSession.Metadata[store.SessionPurposeWorkspaceMetadataKey] != w.WorkspaceID {
		t.Errorf("expected purpose workspace %s, got %+v", w.WorkspaceID, execSession.Metadata[store.SessionPurposeWorkspaceMetadataKey])
	}
	if !store.V3SessionNavigationHidden(execSession) {
		t.Errorf("expected store.V3SessionNavigationHidden(execSession) == true")
	}

	// Verify sidebar/chat list isolation:
	// ListSessions filters with normalizeVisibleSessionList
	visible, err := service.ListSessions(50)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range visible {
		if s.ID == occ.SessionID {
			t.Fatalf("execution session %s leaked into visible ListSessions: %+v", occ.SessionID, s)
		}
	}
	// Verify authorSession remains in the visible list
	authorVisible := false
	for _, s := range visible {
		if s.ID == authorSession.ID {
			authorVisible = true
		}
	}
	if !authorVisible {
		t.Errorf("author user chat should remain visible in ListSessions")
	}

	// Verify the execution session remains durable and inspectable by ID
	direct, found, err := service.GetSession(occ.SessionID)
	if err != nil || !found {
		t.Fatalf("direct GetSession failed: %v", err)
	}
	if direct.ID != occ.SessionID || direct.Title != doc.Title {
		t.Fatalf("direct session data mismatch: %+v", direct)
	}
}

func TestAutomationV2ClosingStateContractAndFallbacks(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "dev"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "fixture"}} {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ids := store.NewIdentityStore(db)
	if _, err = ids.PutUser(store.UserRecord{ID: "owner", Username: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ids.PutAccountScope(store.AccountScopeRecord{ID: "account", Type: store.AccountScopeTypePersonal, CreatedByUserID: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ids.PutAccountUser(store.AccountUserRecord{ID: "member", AccountScopeID: "account", UserID: "owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	ws := workspace.NewService(store.NewWorkspaceStore(db))
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "owner"}
	w, err := ws.AddForPrincipal(p, repo, "fixture", "", false)
	if err != nil {
		t.Fatal(err)
	}
	ss := store.NewSessionStore(db)
	if err = ss.CompleteRepositoryHistoryMaintenance(ctx); err != nil {
		t.Fatal(err)
	}
	events, err := store.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	service := sessions.NewService(ss, events)
	yes := true
	authorSession := store.SessionSnapshot{
		ID:              "author-closing-session",
		AccountScopeID:  "account",
		UserID:          "owner",
		Title:           "Author session",
		Mode:            "auto",
		WorkspacePath:   repo,
		WorkspaceGrants: []store.WorkspaceGrant{{Kind: store.WorkspaceGrantPrimary, WorkspaceID: w.WorkspaceID, Path: repo, Available: &yes}},
	}
	if err = ss.CreateSession(authorSession); err != nil {
		t.Fatal(err)
	}

	agents := agent.NewService(store.NewAgentStore(db), events)
	if err = agents.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	models := model.NewService(store.NewModelStore(db), events, model.NewCatalogService(store.NewModelCatalogStore(db)))
	if err = models.EnsureBootDefaults(); err != nil {
		t.Fatal(err)
	}
	_, _, utility, ok, err := models.RecommendedCatalogDefaults("codex")
	if err != nil || !ok {
		t.Fatal("model defaults", err)
	}
	assignment := store.AgentModelAssignment{Provider: "codex", Model: utility.Model, Thinking: utility.DefaultThinking}
	settings := store.NewAgentModelSettingsStore(db)
	if _, err = settings.PutForAccount(store.AgentModelSettingsRecord{
		AccountScopeID: "account",
		Swarm:          store.SwarmAgentModelAssignments{Action: assignment, Plan: assignment},
		SystemAgents:   store.SystemAgentModelAssignments{Compact: assignment, Finder: assignment, Coder: assignment, Designer: assignment, Router: assignment},
	}); err != nil {
		t.Fatal(err)
	}
	runs := &Service{tools: tool.NewRuntime(1), sessions: service, workspace: ws, agents: agents, agentModelSettings: agentmodelsettings.NewService(settings)}
	runs.sessionDeployCanonicalize = func(in SessionDeployCanonicalizeInput) (SessionDeployCanonicalization, error) {
		return SessionDeployCanonicalization{SourceWorkspaceID: w.WorkspaceID, SourceWorkspaceGeneration: 1, SourceWorkspacePath: repo, SourceWorkspaceName: "fixture", Metadata: map[string]any{}}, nil
	}
	trees := worktree.NewService(store.NewWorktreeStore(db), ws, nil)

	var lastClosingState string
	var lastSummary string
	var failCheckpoint bool
	var blockCheckpoint bool
	var deliverableArtifacts []store.SessionPlanArtifactReference

	enqueue := func(principal identity.Principal, intent store.V3SessionRunIntent) bool {
		intent.Status = sessions.RunIntentRunning
		request := "run-closing:" + intent.RunID
		if _, err = service.ApplySessionMutation(sessions.SessionMutationInput{
			SessionID: intent.SessionID, AccountScopeID: "account", UserID: "owner",
			Kind: sessions.SessionMutationRecordRunIntent, ClientRequestID: request,
			IdempotencyKey: request, PayloadHash: request, RequestHash: request, RunIntent: &intent,
		}); err != nil {
			t.Fatal(err)
		}
		operation := "complete_checkpoint"
		if failCheckpoint {
			operation = "mark_failed"
		} else if blockCheckpoint {
			operation = "mark_blocked"
		}
		_, _, err = service.PatchPlan(intent.SessionID, sessions.PlanPatchOptions{
			PlanID: intent.PlanID,
			DocumentPatch: &sessions.PlanDocumentPatch{
				Operation:       operation,
				CheckpointID:    intent.CheckpointID,
				AttemptID:       intent.AttemptID,
				RunID:           intent.RunID,
				RunSessionID:    intent.SessionID,
				ParentSessionID: intent.SessionID,
				Report:          "Finished work summary",
				Result:          "acknowledged",
				ClosingState:    lastClosingState,
				Summary:         lastSummary,
				Artifacts:       deliverableArtifacts,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return true
	}

	host, err := NewAutomationV2ExecutionHost(runs, ss, trees, service.ApplySessionMutation, enqueue)
	if err != nil {
		t.Fatal(err)
	}

	doc := store.SessionPlanDocument{
		Title: "Closing Contract Plan",
		Info:  store.SessionPlanInfo{Goal: "Verify structured closing states"},
		AutomationV2: &store.AutomationV2Settings{
			SchemaVersion:    2,
			Schedule:         store.AutomationV2Schedule{Kind: "interval", IntervalSeconds: 60},
			Missed:           "skip",
			Overlap:          "serialize",
			ActivateOnAccept: true,
		},
		Checkpoints: []store.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Step", Status: "pending", Order: 1, Tasks: []string{"Task"}, AcceptanceCriteria: []string{"Done"}},
		},
	}
	proposal, err := service.ProposeAutomationV2("account", "owner", w.WorkspaceID, authorSession.ID, &doc, store.AutomationV2Review{})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := service.AcceptAutomationV2("account", "owner", w.WorkspaceID, authorSession.ID, proposal.AutomationV2Review)
	if err != nil {
		t.Fatal(err)
	}

	findByDue := func(occs []store.AutomationV2Occurrence, due int64) store.AutomationV2Occurrence {
		for _, o := range occs {
			if o.DueAt == due {
				return o
			}
		}
		t.Fatalf("occurrence for due=%d not found in %d occurrences", due, len(occs))
		return store.AutomationV2Occurrence{}
	}

	// 1. Explicit routine_clean
	due1 := accepted.NextDueAt
	lastClosingState = "routine_clean"
	lastSummary = "Cleaned up 12 old caches · All good"
	failCheckpoint = false
	blockCheckpoint = false
	deliverableArtifacts = nil

	scheduler := sessions.NewAutomationV2Scheduler(service, host)
	if err = scheduler.Tick(ctx, accepted, due1); err != nil {
		t.Fatal(err)
	}
	occs, _, err := ss.ListAutomationV2Occurrences("account", "owner", w.WorkspaceID, authorSession.ID, "", false, 10)
	if err != nil || len(occs) != 1 {
		t.Fatalf("expected 1 occurrence: %v", err)
	}
	occ1 := findByDue(occs, due1)
	if occ1.State != "succeeded" {
		t.Errorf("expected succeeded, got %s", occ1.State)
	}
	if occ1.ClosingState != "routine_clean" {
		t.Errorf("expected closing_state routine_clean, got %q", occ1.ClosingState)
	}
	if occ1.Summary != "Cleaned up 12 old caches · All good" {
		t.Errorf("expected summary, got %q", occ1.Summary)
	}
	if occ1.Detail != "Cleaned up 12 old caches · All good" {
		t.Errorf("expected calm detail, got %q", occ1.Detail)
	}

	// 2. Explicit deliverable_ready with artifacts
	due2 := accepted.NextDueAt + 60000
	lastClosingState = "deliverable_ready"
	lastSummary = "Quarterly compliance audit report ready"
	deliverableArtifacts = []store.SessionPlanArtifactReference{{
		Path: "reports/audit.md", Role: "deliverable", Description: "Audit report", MediaType: "text/markdown",
	}}
	if err = scheduler.Tick(ctx, accepted, due2); err != nil {
		t.Fatal(err)
	}
	occs, _, err = ss.ListAutomationV2Occurrences("account", "owner", w.WorkspaceID, authorSession.ID, "", false, 10)
	if err != nil || len(occs) != 2 {
		t.Fatalf("expected 2 occurrences: %v", err)
	}
	occ2 := findByDue(occs, due2)
	if occ2.ClosingState != "deliverable_ready" {
		t.Errorf("expected deliverable_ready, got %q", occ2.ClosingState)
	}
	if len(occ2.Deliverables) != 1 || occ2.Deliverables[0].Path != "reports/audit.md" {
		t.Errorf("expected deliverables populated, got %+v", occ2.Deliverables)
	}

	// 3. Explicit attention_alert
	due3 := accepted.NextDueAt + 120000
	lastClosingState = "attention_alert"
	lastSummary = "Memory usage above 92%"
	deliverableArtifacts = nil
	if err = scheduler.Tick(ctx, accepted, due3); err != nil {
		t.Fatal(err)
	}
	occs, _, err = ss.ListAutomationV2Occurrences("account", "owner", w.WorkspaceID, authorSession.ID, "", false, 10)
	if err != nil || len(occs) != 3 {
		t.Fatalf("expected 3 occurrences: %v", err)
	}
	occ3 := findByDue(occs, due3)
	if occ3.ClosingState != "attention_alert" {
		t.Errorf("expected attention_alert, got %q", occ3.ClosingState)
	}
	if !strings.Contains(occ3.Detail, "Alert") || !strings.Contains(occ3.Detail, "Memory usage above 92%") {
		t.Errorf("expected alert detail, got %q", occ3.Detail)
	}

	// 4. Fallback handling: no closing_state and no summary on clean complete
	due4 := accepted.NextDueAt + 180000
	lastClosingState = ""
	lastSummary = ""
	failCheckpoint = false
	blockCheckpoint = false
	deliverableArtifacts = nil
	if err = scheduler.Tick(ctx, accepted, due4); err != nil {
		t.Fatal(err)
	}
	occs, _, err = ss.ListAutomationV2Occurrences("account", "owner", w.WorkspaceID, authorSession.ID, "", false, 10)
	if err != nil || len(occs) != 4 {
		t.Fatalf("expected 4 occurrences: %v", err)
	}
	occ4 := findByDue(occs, due4)
	if occ4.State != "succeeded" {
		t.Errorf("expected fallback run succeeded, got %q", occ4.State)
	}
	if occ4.ClosingState != "routine_clean" {
		t.Errorf("expected fallback closing_state routine_clean, got %q", occ4.ClosingState)
	}
	if occ4.Summary == "" {
		t.Errorf("expected non-empty fallback summary, got empty")
	}

	// 5. Fallback handling: no closing_state on failed run falls back to attention_alert
	due5 := accepted.NextDueAt + 240000
	lastClosingState = ""
	lastSummary = ""
	failCheckpoint = true
	blockCheckpoint = false
	if err = scheduler.Tick(ctx, accepted, due5); err != nil {
		t.Fatal(err)
	}
	occs, _, err = ss.ListAutomationV2Occurrences("account", "owner", w.WorkspaceID, authorSession.ID, "", false, 10)
	if err != nil || len(occs) != 5 {
		t.Fatalf("expected 5 occurrences: %v", err)
	}
	occ5 := findByDue(occs, due5)
	if occ5.State != "failed" {
		t.Errorf("expected failed state, got %q", occ5.State)
	}
	if occ5.ClosingState != "attention_alert" {
		t.Errorf("expected fallback attention_alert on failed run, got %q", occ5.ClosingState)
	}

	// 6. Unknown/invalid closing_state falls back gracefully without failing execution
	due6 := accepted.NextDueAt + 300000
	lastClosingState = "invalid_mystery_state"
	lastSummary = "Mystery check"
	failCheckpoint = false
	blockCheckpoint = false
	if err = scheduler.Tick(ctx, accepted, due6); err != nil {
		t.Fatal(err)
	}
	occs, _, err = ss.ListAutomationV2Occurrences("account", "owner", w.WorkspaceID, authorSession.ID, "", false, 10)
	if err != nil || len(occs) != 6 {
		t.Fatalf("expected 6 occurrences: %v", err)
	}
	occ6 := findByDue(occs, due6)
	if occ6.State != "succeeded" {
		t.Errorf("expected succeeded for unknown closing state fallback, got %q", occ6.State)
	}
	if occ6.ClosingState != "routine_clean" {
		t.Errorf("expected graceful fallback to routine_clean, got %q", occ6.ClosingState)
	}
}

func TestAutomationV2MultiWorkspaceScoping(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 1. Primary workspace repo
	primaryRepo := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "dev"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "fixture-primary"}} {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", primaryRepo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git primary: %v %s", err, out)
		}
	}

	// 2. Secondary workspace repo
	secondaryRepo := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "dev"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "fixture-secondary"}} {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", secondaryRepo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git secondary: %v %s", err, out)
		}
	}

	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ids := store.NewIdentityStore(db)
	if _, err = ids.PutUser(store.UserRecord{ID: "owner", Username: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ids.PutAccountScope(store.AccountScopeRecord{ID: "account", Type: store.AccountScopeTypePersonal, CreatedByUserID: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ids.PutAccountUser(store.AccountUserRecord{ID: "member", AccountScopeID: "account", UserID: "owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}

	ws := workspace.NewService(store.NewWorkspaceStore(db))
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "owner"}
	wPrimary, err := ws.AddForPrincipal(p, primaryRepo, "primary", "", false)
	if err != nil {
		t.Fatal(err)
	}
	wSecondary, err := ws.AddForPrincipal(p, secondaryRepo, "secondary", "", false)
	if err != nil {
		t.Fatal(err)
	}

	ss := store.NewSessionStore(db)
	if err = ss.CompleteRepositoryHistoryMaintenance(ctx); err != nil {
		t.Fatal(err)
	}
	yes := true
	if err = ss.CreateSession(store.SessionSnapshot{
		ID:             "author",
		AccountScopeID: "account",
		UserID:         "owner",
		Mode:           "auto",
		WorkspacePath:  primaryRepo,
		WorkspaceGrants: []store.WorkspaceGrant{{
			Kind:        store.WorkspaceGrantPrimary,
			WorkspaceID: wPrimary.WorkspaceID,
			Path:        primaryRepo,
			Available:   &yes,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	events, err := store.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	service := sessions.NewService(ss, events)

	// Document specifies secondary workspace
	doc := store.SessionPlanDocument{
		Title: "Multi-workspace worker",
		Info:  store.SessionPlanInfo{Goal: "Verify multi-workspace scoping"},
		WorkerV2: &store.AutomationV2Settings{
			SchemaVersion:    2,
			WorkspaceID:      wPrimary.WorkspaceID,
			WorkspaceIDs:     []string{wSecondary.WorkspaceID},
			Schedule:         store.AutomationV2Schedule{Kind: "interval", IntervalSeconds: 60},
			Missed:           "coalesce",
			Overlap:          "independent",
			ActivateOnAccept: true,
		},
		Checkpoints: []store.SessionPlanCheckpoint{{
			ID:                 "step1",
			Title:              "Step 1",
			Status:             "pending",
			Order:              1,
			Tasks:              []string{"Check both workspaces"},
			AcceptanceCriteria: []string{"Done"},
		}},
	}

	permissions := permission.NewService(store.NewPermissionStore(db), events, nil)
	permissions.SetBypassPermissions(true)
	authoring := NewService(service, nil, nil, tool.NewRuntime(1), permissions, nil, nil, events)
	profile := agent.SwarmAgentProfileForContext(store.AgentProfile{})
	if _, _, _, err = authoring.compileResolvedAgentToolContract("account", profile); err != nil {
		t.Fatal(err)
	}

	call := tool.Call{
		Name:      "manage_workers",
		Arguments: fmt.Sprintf(`{"action":"propose","document":%s}`, string(mustJSON(t, doc))),
	}
	out, err := authoring.executeWorkerProposalTool("author", call)
	if err != nil {
		t.Fatalf("proposal failed: %v", err)
	}
	var propResult struct {
		WorkerReview store.AutomationV2Review `json:"worker_review"`
	}
	if err = json.Unmarshal([]byte(out), &propResult); err != nil {
		t.Fatalf("unmarshal proposal result: %v", err)
	}

	accepted, err := service.AcceptAutomationV2("account", "owner", wPrimary.WorkspaceID, "author", propResult.WorkerReview)
	if err != nil {
		t.Fatalf("accept failed: %v", err)
	}

	// Verify proposal and accepted record carry WorkspaceIDs
	var foundSecondaryInRecord bool
	for _, wid := range accepted.WorkspaceIDs {
		if wid == wSecondary.WorkspaceID {
			foundSecondaryInRecord = true
			break
		}
	}
	if !foundSecondaryInRecord {
		t.Fatalf("expected accepted record to contain secondary workspace_id %s, got %v", wSecondary.WorkspaceID, accepted.WorkspaceIDs)
	}

	// Trigger execution and prepare occurrence
	agents := agent.NewService(store.NewAgentStore(db), events)
	if err = agents.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	models := model.NewService(store.NewModelStore(db), events, model.NewCatalogService(store.NewModelCatalogStore(db)))
	if err = models.EnsureBootDefaults(); err != nil {
		t.Fatal(err)
	}
	_, _, utility, ok, err := models.RecommendedCatalogDefaults("codex")
	if err != nil || !ok {
		t.Fatal("model defaults", err)
	}
	assignment := store.AgentModelAssignment{Provider: "codex", Model: utility.Model, Thinking: utility.DefaultThinking}
	settings := store.NewAgentModelSettingsStore(db)
	if _, err = settings.PutForAccount(store.AgentModelSettingsRecord{
		AccountScopeID: "account",
		Swarm:          store.SwarmAgentModelAssignments{Action: assignment, Plan: assignment},
		SystemAgents:   store.SystemAgentModelAssignments{Compact: assignment, Finder: assignment, Coder: assignment, Designer: assignment, Router: assignment},
	}); err != nil {
		t.Fatal(err)
	}
	runs := &Service{tools: tool.NewRuntime(1), sessions: service, workspace: ws, agents: agents, agentModelSettings: agentmodelsettings.NewService(settings)}
	runs.sessionDeployCanonicalize = func(in SessionDeployCanonicalizeInput) (SessionDeployCanonicalization, error) {
		return SessionDeployCanonicalization{SourceWorkspaceID: wPrimary.WorkspaceID, SourceWorkspaceGeneration: 1, SourceWorkspacePath: primaryRepo, SourceWorkspaceName: "primary", Metadata: map[string]any{}}, nil
	}
	trees := worktree.NewService(store.NewWorktreeStore(db), ws, nil)
	enqueue := func(principal identity.Principal, intent store.V3SessionRunIntent) bool {
		return true
	}
	host, err := NewAutomationV2ExecutionHost(runs, ss, trees, service.ApplySessionMutation, enqueue)
	if err != nil {
		t.Fatal(err)
	}

	occ, err := ss.TriggerAutomationV2("account", "owner", wPrimary.WorkspaceID, accepted.SessionID, nil, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("trigger failed: %v", err)
	}

	snapshot, err := host.prepare(ctx, occ)
	if err != nil {
		t.Fatalf("prepare failed: %v", err)
	}

	// Verify snapshot has primary, worktree, AND secondary workspace grants
	var hasPrimary, hasWorktree, hasSecondary bool
	for _, g := range snapshot.WorkspaceGrants {
		if g.Kind == store.WorkspaceGrantPrimary && g.WorkspaceID == wPrimary.WorkspaceID {
			hasPrimary = true
		}
		if g.Kind == store.WorkspaceGrantWorktree {
			hasWorktree = true
		}
		if g.Kind == store.WorkspaceGrantAdditional && g.WorkspaceID == wSecondary.WorkspaceID {
			hasSecondary = true
		}
	}
	if !hasPrimary {
		t.Errorf("missing primary workspace grant")
	}
	if !hasWorktree {
		t.Errorf("missing worktree workspace grant")
	}
	if !hasSecondary {
		t.Errorf("missing secondary workspace grant (WorkspaceGrantAdditional)")
	}
}
