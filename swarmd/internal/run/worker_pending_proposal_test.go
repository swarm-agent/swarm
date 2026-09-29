package run

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: Pending durable worker proposal and human-only acceptance verification.
// Requirement 1: Orchestrator proposals persist canonical WorkerRecord in pending lifecycle state with proposed bindings.
// Requirement 2: Zero-job specialists and scheduled automations are both supported without a project requirement.
// Requirement 3: Pending workers reject direct runs, tests, schedules, triggers, and activate bypass before explicit user acceptance.
// Requirement 4: Stale revisions, cross-account callers, and AI self-approval are strictly rejected.
// Requirement 5: Human acceptance atomically transitions pending worker to active with approved bindings; no immediate unsolicited run is created.
// Threat: AI agents or forged requests bypass human review to create or activate background workers.

func TestWorkerPendingProposal_ZeroJobsAndAutomations(t *testing.T) {
	svc, sessionsSvc, _, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	orchProfile := agent.SwarmOrchestratorAgentProfileForContext(store.AgentProfile{})

	invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{
		sessionID:            "orch-session",
		principal:            identity.Principal{Type: identity.PrincipalTypeUser, UserID: "owner", AccountScopeID: "account"},
		sessionMode:          "auto",
		runID:                "run-proposal-test",
		providerManagedV3:    true,
		applySessionMutation: sessionsSvc.ApplySessionMutation,
		agentProfile:         orchProfile,
		terminalPlanState:    &terminalPlanToolState{},
	})

	// 1. Propose worker with zero jobs (flat format)
	zeroJobArgs := fmt.Sprintf(`{
		"action": "propose",
		"name": "Zero Job Auditor",
		"description": "Performs audits on demand",
		"instructions": "Audit when requested",
		"workspace_id": %q
	}`, workspaceID)

	res, err := svc.newProviderToolInvoker(invoker.(*providerToolInvoker).config).ExecuteTool(context.Background(), provideriface.ToolInvocation{
		Name:      "manage_workers",
		CallID:    "call-zero-job",
		Arguments: zeroJobArgs,
	})
	if err != nil {
		t.Fatalf("zero job proposal invocation failed: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("zero job proposal returned error: %s", res.Error)
	}

	var zeroJobOut map[string]any
	if err := json.Unmarshal([]byte(res.Output), &zeroJobOut); err != nil {
		t.Fatalf("unmarshal proposal output: %v, raw: %s", err, res.Output)
	}
	if zeroJobOut["status"] != "pending_review" {
		t.Fatalf("expected status pending_review, got %v", zeroJobOut["status"])
	}
	if zeroJobOut["lifecycle_state"] != "pending" {
		t.Fatalf("expected lifecycle_state pending, got %v", zeroJobOut["lifecycle_state"])
	}
	if zeroJobOut["next_action"] != "await_worker_acceptance" {
		t.Fatalf("expected next_action await_worker_acceptance, got %v", zeroJobOut["next_action"])
	}
	workerID := fmt.Sprint(zeroJobOut["worker_id"])
	if workerID == "" || !strings.HasPrefix(workerID, "worker_") {
		t.Fatalf("expected worker_id with prefix worker_, got %q", workerID)
	}

	// Verify durable record in Pebble store
	w, found, err := sessionsSvc.GetWorker("account", workerID)
	if err != nil || !found {
		t.Fatalf("failed to retrieve worker from store: %v, found=%v", err, found)
	}
	if w.LifecycleState != store.WorkerLifecycleStatePending {
		t.Fatalf("expected pending lifecycle state in store, got %s", w.LifecycleState)
	}
	if w.Revision != 1 {
		t.Fatalf("expected revision 1, got %d", w.Revision)
	}
	if len(w.Automations) != 0 {
		t.Fatalf("expected 0 automations for zero job worker, got %d", len(w.Automations))
	}
	if len(w.LocalBindings) != 0 {
		t.Fatalf("expected empty local bindings before acceptance, got %v", w.LocalBindings)
	}
	if w.ProposedBindings == nil || w.ProposedBindings["primary"] != workspaceID {
		t.Fatalf("expected proposed primary binding %q, got %v", workspaceID, w.ProposedBindings)
	}
	if w.Instructions != "Audit when requested" {
		t.Fatalf("expected exact instructions, got %q", w.Instructions)
	}

	// 2. Propose worker with attached automation plan (legacy document format)
	doc := map[string]any{
		"title": "Scheduled Reviewer",
		"info":  map[string]any{"goal": "Review code daily", "context": "Review pull requests"},
		"worker_v2": map[string]any{
			"schema_version": 2,
			"workspace_id":   workspaceID,
			"schedule": map[string]any{
				"kind":             "interval",
				"interval_seconds": 3600,
			},
		},
		"checkpoints": []map[string]any{
			{
				"id":                  "cp-1",
				"order":               1,
				"status":              "pending",
				"title":               "Inspect PRs",
				"tasks":               []string{"Fetch diff", "Check invariants"},
				"acceptance_criteria": []string{"Report produced"},
			},
		},
	}
	docBytes, _ := json.Marshal(doc)
	jobProposalArgs := fmt.Sprintf(`{"action":"propose","document":%s}`, string(docBytes))

	res2, err := svc.newProviderToolInvoker(invoker.(*providerToolInvoker).config).ExecuteTool(context.Background(), provideriface.ToolInvocation{
		Name:      "manage_workers",
		CallID:    "call-job-proposal",
		Arguments: jobProposalArgs,
	})
	if err != nil {
		t.Fatalf("job proposal invocation failed: %v", err)
	}
	if res2.Error != "" {
		t.Fatalf("job proposal returned error: %s", res2.Error)
	}
	var jobOut map[string]any
	if err := json.Unmarshal([]byte(res2.Output), &jobOut); err != nil {
		t.Fatalf("unmarshal job output: %v", err)
	}
	jobWorkerID := fmt.Sprint(jobOut["worker_id"])
	jw, found, err := sessionsSvc.GetWorker("account", jobWorkerID)
	if err != nil || !found {
		t.Fatalf("failed to retrieve job worker: %v", err)
	}
	if jw.LifecycleState != store.WorkerLifecycleStatePending {
		t.Fatalf("expected pending lifecycle state, got %s", jw.LifecycleState)
	}
	if len(jw.Automations) != 1 {
		t.Fatalf("expected 1 attached automation, got %d", len(jw.Automations))
	}
	if jw.Automations[0].ActivationMode != "interval" || jw.Automations[0].Schedule == nil || jw.Automations[0].Schedule.IntervalSeconds != 3600 {
		t.Fatalf("expected interval schedule, got %+v", jw.Automations[0].Schedule)
	}
}

