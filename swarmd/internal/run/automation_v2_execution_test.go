package run

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/jsonschema-go/jsonschema"
	"os/exec"
	"reflect"
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
	doc := store.SessionPlanDocument{Title: "Harmless report", Info: store.SessionPlanInfo{Goal: "Return the exact accepted instruction"}, AutomationV2: &store.AutomationV2Settings{SchemaVersion: 2, Schedule: store.AutomationV2Schedule{Kind: "interval", IntervalSeconds: 60}, Missed: "coalesce", Overlap: "serialize", ActivateOnAccept: true}, Checkpoints: []store.SessionPlanCheckpoint{{ID: "report", Title: "Report", Status: "pending", Order: 1, Tasks: []string{"Return ONLY: fixture acknowledged"}, AcceptanceCriteria: []string{"Exact fixture phrase returned"}}}}
	permissions := permission.NewService(store.NewPermissionStore(db), events, nil)
	permissions.SetBypassPermissions(true)
	authoring := NewService(service, nil, nil, tool.NewRuntime(1), permissions, nil, nil, events)
	profile := agent.SwarmAgentProfileForContext(store.AgentProfile{})
	_, policy, disabled, err := authoring.compileResolvedAgentToolContract("account", profile)
	if err != nil {
		t.Fatal(err)
	}
	args, err := json.Marshal(map[string]any{"action": "request_new_plan", "document": doc})
	if err != nil {
		t.Fatal(err)
	}
	var instance any
	if err = json.Unmarshal(args, &instance); err != nil {
		t.Fatal(err)
	}
	// Provider JSON omits unspecified expiration rather than serializing a Go
	// zero-value struct; the advertised optional field must normalize indefinite.
	delete(instance.(map[string]any)["document"].(map[string]any)["automation_v2"].(map[string]any), "expiration")
	args, err = json.Marshal(instance)
	if err != nil {
		t.Fatal(err)
	}
	validated := false
	for _, definition := range filterToolDefinitions(convertToolDefinitions(authoring.ListAgentToolDefinitionsForAccount("account")), disabled) {
		if definition.Name != "plan_manage" {
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
	result, err := invoker.ExecuteTool(ctx, provideriface.ToolInvocation{Name: "plan_manage", CallID: "fresh-proposal", Arguments: string(args)})
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
	doc.AutomationV2.Schedule.IntervalSeconds = 120
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
	step := doc.AutomationV2.Schedule.IntervalSeconds * 1000
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
		if !reflect.DeepEqual(plan.Document.Checkpoints[0].Tasks, doc.Checkpoints[0].Tasks) || plan.Document.AutomationV2 != nil {
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
	if err = scheduler.Tick(ctx, accepted, accepted.NextDueAt); err == nil {
		t.Fatal("wake failure hidden after preparation replay")
	}
	intent, ok, err := service.GetSessionRunIntent(first.SessionID, first.RunID)
	if err != nil || !ok || intent.Status != sessions.RunIntentPendingExecutor {
		t.Fatal("durable wake recovery missing", err)
	}
	failWake = false
	scheduler = sessions.NewAutomationV2Scheduler(service, host)
	if err = scheduler.Tick(ctx, accepted, accepted.NextDueAt+1); err != nil {
		t.Fatal(err)
	}
	if err = scheduler.Tick(ctx, accepted, accepted.NextDueAt+2); err != nil {
		t.Fatal(err)
	}
	if err = scheduler.Tick(ctx, accepted, accepted.NextDueAt+step); err != nil {
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
