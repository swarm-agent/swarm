package run

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	agent "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	session "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: the real provider invoker, permission gate and session/store boundary
// must turn schema-constrained dedicated Worker proposals in Plan/Auto into only a pending review.
// Prevent circular V1 prerequisites, self-approval, one-shot execution, stale
// edits and policy bypass. A temp Pebble store is the narrow integration layer
// proving durable postconditions without a provider or running daemon.
func TestAutomationV2ProviderDispatch(t *testing.T) {
	for _, mode := range []string{"plan", "auto"} {
		t.Run(mode, func(t *testing.T) {
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
			w, err := store.NewWorkspaceStore(db).AddForAccount("account", t.TempDir(), "Workspace")
			if err != nil {
				t.Fatal(err)
			}
			ss := store.NewSessionStore(db)
			yes := true
			if err = ss.CreateSession(store.SessionSnapshot{ID: "author", AccountScopeID: "account", UserID: "owner", Mode: mode, WorkspacePath: w.Path, WorkspaceGrants: []store.WorkspaceGrant{{Kind: store.WorkspaceGrantPrimary, WorkspaceID: w.WorkspaceID, Path: w.Path, Available: &yes}}}); err != nil {
				t.Fatal(err)
			}
			events, err := store.NewEventLog(db)
			if err != nil {
				t.Fatal(err)
			}
			sessions := session.NewService(ss, events)
			ps := store.NewPermissionStore(db)
			permissions := permission.NewService(ps, events, nil)
			permissions.SetBypassPermissions(true)
			svc := NewService(sessions, nil, nil, tool.NewRuntime(1), permissions, nil, nil, events)
			profile := agent.SwarmAgentProfileForContext(store.AgentProfile{})
			_, policy, disabled, err := svc.compileResolvedAgentToolContract("account", profile)
			if err != nil {
				t.Fatal(err)
			}
			name := "manage_workers"
			defs := filterToolDefinitions(convertToolDefinitions(svc.ListAgentToolDefinitionsForAccount("account")), disabled)
			var schema *jsonschema.Resolved
			for _, d := range defs {
				if d.Name == name {
					b, _ := json.Marshal(d.Parameters)
					var s jsonschema.Schema
					if err = json.Unmarshal(b, &s); err != nil {
						t.Fatal(err)
					}
					schema, err = s.Resolve(nil)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if schema == nil {
				t.Fatalf("%s absent from actual primary tool inventory", name)
			}
			doc := map[string]any{"title": "Automation plan: report", "info": map[string]any{"goal": "Report repository status"}, "automation_v2": map[string]any{"schema_version": 2, "schedule": map[string]any{"kind": "interval", "interval_seconds": 900}, "missed": "skip", "overlap": "serialize", "activate_on_accept": true}, "checkpoints": []any{map[string]any{"id": "report", "title": "Report", "status": "pending", "order": 1, "tasks": []string{"Report repository status without changes"}, "acceptance_criteria": []string{"Factual report returned"}}}}
			args := map[string]any{"action": "propose", "document": doc}
			calls := 0
			invoke := func(args map[string]any, overlay *permission.Policy) provideriface.ToolExecutionResult {
				t.Helper()
				raw, _ := json.Marshal(args)
				var instance any
				_ = json.Unmarshal(raw, &instance)
				if err := schema.Validate(instance); err != nil {
					t.Fatalf("advertised schema: %v", err)
				}
				invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{sessionID: "author", principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "owner", AccountScopeID: "account"}, sessionMode: mode, runID: "authoring", providerManagedV3: true, applySessionMutation: sessions.ApplySessionMutation, agentProfile: profile, policy: overlay, terminalPlanState: &terminalPlanToolState{}})
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				calls++
				result, err := invoker.ExecuteTool(ctx, provideriface.ToolInvocation{Name: name, CallID: fmt.Sprintf("proposal-%d", calls), Arguments: string(raw)})
				if err != nil {
					t.Fatal(err)
				}
				return result
			}
			// Denial must be checked before internal proposal persistence, including bypass mode.
			deny := permission.Policy{Version: 1, Rules: []permission.PolicyRule{{Kind: permission.PolicyRuleKindTool, Tool: name, Decision: permission.PolicyDecisionDeny}}}
			rejected := invoke(args, &deny)
			if rejected.Error == "" {
				t.Fatal("denial ignored")
			}
			if _, found, _ := sessions.GetAutomationV2Proposal("account", "owner", w.WorkspaceID, "author"); found {
				t.Fatal("denied call persisted proposal")
			}
			result := invoke(args, policy)
			if result.Error != "" {
				t.Fatalf("dispatch: %s / %s", result.Error, result.Output)
			}
			if !result.RestartTurn {
				t.Fatal("authoring producer not stopped at review")
			}
			p, found, err := sessions.GetAutomationV2Proposal("account", "owner", w.WorkspaceID, "author")
			if err != nil || !found {
				t.Fatalf("proposal %v %v", found, err)
			}
			if p.Document.AutomationV2.Expiration.Kind != "indefinite" {
				t.Fatal("missing expiry did not default indefinite")
			}
			records, _, err := sessions.ListAutomationV2Records("account", "owner", w.WorkspaceID, "", 20)
			if err != nil || len(records) != 0 {
				t.Fatal("proposal activated automation", err)
			}
			if _, found, _ := sessions.GetActivePlan("author"); found {
				t.Fatal("ordinary active plan created")
			}
			snapshot, ok, err := sessions.GetSession("author")
			if err != nil || !ok || snapshot.Mode != mode {
				t.Fatalf("proposal changed authoring session mode: %q %v", snapshot.Mode, err)
			}
			if _, found, _ := ss.GetV3SessionActiveRunIntent("author"); found {
				t.Fatal("one-shot run created")
			}
			pending, err := ps.ListPendingPermissions("author", 10)
			if err != nil || len(pending) != 1 {
				t.Fatal("missing unique permission", err)
			}
			if pending[0].Requirement != "automation_v2_acceptance" || pending[0].ToolName != "manage_workers" || pending[0].Mode != "plan" || !strings.Contains(pending[0].ToolArguments, "Accept worker") || strings.Contains(pending[0].ToolArguments, "request_new_plan") {
				t.Fatal("not a dedicated pending worker review")
			}
			// Runtime rejects schema-valid ambiguity before changing the pending head.
			bad := map[string]any{"action": "propose", "document": doc, "worker_review": p.AutomationV2Review}
			doc["automation_v2"].(map[string]any)["schedule"] = map[string]any{"kind": "cron", "cron": "0 18 * * *"}
			if got := invoke(bad, policy); got.Error == "" || !strings.Contains(got.Error, "timezone") {
				t.Fatalf("missing timezone not actionable: %s", got.Error)
			}
			doc["automation_v2"].(map[string]any)["schedule"] = map[string]any{"kind": "interval", "interval_seconds": 900}
			if _, err := permissions.EditPendingPlanProposal(permission.PendingPlanProposalEditInput{SessionID: "author", PermissionID: pending[0].ID, ExpectedRevision: 1, Document: &p.Document}); err == nil {
				t.Fatal("ordinary editor modified recurring review")
			}
			if _, err := svc.executeManageAutomationV2Tool("author", `{"action":"approve"}`); err == nil {
				t.Fatal("agent approval supported")
			}
			reviewOut, err := svc.executeManageAutomationV2Tool("author", `{"action":"review"}`)
			if err != nil || !strings.Contains(reviewOut, "worker_review") {
				t.Fatalf("manage_workers review missing worker_review: %s %v", reviewOut, err)
			}
			// The bound Plan sidechat can update only this exact parent's proposal.
			if err := ss.CreateSession(store.SessionSnapshot{ID: "sidechat", AccountScopeID: "account", UserID: "owner", Metadata: map[string]any{"system_sidechat_kind": "plan", "lineage_kind": "system_sidechat", "parent_session_id": "author", "plan_permission_id": pending[0].ID}}); err != nil {
				t.Fatal(err)
			}
			sideDoc := p.Document
			sideDoc.AutomationV2.Expiration = store.AutomationV2Expiration{Kind: "at", ExpiresAt: time.Now().Add(24 * time.Hour).UnixMilli()}
			sideArgs, _ := json.Marshal(map[string]any{"expected_revision": 1, "automation_review": p.AutomationV2Review, "document": sideDoc})
			if _, err := svc.executeEditPendingPlanTool("sidechat", string(sideArgs)); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.executeEditPendingPlanTool("sidechat", string(sideArgs)); err != nil {
				t.Fatal("exact edit replay should be idempotent", err)
			}
			p, _, _ = sessions.GetAutomationV2Proposal("account", "owner", w.WorkspaceID, "author")
			if p.Document.AutomationV2.Expiration != sideDoc.AutomationV2.Expiration {
				t.Fatal("sidechat changed finite expiry")
			}
			old := p.AutomationV2Review
			args["worker_review"] = old
			doc["automation_v2"].(map[string]any)["schedule"] = map[string]any{"kind": "cron", "cron": "0 18 * * *", "timezone": "UTC"}
			result = invoke(args, policy)
			if result.Error != "" {
				t.Fatal(result.Error)
			}
			p, _, _ = sessions.GetAutomationV2Proposal("account", "owner", w.WorkspaceID, "author")
			if p.Revision != 3 || p.Document.AutomationV2.Schedule.Cron != "0 18 * * *" {
				t.Fatal("edit lost cadence")
			}
			doc["title"] = "Stale changed instructions"
			if result = invoke(args, policy); result.Error == "" {
				t.Fatal("stale edit accepted")
			}
			head, _, _ := sessions.GetAutomationV2Proposal("account", "owner", w.WorkspaceID, "author")
			if !reflect.DeepEqual(p, head) {
				t.Fatal("stale edit changed head")
			}
			if _, err := sessions.AcceptAutomationV2("account", "owner", w.WorkspaceID, "author", old); err == nil {
				t.Fatal("stale acceptance succeeded")
			}
			accepted, err := sessions.AcceptAutomationV2("account", "owner", w.WorkspaceID, "author", p.AutomationV2Review)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(accepted.Document, p.Document) || !accepted.Enabled {
				t.Fatal("acceptance changed reviewed instructions")
			}
			pending, err = ps.ListPendingPermissions("author", 10)
			if err != nil || len(pending) != 0 {
				t.Fatal("acceptance left pending permission")
			}
			if _, found, _ := ss.GetV3SessionActiveRunIntent("author"); found {
				t.Fatal("acceptance ran authoring chat")
			}
		})
	}
}