func TestWorkerPendingProposal_NoRunCreatedPreAccept(t *testing.T) {
	svc, sessionsSvc, execution, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	orchProfile := agent.SwarmOrchestratorAgentProfileForContext(store.AgentProfile{})

	invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{
		sessionID:            "orch-session",
		principal:            identity.Principal{Type: identity.PrincipalTypeUser, UserID: "owner", AccountScopeID: "account"},
		sessionMode:          "auto",
		runID:                "run-norun-test",
		providerManagedV3:    true,
		applySessionMutation: sessionsSvc.ApplySessionMutation,
		agentProfile:         orchProfile,
		terminalPlanState:    &terminalPlanToolState{},
	})

	res, err := svc.newProviderToolInvoker(invoker.(*providerToolInvoker).config).ExecuteTool(context.Background(), provideriface.ToolInvocation{
		Name:      "manage_workers",
		CallID:    "call-prop",
		Arguments: fmt.Sprintf(`{"action":"propose","name":"Pending Worker","instructions":"Do tasks","workspace_id":%q}`, workspaceID),
	})
	if err != nil || res.Error != "" {
		t.Fatalf("proposal failed: %v / %s", err, res.Error)
	}
	var out map[string]any
	_ = json.Unmarshal([]byte(res.Output), &out)
	workerID := fmt.Sprint(out["worker_id"])

	// 1. Verify zero runs exist pre-accept
	runs, _, err := sessionsSvc.ListWorkerRuns("account", workerID, 10, "")
	if err != nil {
		t.Fatalf("list worker runs failed: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("expected 0 runs pre-accept, got %d", len(runs))
	}

	// 2. Direct dispatch must reject pending worker
	_, err = execution.Dispatch(context.Background(), "account", "owner", store.WorkerRunAdmission{
		WorkerID:      workerID,
		RequestSource: "direct",
		Input:         map[string]any{"task": "Run direct task"},
	})
	if err == nil {
		t.Fatal("expected direct dispatch on pending worker to fail")
	}

	// 3. Test run dispatch must reject pending worker
	_, err = execution.Dispatch(context.Background(), "account", "owner", store.WorkerRunAdmission{
		WorkerID:      workerID,
		RequestSource: "test_run",
		Input:         map[string]any{"prompt": "Run test"},
	})
	if err == nil {
		t.Fatal("expected test dispatch on pending worker to fail")
	}

	// 4. Activate bypass must reject pending worker
	_, err = execution.Activate("account", "owner", workerID, 1, map[string]string{"primary": workspaceID})
	if err == nil {
		t.Fatal("expected execution.Activate on pending worker to fail")
	}

	// 5. Zero runs still exist after rejected dispatch attempts
	runsAfter, _, err := sessionsSvc.ListWorkerRuns("account", workerID, 10, "")
	if err != nil {
		t.Fatalf("list worker runs failed: %v", err)
	}
	if len(runsAfter) != 0 {
		t.Fatalf("expected 0 runs after rejected attempts, got %d", len(runsAfter))
	}
}

