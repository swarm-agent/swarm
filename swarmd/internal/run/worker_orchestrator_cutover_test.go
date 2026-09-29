package run

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: Step 2 Orchestrator worker-tool cutover verification.
// Invariant 1: Only Swarm Orchestrator agent may invoke manage_workers or manage_automation.
// Ordinary chat (swarm) and delegated subagents (coder, finder, designer) are denied before mutation.
// Invariant 2: AI cannot self-approve worker activation or capabilities.
// Invariant 3: manage_workers supports stable worker IDs and canonical shared service across
// inspect, create, update, attach, test, request, pause, resume, archive, delete, and scoped automation disable.
// Threat: crafted calls from non-orchestrator agents or self-activating AI alter background workers.

func setupWorkerOrchestratorTestEnv(t *testing.T) (*Service, *session.Service, *store.SessionStore) {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

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

	wsStore := store.NewWorkspaceStore(db)
	w, err := wsStore.AddForAccount("account", t.TempDir(), "Main Workspace")
	if err != nil {
		t.Fatal(err)
	}

	ss := store.NewSessionStore(db)
	projID := "proj_orch_test"
	if err := ss.PutProject("account", &store.ProjectRecord{
		ID:         projID,
		AccountID:  "account",
		Name:       "Orchestrator Project",
		Workspaces: []store.ProjectWorkspaceRef{{WorkspaceID: w.WorkspaceID, Path: w.Path, Role: "primary_code"}},
	}); err != nil {
		t.Fatal(err)
	}

	avail := true
	orchSession := store.SessionSnapshot{
		ID:             "orch-session",
		AccountScopeID: "account",
		UserID:         "owner",
		Mode:           "auto",
		WorkspacePath:  w.Path,
		WorkspaceGrants: []store.WorkspaceGrant{
			{Kind: store.WorkspaceGrantPrimary, Path: w.Path, WorkspaceID: w.WorkspaceID, Available: &avail},
		},
		Metadata: map[string]any{
			"project_id":          projID,
			"role":                "project_orchestrator",
			"agent_name":          "system-orchestrator",
			"resolved_agent_name": "system-orchestrator",
		},
	}
	if err := ss.CreateSession(orchSession); err != nil {
		t.Fatal(err)
	}

	chatSession := store.SessionSnapshot{
		ID:             "chat-session",
		AccountScopeID: "account",
		UserID:         "owner",
		Mode:           "auto",
		WorkspacePath:  w.Path,
		WorkspaceGrants: []store.WorkspaceGrant{
			{Kind: store.WorkspaceGrantPrimary, Path: w.Path, WorkspaceID: w.WorkspaceID, Available: &avail},
		},
		Metadata: map[string]any{
			"agent_name":          "swarm",
			"resolved_agent_name": "swarm",
		},
	}
	if err := ss.CreateSession(chatSession); err != nil {
		t.Fatal(err)
	}

	events, err := store.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	sessionsSvc := session.NewService(ss, events)
	ps := store.NewPermissionStore(db)
	permissions := permission.NewService(ps, events, nil)
	permissions.SetBypassPermissions(true)
	svc := NewService(sessionsSvc, nil, nil, tool.NewRuntime(1), permissions, nil, nil, events)

	return svc, sessionsSvc, ss
}