// Purpose: typed recurrence, not titles, selects V2. Exercise the real ordinary
// permission payload and lifecycle to prevent changing ordinary approval/run
// behavior when a plan happens to contain words such as hourly or automation.
func TestAutomationV2DoesNotInferOrdinaryPlan(t *testing.T) {
	svc, sessions, cleanup := newPlanManageRunTestService(t)
	defer cleanup()
	snap, _, err := sessions.CreateSessionWithOptions(session.CreateSessionOptions{SessionID: "ordinary", UserID: "owner", AccountScopeID: "account", WorkspacePath: t.TempDir(), Title: "Ordinary", Mode: "auto", Preference: &store.ModelPreference{Provider: "fake", Model: "fake-model", Thinking: "off"}})
	if err != nil {
		t.Fatal(err)
	}
	id := snap.ID
	args := `{"action":"request_new_plan","document":{"title":"Hourly automation discussion","info":{"goal":"Write a one-time report"},"checkpoints":[{"id":"report","title":"Report","status":"pending","order":1,"tasks":["Write the report once"],"acceptance_criteria":["Report delivered"]}]}}`
	call := tool.Call{Name: "plan_manage", Arguments: args}
	if workerDocumentInPlanCall(call) {
		t.Fatal("inferred recurrence from title")
	}
	payload, needed, err := svc.buildPlanManagePermissionPayload(id, call)
	if err != nil || !needed {
		t.Fatal("ordinary review missing", err)
	}
	b, _ := json.Marshal(payload.Document)
	var ordinary store.SessionPlanDocument
	if err := json.Unmarshal(b, &ordinary); err != nil {
		t.Fatal(err)
	}
	if ordinary.AutomationV2 != nil {
		t.Fatal("ordinary permission gained recurrence")
	}
	output, err := svc.executePlanManageToolWithMutation(id, args, "", sessions.ApplySessionMutation)
	if err == nil || !strings.Contains(err.Error(), "requires user approval") {
		t.Fatalf("ordinary plan did not require its normal approval: %s %v", output, err)
	}
	if strings.Contains(output, "await_automation_acceptance") {
		t.Fatal("ordinary plan routed as recurring")
	}
	feedback, _ := json.Marshal(map[string]any{"action": payload.Action, "approved_arguments": payload.ApprovedArguments})
	output, err = svc.executePlanManageToolWithMutation(id, args, string(feedback), sessions.ApplySessionMutation)
	if err != nil {
		t.Fatal(err)
	}
	accepted, found, err := sessions.GetActivePlan(id)
	if err != nil || !found || accepted.ApprovalState != "approved" || accepted.Document.AutomationV2 != nil || !strings.Contains(output, "run_checkpoint_with_current_context") {
		t.Fatalf("ordinary approval/execution changed: %s %v", output, err)
	}
}