func TestWorkerPendingProposal_AcceptanceTransitionsToActive(t *testing.T) {
	svc, sessionsSvc, execution, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	orchProfile := agent.SwarmOrchestratorAgentProfileForContext(store.AgentProfile{})

	invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{
		sessionID:            "orch-session",
		principal:            identity.Principal{Type: identity.PrincipalTypeUser, UserID: "owner", AccountScopeID: "account"},
		sessionMode:          "auto",
		runID:                "run-accept-test",
		providerManagedV3:    true,
		applySessionMutation: sessionsSvc.ApplySessionMutation,
		agentProfile:         orchProfile,
		terminalPlanState:    &terminalPlanToolState{},
	})

	res, err := svc.newProviderToolInvoker(invoker.(*providerToolInvoker).config).ExecuteTool(context.Background(), provideriface.ToolInvocation{
		Name:      "manage_workers",
		CallID:    "call-prop-accept",
		Arguments: fmt.Sprintf(`{"action":"propose","name":"Acceptance Worker","instructions":"Standing instructions","workspace_id":%q}`, workspaceID),
	})
	if err != nil || res.Error != "" {
		t.Fatalf("proposal failed: %v / %s", err, res.Error)
	}
	var out map[string]any
	_ = json.Unmarshal([]byte(res.Output), &out)
	workerID := fmt.Sprint(out["worker_id"])

	// Human accepts the proposal with expected_revision: 1
	accepted, err := execution.Accept("account", "owner", workerID, 1)
	if err != nil {
		t.Fatalf("acceptance failed: %v", err)
	}
	if accepted.LifecycleState != store.WorkerLifecycleStateActive {
		t.Fatalf("expected active lifecycle state after acceptance, got %s", accepted.LifecycleState)
	}
	if accepted.Revision != 2 {
		t.Fatalf("expected revision 2 after acceptance, got %d", accepted.Revision)
	}
	if accepted.LocalBindings["primary"] != workspaceID {
		t.Fatalf("expected local binding primary=%q, got %v", workspaceID, accepted.LocalBindings)
	}

	// Verify revision history contains acceptance summary
	history, _, err := sessionsSvc.GetWorkerHistory("account", workerID, 10, "")
	if err != nil {
		t.Fatalf("get history failed: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 revisions in history, got %d", len(history))
	}
	if history[1].ChangeSummary != "accepted worker proposal" {
		t.Fatalf("expected change summary 'accepted worker proposal', got %q", history[1].ChangeSummary)
	}

	// Verify no immediate run was dispatched
	runs, _, err := sessionsSvc.ListWorkerRuns("account", workerID, 10, "")
	if err != nil {
		t.Fatalf("list runs failed: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("expected 0 runs immediately after acceptance, got %d", len(runs))
	}

	// Subsequent acceptance on already active worker must fail
	_, err = execution.Accept("account", "owner", workerID, 2)
	if err == nil {
		t.Fatal("expected acceptance on already active worker to fail")
	}
}

func TestWorkerPendingProposal_RejectionCases(t *testing.T) {
	svc, sessionsSvc, execution, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	orchProfile := agent.SwarmOrchestratorAgentProfileForContext(store.AgentProfile{})

	invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{
		sessionID:            "orch-session",
		principal:            identity.Principal{Type: identity.PrincipalTypeUser, UserID: "owner", AccountScopeID: "account"},
		sessionMode:          "auto",
		runID:                "run-rejection-test",
		providerManagedV3:    true,
		applySessionMutation: sessionsSvc.ApplySessionMutation,
		agentProfile:         orchProfile,
		terminalPlanState:    &terminalPlanToolState{},
	})

	res, err := svc.newProviderToolInvoker(invoker.(*providerToolInvoker).config).ExecuteTool(context.Background(), provideriface.ToolInvocation{
		Name:      "manage_workers",
		CallID:    "call-rejections",
		Arguments: fmt.Sprintf(`{"action":"propose","name":"Rejection Worker","instructions":"Tasks","workspace_id":%q}`, workspaceID),
	})
	if err != nil || res.Error != "" {
		t.Fatalf("proposal failed: %v / %s", err, res.Error)
	}
	var out map[string]any
	_ = json.Unmarshal([]byte(res.Output), &out)
	workerID := fmt.Sprint(out["worker_id"])

	// 1. Stale expected_revision rejection
	_, err = execution.Accept("account", "owner", workerID, 0)
	if err == nil {
		t.Fatal("expected acceptance with revision 0 to fail")
	}
	_, err = execution.Accept("account", "owner", workerID, 99)
	if err == nil {
		t.Fatal("expected acceptance with stale revision 99 to fail")
	}

	// 2. Cross-account rejection
	_, err = execution.Accept("foreign-account", "owner", workerID, 1)
	if err == nil {
		t.Fatal("expected cross-account acceptance to fail")
	}

	// 3. AI self-approval rejection
	for _, act := range []string{"accept", "activate", "approve"} {
		aiRes, aiErr := svc.newProviderToolInvoker(invoker.(*providerToolInvoker).config).ExecuteTool(context.Background(), provideriface.ToolInvocation{
			Name:      "manage_workers",
			CallID:    "call-ai-" + act,
			Arguments: fmt.Sprintf(`{"action":%q,"worker_id":%q,"expected_revision":1}`, act, workerID),
		})
		if aiErr != nil {
			t.Fatal(aiErr)
		}
		if !strings.Contains(aiRes.Error, "AI cannot self-approve") || !strings.Contains(aiRes.Error, "user approval") {
			t.Fatalf("expected self-approval rejection for action %s, got: %q", act, aiRes.Error)
		}
	}

	// 4. Verify worker state is completely unchanged
	w, found, err := sessionsSvc.GetWorker("account", workerID)
	if err != nil || !found {
		t.Fatalf("get worker: %v", err)
	}
	if w.LifecycleState != store.WorkerLifecycleStatePending || w.Revision != 1 || len(w.LocalBindings) != 0 {
		t.Fatalf("worker state mutated after rejections: %+v", w)
	}
}