func TestOrdinaryChatAndSubagentsCannotManageWorkers(t *testing.T) {
	svc, sessionsSvc, _ := setupWorkerOrchestratorTestEnv(t)

	disallowedProfiles := []struct {
		name    string
		profile store.AgentProfile
	}{
		{"chat_swarm", agent.SwarmAgentProfileForContext(store.AgentProfile{})},
		{"subagent_coder", agent.CoderAgentProfileForParent(store.AgentProfile{})},
		{"subagent_finder", agent.FinderAgentProfileForParent(store.AgentProfile{})},
		{"subagent_designer", agent.DesignerAgentProfileForParent(store.AgentProfile{})},
		{"compact", agent.CompactAgentProfileForParent(store.AgentProfile{})},
	}

	actionsToTest := []string{
		`{"action":"create","name":"Test Worker","instructions":"Do tasks"}`,
		`{"action":"list"}`,
		`{"action":"inspect","worker_id":"worker_test"}`,
		`{"action":"update","worker_id":"worker_test","expected_revision":1,"name":"New"}`,
		`{"action":"attach","worker_id":"worker_test","expected_revision":1,"name":"Auto"}`,
		`{"action":"test","worker_id":"worker_test","prompt":"Run test"}`,
		`{"action":"request","worker_id":"worker_test","prompt":"Direct ask"}`,
		`{"action":"pause","worker_id":"worker_test","expected_revision":1}`,
		`{"action":"delete","worker_id":"worker_test","expected_revision":1}`,
		`{"action":"propose","document":{"title":"W","info":{"goal":"G"},"worker_v2":{"schedule":{"kind":"trigger"}}}}`,
	}

	for _, dp := range disallowedProfiles {
		t.Run(dp.name, func(t *testing.T) {
			invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{
				sessionID:            "chat-session",
				principal:            identity.Principal{Type: identity.PrincipalTypeUser, UserID: "owner", AccountScopeID: "account"},
				sessionMode:          "auto",
				runID:                "run-test-" + dp.name,
				providerManagedV3:    true,
				applySessionMutation: sessionsSvc.ApplySessionMutation,
				agentProfile:         dp.profile,
				terminalPlanState:    &terminalPlanToolState{},
			})

			for _, args := range actionsToTest {
				// 1. Check provider invoker denial
				res, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{
					Name:      "manage_workers",
					CallID:    "call-test",
					Arguments: args,
				})
				if err != nil {
					t.Fatalf("unexpected invocation error: %v", err)
				}
				if !strings.Contains(res.Error, "exclusive to Swarm Orchestrator") {
					t.Fatalf("expected orchestrator exclusivity error for profile %s with args %s, got: %q", dp.name, args, res.Error)
				}

				// Check manage_automation alias also denied
				resAuto, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{
					Name:      "manage_automation",
					CallID:    "call-test-auto",
					Arguments: args,
				})
				if err != nil {
					t.Fatalf("unexpected invocation error: %v", err)
				}
				if !strings.Contains(resAuto.Error, "exclusive to Swarm Orchestrator") {
					t.Fatalf("expected orchestrator exclusivity error for manage_automation profile %s, got: %q", dp.name, resAuto.Error)
				}

				// 2. Check control plane dispatch denial
				handled, cpRes, cpErr := svc.executeControlPlaneTool(context.Background(), "chat-session", "auto", dp.profile, 1, tool.Call{
					Name:      "manage_workers",
					CallID:    "call-cp",
					Arguments: args,
				}, "", nil)
				if !handled {
					t.Fatalf("control plane should handle manage_workers for profile %s", dp.name)
				}
				if cpErr == nil || !strings.Contains(cpErr.Error(), "exclusive to Swarm Orchestrator") {
					t.Fatalf("expected control plane error for profile %s, got: %v", dp.name, cpErr)
				}
				if !strings.Contains(cpRes.Error, "exclusive to Swarm Orchestrator") {
					t.Fatalf("expected control plane result error for profile %s, got: %s", dp.name, cpRes.Error)
				}
			}
		})
	}

	// Invariant: underlying store remains completely unchanged (0 workers)
	listRes, err := sessionsSvc.ListWorkers("account", store.ListWorkersQuery{})
	if err != nil {
		t.Fatalf("ListWorkers failed: %v", err)
	}
	if len(listRes.Workers) != 0 {
		t.Fatalf("expected 0 workers in store after denied crafted calls, found %d", len(listRes.Workers))
	}
}

func TestWorkerActivationAISelfApprovalDenied(t *testing.T) {
	svc, sessionsSvc, _ := setupWorkerOrchestratorTestEnv(t)
	orchProfile := agent.SwarmOrchestratorAgentProfileForContext(store.AgentProfile{})

	invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{
		sessionID:            "orch-session",
		principal:            identity.Principal{Type: identity.PrincipalTypeUser, UserID: "owner", AccountScopeID: "account"},
		sessionMode:          "auto",
		runID:                "run-orch-activate",
		providerManagedV3:    true,
		applySessionMutation: sessionsSvc.ApplySessionMutation,
		agentProfile:         orchProfile,
		terminalPlanState:    &terminalPlanToolState{},
	})

	for _, act := range []string{"activate", "approve"} {
		t.Run(act, func(t *testing.T) {
			res, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{
				Name:      "manage_workers",
				CallID:    "call-act-" + act,
				Arguments: fmt.Sprintf(`{"action":%q,"worker_id":"worker_123"}`, act),
			})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(res.Error, "AI cannot self-approve") || !strings.Contains(res.Error, "user approval") {
				t.Fatalf("expected self-approval rejection error for action %s, got: %q", act, res.Error)
			}
		})
	}
}