// Purpose: verify that worker_v2, worker_review, and manage_workers tool work seamlessly.
func TestWorkerV2ProviderDispatch(t *testing.T) {
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
	w, err := store.NewWorkspaceStore(db).AddForAccount("account", t.TempDir(), "Workspace")
	if err != nil {
		t.Fatal(err)
	}
	ss := store.NewSessionStore(db)
	yes := true
	if err = ss.CreateSession(store.SessionSnapshot{ID: "author", AccountScopeID: "account", UserID: "owner", Mode: "auto", WorkspacePath: w.Path, WorkspaceGrants: []store.WorkspaceGrant{{Kind: store.WorkspaceGrantPrimary, WorkspaceID: w.WorkspaceID, Path: w.Path, Available: &yes}}}); err != nil {
		t.Fatal(err)
	}
	events, err := store.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.NewService(ss, events)
	ps := store.NewPermissionStore(db)
	permissions := permission.NewService(ps, events, nil)
	permissions.SetBypassPermissions(true)
	svc := NewService(sessions, nil, nil, tool.NewRuntime(1), permissions, nil, nil, events)
	profile := agent.SwarmAgentProfileForContext(store.AgentProfile{})
	_, policy, disabled, err := svc.compileResolvedAgentToolContract("account", profile)
	if err != nil {
		t.Fatal(err)
	}
	defs := filterToolDefinitions(convertToolDefinitions(svc.ListAgentToolDefinitionsForAccount("account")), disabled)
	hasManageWorkers := false
	for _, d := range defs {
		if d.Name == "manage_workers" {
			hasManageWorkers = true
			break
		}
	}
	if !hasManageWorkers {
		t.Fatal("manage_workers missing from compiled agent tool definitions")
	}
	doc := map[string]any{
		"title": "Worker plan: report",
		"info":  map[string]any{"goal": "Report repository status"},
		"worker_v2": map[string]any{
			"schema_version":     2,
			"schedule":           map[string]any{"kind": "interval", "interval_seconds": 900},
			"missed":             "skip",
			"overlap":            "serialize",
			"activate_on_accept": true,
		},
		"checkpoints": []any{
			map[string]any{"id": "report", "title": "Report", "status": "pending", "order": 1, "tasks": []string{"Report repository status without changes"}, "acceptance_criteria": []string{"Factual report returned"}},
		},
	}
	args := map[string]any{"action": "propose", "document": doc}
	raw, _ := json.Marshal(args)
	invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{sessionID: "author", principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "owner", AccountScopeID: "account"}, sessionMode: "auto", runID: "authoring", providerManagedV3: true, applySessionMutation: sessions.ApplySessionMutation, agentProfile: profile, policy: policy, terminalPlanState: &terminalPlanToolState{}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	legacy := map[string]any{"action": "request_new_plan", "document": doc}
	legacyRaw, _ := json.Marshal(legacy)
	for _, oldTool := range []string{"plan_manage", "exit_plan_mode"} {
		oldArgs := legacyRaw
		if oldTool == "exit_plan_mode" {
			oldArgs, _ = json.Marshal(map[string]any{"document": doc})
		}
		oldResult, callErr := invoker.ExecuteTool(ctx, provideriface.ToolInvocation{Name: oldTool, CallID: "legacy-" + oldTool, Arguments: string(oldArgs)})
		if callErr != nil || !strings.Contains(oldResult.Error, "manage_workers action=propose") {
			t.Fatalf("%s should reject worker authoring: %s %v", oldTool, oldResult.Error, callErr)
		}
	}
	if _, found, _ := sessions.GetAutomationV2Proposal("account", "owner", w.WorkspaceID, "author"); found {
		t.Fatal("legacy session-plan path persisted a worker proposal")
	}
	res, err := invoker.ExecuteTool(ctx, provideriface.ToolInvocation{Name: "manage_workers", CallID: "call-1", Arguments: string(raw)})
	if err != nil || res.Error != "" {
		t.Fatalf("worker_v2 plan dispatch failed: %s %v", res.Error, err)
	}
	if !res.RestartTurn {
		t.Fatal("worker proposal did not stop turn")
	}
	p, found, err := sessions.GetAutomationV2Proposal("account", "owner", w.WorkspaceID, "author")
	if err != nil || !found {
		t.Fatalf("proposal not found: %v", err)
	}
	if p.Document.WorkerV2 == nil || p.Document.AutomationV2 == nil {
		t.Fatal("expected mirrored worker_v2 and automation_v2 in proposal document")
	}
	if pending, err := ps.ListPendingPermissions("author", 10); err != nil || len(pending) != 1 || pending[0].Requirement != "automation_v2_acceptance" || pending[0].ToolName != "manage_workers" {
		t.Fatalf("dedicated worker review card missing: %v %v", pending, err)
	}
	if _, found, err := sessions.GetActivePlan("author"); err != nil || found {
		t.Fatalf("worker proposal created an active session plan: %v %v", found, err)
	}
	// Verify manage_workers review in a fresh turn
	invoker2 := svc.newProviderToolInvoker(providerToolInvokerConfig{sessionID: "author", principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "owner", AccountScopeID: "account"}, sessionMode: "auto", runID: "authoring-2", providerManagedV3: true, applySessionMutation: sessions.ApplySessionMutation, agentProfile: profile, policy: policy, terminalPlanState: &terminalPlanToolState{}})
	workersRes, err := invoker2.ExecuteTool(ctx, provideriface.ToolInvocation{Name: "manage_workers", CallID: "call-2", Arguments: `{"action":"review"}`})
	if err != nil || workersRes.Error != "" {
		t.Fatalf("manage_workers review failed: %s %v", workersRes.Error, err)
	}
	if !strings.Contains(workersRes.Output, "worker_review") {
		t.Fatalf("missing worker_review in output: %s", workersRes.Output)
	}
}