func TestWorkerPendingProposal_ReproposalAndUpdates(t *testing.T) {
	svc, sessionsSvc, _, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	orchProfile := agent.SwarmOrchestratorAgentProfileForContext(store.AgentProfile{})

	invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{
		sessionID:            "orch-session",
		principal:            identity.Principal{Type: identity.PrincipalTypeUser, UserID: "owner", AccountScopeID: "account"},
		sessionMode:          "auto",
		runID:                "run-reprop-test",
		providerManagedV3:    true,
		applySessionMutation: sessionsSvc.ApplySessionMutation,
		agentProfile:         orchProfile,
		terminalPlanState:    &terminalPlanToolState{},
	})

	// 1. Initial proposal
	res, err := svc.newProviderToolInvoker(invoker.(*providerToolInvoker).config).ExecuteTool(context.Background(), provideriface.ToolInvocation{
		Name:      "manage_workers",
		CallID:    "call-init",
		Arguments: fmt.Sprintf(`{"action":"propose","name":"Draft Specialist","instructions":"Initial instructions","workspace_id":%q}`, workspaceID),
	})
	if err != nil || res.Error != "" {
		t.Fatalf("initial proposal failed: %v / %s", err, res.Error)
	}
	var out map[string]any
	_ = json.Unmarshal([]byte(res.Output), &out)
	workerID := fmt.Sprint(out["worker_id"])

	// 2. Reproposal with expected_revision: 1
	repropArgs := fmt.Sprintf(`{
		"action": "propose",
		"worker_id": %q,
		"expected_revision": 1,
		"name": "Revised Specialist",
		"instructions": "Revised instructions",
		"workspace_id": %q
	}`, workerID, workspaceID)

	res2, err := svc.newProviderToolInvoker(invoker.(*providerToolInvoker).config).ExecuteTool(context.Background(), provideriface.ToolInvocation{
		Name:      "manage_workers",
		CallID:    "call-reprop",
		Arguments: repropArgs,
	})
	if err != nil || res2.Error != "" {
		t.Fatalf("reproposal failed: %v / %s", err, res2.Error)
	}

	w, found, err := sessionsSvc.GetWorker("account", workerID)
	if err != nil || !found {
		t.Fatalf("get worker: %v", err)
	}
	if w.Name != "Revised Specialist" || w.Instructions != "Revised instructions" {
		t.Fatalf("expected updated fields, got name=%q instructions=%q", w.Name, w.Instructions)
	}
	if w.Revision != 2 {
		t.Fatalf("expected revision 2 after reproposal, got %d", w.Revision)
	}
	if w.LifecycleState != store.WorkerLifecycleStatePending {
		t.Fatalf("expected worker to remain pending, got %s", w.LifecycleState)
	}

	// 3. Stale reproposal must fail
	staleArgs := fmt.Sprintf(`{
		"action": "propose",
		"worker_id": %q,
		"expected_revision": 1,
		"name": "Stale Attempt",
		"instructions": "Do tasks"
	}`, workerID)

	resStale, err := svc.newProviderToolInvoker(invoker.(*providerToolInvoker).config).ExecuteTool(context.Background(), provideriface.ToolInvocation{
		Name:      "manage_workers",
		CallID:    "call-stale-reprop",
		Arguments: staleArgs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resStale.Error, "conflict") {
		t.Fatalf("expected conflict error on stale reproposal, got: %s", resStale.Error)
	}
}