func TestOrchestratorWorkerLifecycleActions(t *testing.T) {
	svc, sessionsSvc, execution, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	orchProfile := agent.SwarmOrchestratorAgentProfileForContext(store.AgentProfile{})

	invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{
		sessionID:            "orch-session",
		principal:            identity.Principal{Type: identity.PrincipalTypeUser, UserID: "owner", AccountScopeID: "account"},
		sessionMode:          "auto",
		runID:                "run-orch-lifecycle",
		providerManagedV3:    true,
		applySessionMutation: sessionsSvc.ApplySessionMutation,
		agentProfile:         orchProfile,
		terminalPlanState:    &terminalPlanToolState{},
	})

	callNumber := 0
	callTool := func(args map[string]any) map[string]any {
		t.Helper()
		callNumber++
		raw, _ := json.Marshal(args)
		res, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{
			Name:      "manage_workers",
			CallID:    fmt.Sprintf("call-step-%d", callNumber),
			Arguments: string(raw),
		})
		if err != nil {
			t.Fatalf("ExecuteTool failed: %v", err)
		}
		if res.Error != "" {
			t.Fatalf("ExecuteTool returned error for args %s: %s", string(raw), res.Error)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
			t.Fatalf("failed to unmarshal output: %v, raw: %s", err, res.Output)
		}
		return out
	}

	// 1. action=help
	helpOut := callTool(map[string]any{"action": "help"})
	if helpOut["status"] != "ok" || !strings.Contains(fmt.Sprint(helpOut["instructions"]), "Worker") {
		t.Fatalf("help failed: %v", helpOut)
	}

	// 2. action=create
	createOut := callTool(map[string]any{
		"action":       "create",
		"name":         "Code Reviewer",
		"description":  "Performs automated reviews",
		"instructions": "Review PR diffs thoroughly",

		"workspace_requirements": []map[string]any{
			{"role": "primary", "required": true},
		},
	})
	if createOut["status"] != "created" {
		t.Fatalf("expected status created, got %v", createOut)
	}
	workerData, _ := createOut["worker"].(map[string]any)
	workerID := fmt.Sprint(workerData["id"])
	if workerID == "" || !strings.HasPrefix(workerID, "worker_") {
		t.Fatalf("expected stable worker ID prefix worker_, got %q", workerID)
	}
	if workerData["lifecycle_state"] != "idle" {
		t.Fatalf("expected idle lifecycle state on create, got %v", workerData["lifecycle_state"])
	}
	if uint64(workerData["revision"].(float64)) != 1 {
		t.Fatalf("expected revision 1, got %v", workerData["revision"])
	}

	// 3. action=inspect
	inspectOut := callTool(map[string]any{
		"action":    "inspect",
		"worker_id": workerID,
	})
	if inspectOut["found"] != true {
		t.Fatalf("inspect expected found=true, got %v", inspectOut)
	}
	inspectedWorker, _ := inspectOut["worker"].(map[string]any)
	if inspectedWorker["id"] != workerID || inspectedWorker["name"] != "Code Reviewer" {
		t.Fatalf("mismatched inspected worker: %v", inspectedWorker)
	}

	// 4. action=list
	listOut := callTool(map[string]any{"action": "list"})
	workersList, _ := listOut["workers"].([]any)
	if len(workersList) != 1 {
		t.Fatalf("expected 1 worker in list, got %d", len(workersList))
	}

	// 5. action=update with expected_revision
	updateOut := callTool(map[string]any{
		"action":            "update",
		"worker_id":         workerID,
		"expected_revision": 1,
		"name":              "Code Reviewer Specialist",
		"change_summary":    "Updated display name",
	})
	if updateOut["status"] != "updated" {
		t.Fatalf("update expected status updated, got %v", updateOut)
	}
	updatedWorker, _ := updateOut["worker"].(map[string]any)
	if updatedWorker["name"] != "Code Reviewer Specialist" {
		t.Fatalf("expected updated name, got %v", updatedWorker["name"])
	}
	if uint64(updatedWorker["revision"].(float64)) != 2 {
		t.Fatalf("expected revision 2, got %v", updatedWorker["revision"])
	}

	// 6. action=update with stale expected_revision must fail
	staleArgs, _ := json.Marshal(map[string]any{
		"action":            "update",
		"worker_id":         workerID,
		"expected_revision": 1, // stale!
		"name":              "Conflict Name",
	})
	staleRes, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{
		Name:      "manage_workers",
		CallID:    "call-stale",
		Arguments: string(staleArgs),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(staleRes.Error, "conflict") {
		t.Fatalf("expected revision conflict error, got: %q", staleRes.Error)
	}

	// 7. action=attach automation
	attachOut := callTool(map[string]any{
		"action":            "attach",
		"worker_id":         workerID,
		"expected_revision": 2,
		"name":              "Daily PR Audit",
		"activation_mode":   "manual",
		"description":       "Runs daily audit",
		"plan_document":     store.SessionPlanDocument{Title: "Audit", Info: store.SessionPlanInfo{Goal: "Review repository"}, Checkpoints: []store.SessionPlanCheckpoint{{ID: "cp-1", Order: 1, Title: "Review", Status: "pending", Tasks: []string{"Review repository"}, AcceptanceCriteria: []string{"Review delivered"}}}},
	})
	if attachOut["status"] != "attached" {
		t.Fatalf("attach expected status attached, got %v", attachOut)
	}
	attachedWorker, _ := attachOut["worker"].(map[string]any)
	automations, _ := attachedWorker["automations"].([]any)
	if len(automations) != 1 {
		t.Fatalf("expected 1 attached automation, got %d", len(automations))
	}
	autoMap, _ := automations[0].(map[string]any)
	autoID := fmt.Sprint(autoMap["id"])
	if autoID == "" || !strings.HasPrefix(autoID, "wauto_") {
		t.Fatalf("expected automation ID prefix wauto_, got %q", autoID)
	}
	if uint64(attachedWorker["revision"].(float64)) != 3 {
		t.Fatalf("expected revision 3, got %v", attachedWorker["revision"])
	}

	// User approval activates bindings; AI cannot self-approve.
	if _, err := execution.Activate("account", "owner", workerID, 3, map[string]string{"primary": workspaceID}); err != nil {
		t.Fatal(err)
	}
	// 8. action=test (labelled test run)
	testOut := callTool(map[string]any{
		"action":          "test",
		"worker_id":       workerID,
		"automation_id":   autoID,
		"prompt":          "Run test verification",
		"idempotency_key": "tool-test",
	})
	if testOut["status"] != "running" {
		t.Fatalf("test expected status admitted and is_test=true, got %v", testOut)
	}
	testRun, _ := testOut["run"].(map[string]any)
	if testRun["request_source"] != "test_run" || testRun["worker_id"] != workerID {
		t.Fatalf("mismatched test run: %v", testRun)
	}

	// 9. action=request (direct request)
	reqOut := callTool(map[string]any{
		"action":          "request",
		"worker_id":       workerID,
		"prompt":          "Review commit abc1234",
		"idempotency_key": "tool-request",
	})
	if reqOut["status"] != "running" {
		t.Fatalf("request expected status admitted and is_test=false, got %v", reqOut)
	}
	reqRun, _ := reqOut["run"].(map[string]any)
	if reqRun["request_source"] != "orchestrator" || reqRun["worker_id"] != workerID {
		t.Fatalf("mismatched direct run: %v", reqRun)
	}

	// 10. action=disable_automation (scoped automation disable)
	disableOut := callTool(map[string]any{
		"action":            "disable_automation",
		"worker_id":         workerID,
		"automation_id":     autoID,
		"expected_revision": 4,
	})
	if disableOut["status"] != "automation_disabled" {
		t.Fatalf("disable_automation expected status automation_disabled, got %v", disableOut)
	}
	disWorker, _ := disableOut["worker"].(map[string]any)
	disAutos, _ := disWorker["automations"].([]any)
	disAuto0, _ := disAutos[0].(map[string]any)
	if disAuto0["enabled"] != false {
		t.Fatalf("expected automation enabled=false, got %v", disAuto0["enabled"])
	}
	if uint64(disWorker["revision"].(float64)) != 5 {
		t.Fatalf("expected revision 4, got %v", disWorker["revision"])
	}

	// 11. action=pause
	pauseOut := callTool(map[string]any{
		"action":            "pause",
		"worker_id":         workerID,
		"expected_revision": 5,
	})
	if pauseOut["status"] != "paused" {
		t.Fatalf("pause expected status paused, got %v", pauseOut)
	}
	pausedWorker, _ := pauseOut["worker"].(map[string]any)
	if uint64(pausedWorker["revision"].(float64)) != 7 {
		t.Fatalf("expected revision 5, got %v", pausedWorker["revision"])
	}

	// Direct request to paused worker must be rejected
	pausedReqArgs, _ := json.Marshal(map[string]any{
		"action":          "request",
		"worker_id":       workerID,
		"prompt":          "Cannot run while paused",
		"idempotency_key": "paused-request",
	})
	pausedReqRes, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{
		Name:      "manage_workers",
		CallID:    "call-paused-req",
		Arguments: string(pausedReqArgs),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pausedReqRes.Error, "admission closed (paused)") {
		t.Fatalf("expected admission closed error for paused worker, got: %q", pausedReqRes.Error)
	}

	// 12. action=resume
	resumeOut := callTool(map[string]any{
		"action":            "resume",
		"worker_id":         workerID,
		"expected_revision": 7,
	})
	if resumeOut["status"] != "active" {
		t.Fatalf("resume expected status resumed, got %v", resumeOut)
	}
	resumedWorker, _ := resumeOut["worker"].(map[string]any)
	if uint64(resumedWorker["revision"].(float64)) != 8 {
		t.Fatalf("expected revision 6, got %v", resumedWorker["revision"])
	}

	// Request after resume succeeds
	afterResumeOut := callTool(map[string]any{
		"action":          "request",
		"worker_id":       workerID,
		"prompt":          "Runs fine after resume",
		"idempotency_key": "resumed-request",
	})
	if afterResumeOut["status"] != "running" {
		t.Fatalf("request after resume failed: %v", afterResumeOut)
	}

	// 13. action=delete
	deleteOut := callTool(map[string]any{
		"action":            "delete",
		"worker_id":         workerID,
		"expected_revision": 8,
	})
	if deleteOut["status"] != "deleted" {
		t.Fatalf("delete expected status deleted, got %v", deleteOut)
	}

	// Inspect deleted worker returns not found
	delInspectArgs, _ := json.Marshal(map[string]any{
		"action":    "inspect",
		"worker_id": workerID,
	})
	delInspectRes, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{
		Name:      "manage_workers",
		CallID:    "call-del-inspect",
		Arguments: string(delInspectArgs),
	})
	if err != nil {
		t.Fatal(err)
	}
	var tombstone struct {
		Worker store.WorkerRecord `json:"worker"`
	}
	if err := json.Unmarshal([]byte(delInspectRes.Output), &tombstone); err != nil || tombstone.Worker.LifecycleState != store.WorkerLifecycleStateDeleted {
		t.Fatalf("expected retained deleted tombstone: %s %v", delInspectRes.Output, err)
	}
}