// Purpose: verify that manage_workers and manage_automation action="help" do not
// require a target workspace, and that project orchestrator sessions resolve
// target workspaces even when WorkspaceGrants have empty WorkspaceID.
func TestAutomationV2ManageWorkersHelpAndProjectWorkspaceResolution(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ss := store.NewSessionStore(db)
	sessions := session.NewService(ss, nil)
	svc := &Service{sessions: sessions}

	// 1. Session without any workspace grants
	noWsSession := store.SessionSnapshot{
		ID:             "no-ws-session",
		AccountScopeID: "account",
		UserID:         "owner",
		Mode:           "auto",
	}
	if err := ss.CreateSession(noWsSession); err != nil {
		t.Fatal(err)
	}

	// action=help on manage_workers must succeed without workspace
	helpWorkers, err := svc.executeManageAutomationV2Tool("no-ws-session", `{"action":"help"}`)
	if err != nil {
		t.Fatalf("manage_workers action=help failed without workspace: %v", err)
	}
	if !strings.Contains(helpWorkers, "instructions") {
		t.Fatalf("manage_workers action=help expected instructions, got %s", helpWorkers)
	}

	// action=help on manage_automation must succeed without workspace
	helpAuto, err := svc.executeManageAutomationV2Tool("no-ws-session", `{"action":"help"}`)
	if err != nil {
		t.Fatalf("manage_automation action=help failed without workspace: %v", err)
	}
	if !strings.Contains(helpAuto, "instructions") {
		t.Fatalf("manage_automation action=help expected instructions, got %s", helpAuto)
	}

	// 2. Project Orchestrator session where grant has empty WorkspaceID but project has workspace
	projID := "proj_test_123"
	if err := ss.PutProject("account", &store.ProjectRecord{
		ID:        projID,
		AccountID: "account",
		Name:      "Test Project",
		Workspaces: []store.ProjectWorkspaceRef{
			{WorkspaceID: "ws-project-abc", Path: "/workspace/path", Role: "primary_code"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	avail := true
	orchSession := store.SessionSnapshot{
		ID:             "orch-session",
		AccountScopeID: "account",
		UserID:         "owner",
		Mode:           "auto",
		WorkspacePath:  "/workspace/path",
		WorkspaceGrants: []store.WorkspaceGrant{
			{Kind: store.WorkspaceGrantPrimary, Path: "/workspace/path", WorkspaceID: "", Available: &avail},
		},
		Metadata: map[string]any{
			"project_id": projID,
			"role":       "project_orchestrator",
		},
	}
	if err := ss.CreateSession(orchSession); err != nil {
		t.Fatal(err)
	}

	_, resolvedWs, err := svc.automationV2ToolSession("orch-session")
	if err != nil {
		t.Fatalf("automationV2ToolSession failed for orchestrator: %v", err)
	}
	if resolvedWs != "ws-project-abc" {
		t.Fatalf("expected resolved workspace %q, got %q", "ws-project-abc", resolvedWs)
	}

	// Review/context should not fail with "target workspace required"
	reviewOut, err := svc.executeManageAutomationV2Tool("orch-session", `{"action":"review"}`)
	if err != nil {
		t.Fatalf("manage_workers review failed on orchestrator session: %v", err)
	}
	if !strings.Contains(reviewOut, "not_created") {
		t.Fatalf("unexpected review output: %s", reviewOut)
	}
}