func TestWorkerOrchestratorCreate_CannotBypassPending(t *testing.T) {
	svc, sessionsSvc, execution, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	orchProfile := agent.SwarmOrchestratorAgentProfileForContext(store.AgentProfile{})

	invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{
		sessionID:            "orch-session",
		principal:            identity.Principal{Type: identity.PrincipalTypeUser, UserID: "owner", AccountScopeID: "account"},
		sessionMode:          "auto",
		runID:                "run-create-guard",
		providerManagedV3:    true,
		applySessionMutation: sessionsSvc.ApplySessionMutation,
		agentProfile:         orchProfile,
		terminalPlanState:    &terminalPlanToolState{},
	})

	createArgs := fmt.Sprintf(`{
		"action": "create",
		"name": "Orchestrator Worker",
		"instructions": "Do something",
		"workspace_id": %q
	}`, workspaceID)

	res, err := svc.newProviderToolInvoker(invoker.(*providerToolInvoker).config).ExecuteTool(context.Background(), provideriface.ToolInvocation{
		Name:      "manage_workers",
		CallID:    "call-create",
		Arguments: createArgs,
	})
	if err != nil || res.Error != "" {
		t.Fatalf("create failed: %v / %s", err, res.Error)
	}

	var out map[string]any
	_ = json.Unmarshal([]byte(res.Output), &out)
	workerData, _ := out["worker"].(map[string]any)
	workerID := fmt.Sprint(workerData["id"])

	// The created worker MUST be in pending lifecycle state awaiting human acceptance
	if workerData["lifecycle_state"] != "pending" {
		t.Fatalf("expected pending lifecycle state, got %v", workerData["lifecycle_state"])
	}

	// Verify cannot run direct or test before acceptance
	_, err = execution.Dispatch(context.Background(), "account", "owner", store.WorkerRunAdmission{
		WorkerID:      workerID,
		RequestSource: "direct",
		Input:         map[string]any{"task": "Run"},
	})
	if err == nil {
		t.Fatal("expected direct dispatch to be rejected pre-acceptance")
	}

	// Human accepts -> now ready
	accepted, err := execution.Accept("account", "owner", workerID, 1)
	if err != nil {
		t.Fatalf("human accept failed: %v", err)
	}
	if accepted.LifecycleState != store.WorkerLifecycleStateActive {
		t.Fatalf("expected active state after accept, got %s", accepted.LifecycleState)
	}
}

func TestWorkerPendingProposal_InterceptionGateToolCalls(t *testing.T) {
	svc, sessionsSvc, _, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	orchProfile := agent.SwarmOrchestratorAgentProfileForContext(store.AgentProfile{})
	chatProfile := agent.SwarmAgentProfileForContext(store.AgentProfile{})

	// 1. Orchestrator proposal intercepted at gateToolCalls level succeeds and creates pending worker
	call := tool.Call{
		CallID:    "call-gate-prop",
		Name:      "manage_workers",
		Arguments: fmt.Sprintf(`{"action":"propose","name":"Intercepted Worker","instructions":"Gate check","workspace_id":%q}`, workspaceID),
	}
	results, approvedCalls, _, approvedMask, _, err := svc.gateToolCalls(
		context.Background(),
		"orch-session",
		"run-gate-test",
		1,
		"auto",
		[]tool.Call{call},
		nil,
		nil,
		orchProfile,
	)
	if err != nil {
		t.Fatalf("gateToolCalls error: %v", err)
	}
	if len(approvedCalls) != 0 || approvedMask[0] {
		t.Fatal("expected proposal call to NOT be approved for immediate execution")
	}
	if results[0].Error != "" {
		t.Fatalf("expected no error in result, got: %s", results[0].Error)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(results[0].Output), &out); err != nil {
		t.Fatalf("unmarshal gate result: %v", err)
	}
	if out["status"] != "pending_review" || out["next_action"] != "await_worker_acceptance" {
		t.Fatalf("unexpected gate output payload: %v", out)
	}
	workerID := fmt.Sprint(out["worker_id"])
	w, found, err := sessionsSvc.GetWorker("account", workerID)
	if err != nil || !found {
		t.Fatalf("worker not found in store: %v, found=%v", err, found)
	}
	if w.LifecycleState != store.WorkerLifecycleStatePending {
		t.Fatalf("expected pending lifecycle state, got %s", w.LifecycleState)
	}

	// 2. Chat session (non-orchestrator) intercepted at gateToolCalls is rejected with NO state change
	chatCall := tool.Call{
		CallID:    "call-gate-chat",
		Name:      "manage_workers",
		Arguments: fmt.Sprintf(`{"action":"propose","name":"Chat Forgery","instructions":"Forbidden","workspace_id":%q}`, workspaceID),
	}
	resultsChat, approvedChat, _, _, _, errChat := svc.gateToolCalls(
		context.Background(),
		"chat-session",
		"run-gate-chat",
		1,
		"auto",
		[]tool.Call{chatCall},
		nil,
		nil,
		chatProfile,
	)
	if errChat != nil {
		t.Fatalf("unexpected gateToolCalls hard error: %v", errChat)
	}
	if len(approvedChat) != 0 {
		t.Fatal("expected no approved calls for chat session")
	}
	if !strings.Contains(resultsChat[0].Error, "exclusive to Swarm Orchestrator") {
		t.Fatalf("expected orchestrator exclusivity error, got: %s", resultsChat[0].Error)
	}

	// 3. Orchestrator session called with explicit chat profile is rejected
	resultsProf, _, _, _, _, _ := svc.gateToolCalls(
		context.Background(),
		"orch-session",
		"run-gate-prof",
		1,
		"auto",
		[]tool.Call{chatCall},
		nil,
		nil,
		chatProfile,
	)
	if !strings.Contains(resultsProf[0].Error, "exclusive to Swarm Orchestrator") {
		t.Fatalf("expected profile rejection, got: %s", resultsProf[0].Error)
	}
}