func TestScopedAutomationDisableDoesNotStopUnrelatedRuns(t *testing.T) {
	svc, sessionsSvc, _ := setupWorkerOrchestratorTestEnv(t)
	svc.SetWorkerExecutionService(&WorkerExecutionService{host: &AutomationV2ExecutionHost{runs: svc, apply: sessionsSvc.ApplySessionMutation}})
	plan := store.SessionPlanDocument{Title: "Review", Info: store.SessionPlanInfo{Goal: "Review"}, Checkpoints: []store.SessionPlanCheckpoint{{ID: "cp-1", Order: 1, Title: "Review", Status: "pending", Tasks: []string{"Review"}, AcceptanceCriteria: []string{"Reviewed"}}}}
	orchProfile := agent.SwarmOrchestratorAgentProfileForContext(store.AgentProfile{})

	invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{
		sessionID:            "orch-session",
		principal:            identity.Principal{Type: identity.PrincipalTypeUser, UserID: "owner", AccountScopeID: "account"},
		sessionMode:          "auto",
		runID:                "run-orch-scoped",
		providerManagedV3:    true,
		applySessionMutation: sessionsSvc.ApplySessionMutation,
		agentProfile:         orchProfile,
		terminalPlanState:    &terminalPlanToolState{},
	})

	callNumber := 0
	callTool := func(args map[string]any) map[string]any {
		t.Helper()
		callNumber++
		raw, _ := json.Marshal(args)
		res, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{
			Name:      "manage_workers",
			CallID:    fmt.Sprintf("call-step-%d", callNumber),
			Arguments: string(raw),
		})
		if err != nil {
			t.Fatalf("ExecuteTool failed: %v", err)
		}
		if res.Error != "" {
			t.Fatalf("ExecuteTool returned error for args %s: %s", string(raw), res.Error)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
			t.Fatalf("failed to unmarshal output: %v, raw: %s", err, res.Output)
		}
		return out
	}

	// Create worker
	cOut := callTool(map[string]any{
		"action":       "create",
		"name":         "Dual Automation Worker",
		"instructions": "Execute assigned automations",
	})
	wData := cOut["worker"].(map[string]any)
	workerID := fmt.Sprint(wData["id"])

	// Attach Auto 1
	a1Out := callTool(map[string]any{
		"action":            "attach",
		"worker_id":         workerID,
		"expected_revision": 1,
		"name":              "Auto One",
		"plan_document":     plan,
	})
	w1 := a1Out["worker"].(map[string]any)
	autos1 := w1["automations"].([]any)
	auto1ID := fmt.Sprint(autos1[0].(map[string]any)["id"])

	// Attach Auto 2
	a2Out := callTool(map[string]any{
		"action":            "attach",
		"worker_id":         workerID,
		"expected_revision": 2,
		"name":              "Auto Two",
		"plan_document":     plan,
	})
	w2 := a2Out["worker"].(map[string]any)
	autos2 := w2["automations"].([]any)
	var auto2ID string
	for _, a := range autos2 {
		id := fmt.Sprint(a.(map[string]any)["id"])
		if id != auto1ID {
			auto2ID = id
			break
		}
	}

	// Create run for Auto 1
	run1, err := sessionsSvc.RecordWorkerRun("account", store.WorkerRunRecord{
		ID:           store.GenerateWorkerRunID(),
		WorkerID:     workerID,
		AutomationID: auto1ID,
		SessionID:    "undispatched-one",
		Status:       "admitted",
		CreatedAt:    100,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create run for Auto 2
	run2, err := sessionsSvc.RecordWorkerRun("account", store.WorkerRunRecord{
		ID:           store.GenerateWorkerRunID(),
		WorkerID:     workerID,
		AutomationID: auto2ID,
		SessionID:    "undispatched-two",
		Status:       "admitted",
		CreatedAt:    200,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Disable Auto 1
	disableOut := callTool(map[string]any{
		"action":            "disable_automation",
		"worker_id":         workerID,
		"automation_id":     auto1ID,
		"expected_revision": 3,
	})
	if disableOut["status"] != "automation_disabled" {
		t.Fatalf("disable failed: %v", disableOut)
	}

	// Verify run1 was cancelled
	storedRun1, found, err := sessionsSvc.GetWorkerRun("account", workerID, run1.ID)
	if err != nil || !found {
		t.Fatalf("GetWorkerRun run1 failed: %v", err)
	}
	if storedRun1.Status != "cancelled" {
		t.Fatalf("expected run1 to be cancelled by disable_automation, got %s", storedRun1.Status)
	}

	// Invariant: run2 for Auto 2 was NOT stopped and remains admitted!
	storedRun2, found, err := sessionsSvc.GetWorkerRun("account", workerID, run2.ID)
	if err != nil || !found {
		t.Fatalf("GetWorkerRun run2 failed: %v", err)
	}
	if storedRun2.Status != "admitted" {
		t.Fatalf("expected unrelated run2 to remain admitted, got %s", storedRun2.Status)
	}
}