func TestWorkerPendingProposal_LegacyDocumentValidationAndNormalization(t *testing.T) {
	svc, sessionsSvc, _, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	orchProfile := agent.SwarmOrchestratorAgentProfileForContext(store.AgentProfile{})

	proposeDoc := func(doc map[string]any) (map[string]any, string, error) {
		rawDoc, _ := json.Marshal(doc)
		args := fmt.Sprintf(`{"action":"propose","document":%s}`, string(rawDoc))
		invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{
			sessionID:            "orch-session",
			principal:            identity.Principal{Type: identity.PrincipalTypeUser, UserID: "owner", AccountScopeID: "account"},
			sessionMode:          "auto",
			runID:                "run-doc-norm",
			providerManagedV3:    true,
			applySessionMutation: sessionsSvc.ApplySessionMutation,
			agentProfile:         orchProfile,
			terminalPlanState:    &terminalPlanToolState{},
		})
		res, err := svc.newProviderToolInvoker(invoker.(*providerToolInvoker).config).ExecuteTool(context.Background(), provideriface.ToolInvocation{
			Name:      "manage_workers",
			CallID:    fmt.Sprintf("call-doc-test-%x", sha256.Sum256([]byte(args))),
			Arguments: args,
		})
		if err != nil {
			return nil, "", err
		}
		if res.Error != "" {
			return nil, res.Error, nil
		}
		var out map[string]any
		_ = json.Unmarshal([]byte(res.Output), &out)
		return out, "", nil
	}

	baseDoc := func() map[string]any {
		return map[string]any{
			"title": "Normalized Worker",
			"info":  map[string]any{"goal": "Check invariants"},
			"worker_v2": map[string]any{
				"schema_version": 2,
				"workspace_id":   workspaceID,
				"schedule":       map[string]any{"kind": "interval", "interval_seconds": 3600},
				"missed":         "skip",
				"overlap":        "serialize",
				"expiration":     map[string]any{"kind": "indefinite"},
			},
			"checkpoints": []map[string]any{
				{
					"id":                  "cp-1",
					"order":               1,
					"status":              "pending",
					"title":               "Run check",
					"tasks":               []string{"Verify status"},
					"acceptance_criteria": []string{"Verified"},
				},
			},
		}
	}

	// 1. Expiration "indefinite" succeeds and normalizes planCopy (WorkerV2 and AutomationV2 stripped)
	out, errStr, err := proposeDoc(baseDoc())
	if err != nil || errStr != "" {
		t.Fatalf("indefinite expiration failed: %v / %s", err, errStr)
	}
	workerID := fmt.Sprint(out["worker_id"])
	w, found, err := sessionsSvc.GetWorker("account", workerID)
	if err != nil || !found {
		t.Fatalf("worker not found: %v", err)
	}
	if len(w.Automations) != 1 {
		t.Fatalf("expected 1 automation, got %d", len(w.Automations))
	}
	// Attached PlanDocument MUST have WorkerV2 and AutomationV2 stripped
	if w.Automations[0].PlanDocument.WorkerV2 != nil || w.Automations[0].PlanDocument.AutomationV2 != nil {
		t.Fatal("attached plan document retained nested worker_v2 or automation_v2 settings")
	}

	// 2. Expiration "never" succeeds
	neverDoc := baseDoc()
	neverDoc["worker_v2"].(map[string]any)["expiration"] = map[string]any{"kind": "never"}
	outNever, errNever, _ := proposeDoc(neverDoc)
	if errNever != "" {
		t.Fatalf("never expiration failed: %s", errNever)
	}
	if outNever["status"] != "pending_review" {
		t.Fatalf("expected pending_review, got %v", outNever["status"])
	}

	// 3. Finite expiration "at" is rejected with clear error
	finiteDoc := baseDoc()
	finiteDoc["worker_v2"].(map[string]any)["expiration"] = map[string]any{"kind": "at", "expires_at": 9999999999}
	_, errFinite, _ := proposeDoc(finiteDoc)
	if !strings.Contains(errFinite, "unrepresentable in durable workers") {
		t.Fatalf("expected unrepresentable expiration error, got: %s", errFinite)
	}

	// 4. Multiple workspace_ids rejected
	multiWsDoc := baseDoc()
	multiWsDoc["worker_v2"].(map[string]any)["workspace_ids"] = []string{workspaceID, "ws_other"}
	delete(multiWsDoc["worker_v2"].(map[string]any), "workspace_id")
	_, errMultiWs, _ := proposeDoc(multiWsDoc)
	if !strings.Contains(errMultiWs, "multiple workspace_ids in worker_v2 is unsupported") {
		t.Fatalf("expected multiple workspace_ids rejection, got: %s", errMultiWs)
	}

	// 5. Unsupported missed policy rejected
	coalesceDoc := baseDoc()
	coalesceDoc["worker_v2"].(map[string]any)["missed"] = "coalesce"
	_, errCoalesce, _ := proposeDoc(coalesceDoc)
	if !strings.Contains(errCoalesce, "unsupported missed policy") {
		t.Fatalf("expected unsupported missed policy error, got: %s", errCoalesce)
	}

	// 6. Unsupported overlap policy rejected
	indepDoc := baseDoc()
	indepDoc["worker_v2"].(map[string]any)["overlap"] = "independent"
	_, errIndep, _ := proposeDoc(indepDoc)
	if !strings.Contains(errIndep, "unsupported overlap policy") {
		t.Fatalf("expected unsupported overlap policy error, got: %s", errIndep)
	}

	// 7. Unsupported activate_on_accept=false rejected
	noActDoc := baseDoc()
	noActDoc["worker_v2"].(map[string]any)["activate_on_accept"] = false
	_, errNoAct, _ := proposeDoc(noActDoc)
	if !strings.Contains(errNoAct, "unsupported activate_on_accept=false") {
		t.Fatalf("expected unsupported activate_on_accept=false error, got: %s", errNoAct)
	}

	// 8. Schedule without checkpoints rejected
	noCheckpointsDoc := baseDoc()
	noCheckpointsDoc["checkpoints"] = []any{}
	_, errNoCheckpoints, _ := proposeDoc(noCheckpointsDoc)
	if !strings.Contains(errNoCheckpoints, "requires checkpoints") {
		t.Fatalf("expected schedule requires checkpoints error, got: %s", errNoCheckpoints)
	}
}

func TestWorkerPendingProposal_ReproposalRequiresExpectedRevisionAndCanRemoveAutomations(t *testing.T) {
	svc, sessionsSvc, _, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	orchProfile := agent.SwarmOrchestratorAgentProfileForContext(store.AgentProfile{})

	invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{
		sessionID:            "orch-session",
		principal:            identity.Principal{Type: identity.PrincipalTypeUser, UserID: "owner", AccountScopeID: "account"},
		sessionMode:          "auto",
		runID:                "run-reprop-advanced",
		providerManagedV3:    true,
		applySessionMutation: sessionsSvc.ApplySessionMutation,
		agentProfile:         orchProfile,
		terminalPlanState:    &terminalPlanToolState{},
	})

	// 1. Create worker with 1 automation
	initArgs := fmt.Sprintf(`{
		"action": "propose",
		"name": "Automation Holder",
		"instructions": "Run periodic tasks",
		"workspace_id": %q,
		"automations": [
			{
				"name": "Periodic Check",
				"activation_mode": "manual",
				"enabled": true,
				"plan_document": {
					"title": "Check",
					"info": {"goal": "Check"},
					"checkpoints": [{"id": "c1", "title": "Check", "status": "pending", "order": 1, "tasks": ["T1"], "acceptance_criteria": ["A1"]}]
				}
			}
		]
	}`, workspaceID)

	resInit, err := svc.newProviderToolInvoker(invoker.(*providerToolInvoker).config).ExecuteTool(context.Background(), provideriface.ToolInvocation{
		Name:      "manage_workers",
		CallID:    "call-init-auto",
		Arguments: initArgs,
	})
	if err != nil || resInit.Error != "" {
		t.Fatalf("init proposal failed: %v / %s", err, resInit.Error)
	}
	var outInit map[string]any
	_ = json.Unmarshal([]byte(resInit.Output), &outInit)
	workerID := fmt.Sprint(outInit["worker_id"])

	wInit, found, err := sessionsSvc.GetWorker("account", workerID)
	if err != nil || !found {
		t.Fatalf("worker not found: %v", err)
	}
	if len(wInit.Automations) != 1 {
		t.Fatalf("expected 1 automation, got %d", len(wInit.Automations))
	}

	// 2. Reproposal without expected_revision must be rejected
	missingRevArgs := fmt.Sprintf(`{
		"action": "propose",
		"worker_id": %q,
		"name": "Missing Revision Rename"
	}`, workerID)
	resMissingRev, err := svc.newProviderToolInvoker(invoker.(*providerToolInvoker).config).ExecuteTool(context.Background(), provideriface.ToolInvocation{
		Name:      "manage_workers",
		CallID:    "call-missing-rev",
		Arguments: missingRevArgs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resMissingRev.Error, "expected_revision is required") {
		t.Fatalf("expected required expected_revision error, got: %s", resMissingRev.Error)
	}

	// Verify worker state is unmutated
	wUnmutated, _, _ := sessionsSvc.GetWorker("account", workerID)
	if wUnmutated.Revision != 1 || wUnmutated.Name != "Automation Holder" || len(wUnmutated.Automations) != 1 {
		t.Fatalf("worker state mutated after failed reproposal: %+v", wUnmutated)
	}

	// 3. Reproposal with empty automations removes all jobs
	clearAutosArgs := fmt.Sprintf(`{
		"action": "propose",
		"worker_id": %q,
		"expected_revision": 1,
		"name": "Automation Free Specialist",
		"instructions": "Now a job-free specialist",
		"automations": []
	}`, workerID)
	resClear, err := svc.newProviderToolInvoker(invoker.(*providerToolInvoker).config).ExecuteTool(context.Background(), provideriface.ToolInvocation{
		Name:      "manage_workers",
		CallID:    "call-clear-autos",
		Arguments: clearAutosArgs,
	})
	if err != nil || resClear.Error != "" {
		t.Fatalf("clear automations reproposal failed: %v / %s", err, resClear.Error)
	}

	wCleared, found, err := sessionsSvc.GetWorker("account", workerID)
	if err != nil || !found {
		t.Fatalf("worker not found: %v", err)
	}
	if wCleared.Revision != 2 {
		t.Fatalf("expected revision 2, got %d", wCleared.Revision)
	}
	if len(wCleared.Automations) != 0 {
		t.Fatalf("expected 0 automations after explicit empty automations, got %d", len(wCleared.Automations))
	}
	if wCleared.Name != "Automation Free Specialist" {
		t.Fatalf("expected updated name, got %s", wCleared.Name)
	}
}

func TestWorkerPendingProposal_TargetWorkspaceRequiredAndAuthorized(t *testing.T) {
	svc, sessionsSvc, _, _ := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })

	// 1. Session with no workspace grants attempting proposal without workspace_id
	noWsSession := store.SessionSnapshot{
		ID:             "no-ws-test-session",
		AccountScopeID: "account",
		UserID:         "owner",
		Mode:           "auto",
		Metadata: map[string]any{
			"agent_name":          "system-orchestrator",
			"resolved_agent_name": "system-orchestrator",
		},
	}
	if err := sessionsSvc.Store().CreateSession(noWsSession); err != nil {
		t.Fatal(err)
	}

	callNoWs := tool.Call{
		Name:      "manage_workers",
		Arguments: `{"action":"propose","name":"Unbound Worker","instructions":"No workspace"}`,
	}
	_, errNoWs := svc.executeWorkerProposalTool("no-ws-test-session", callNoWs)
	if errNoWs == nil || !strings.Contains(errNoWs.Error(), "target workspace required") {
		t.Fatalf("expected target workspace required error, got: %v", errNoWs)
	}

	// 2. Proposal with unauthorized/non-existent workspace ID
	callInvalidWs := tool.Call{
		Name:      "manage_workers",
		Arguments: `{"action":"propose","name":"Invalid Workspace Worker","instructions":"Do work","workspace_id":"ws_does_not_exist_12345"}`,
	}
	_, errInvalidWs := svc.executeWorkerProposalTool("no-ws-test-session", callInvalidWs)
	if errInvalidWs == nil || !strings.Contains(errInvalidWs.Error(), "not found or not accessible") {
		t.Fatalf("expected workspace not accessible error, got: %v", errInvalidWs)
	}
}

func TestWorkerPendingProposal_AcceptBindingIntegrityUnderMutex(t *testing.T) {
	_, sessionsSvc, _, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })

	w, err := sessionsSvc.CreateWorker(context.Background(), "account", "owner", store.CreateWorkerRequest{
		Name:                  "Binding Guard Worker",
		Instructions:          "Strict bindings",
		InitialLifecycleState: store.WorkerLifecycleStatePending,
		ProposedBindings:      map[string]string{"primary": workspaceID},
		WorkspaceRequirements: []store.WorkerWorkspaceRequirement{
			{Role: "primary", Description: "Primary workspace", Required: true},
		},
		IdempotencyKey: "test-binding-integrity",
	})
	if err != nil {
		t.Fatalf("create worker failed: %v", err)
	}

	// Direct call to ws.AcceptWorker with altered bindings must fail under mutex
	ws := sessionsSvc.Store().WorkerStore()
	_, err = ws.AcceptWorker("account", "owner", w.ID, 1, map[string]string{"primary": "ws_forged_different"})
	if err == nil || !errors.Is(err, store.ErrWorkerConflict) || !strings.Contains(err.Error(), "accepted bindings must match exact proposed bindings") {
		t.Fatalf("expected ErrWorkerConflict for altered bindings, got: %v", err)
	}

	// Verify worker state is unchanged
	loaded, found, err := sessionsSvc.GetWorker("account", w.ID)
	if err != nil || !found {
		t.Fatalf("get worker: %v", err)
	}
	if loaded.LifecycleState != store.WorkerLifecycleStatePending || loaded.Revision != 1 || len(loaded.LocalBindings) != 0 {
		t.Fatalf("worker state mutated after failed accept: %+v", loaded)
	}
}
