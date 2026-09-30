package run

import (
	"encoding/json"
	"reflect"
	"testing"

	"swarm/packages/swarmd/internal/agent"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: accepted worker execution defaults to the account action selection,
// with explicit Plan retaining its independent selection. Threat: automatic
// plan installation accidentally pins the planning model for all execution.
// The preference helper is the narrowest selection layer; integration of plan
// acceptance and dispatch remains parent-owned focused regression validation.
func TestWorkerExecutionPreferenceDefaultsToAction(t *testing.T) {
	action := store.ModelProfileSelection{Provider: "fixture", Model: "action", Thinking: "high"}
	plan := store.ModelProfileSelection{Provider: "fixture", Model: "plan", Thinking: "low"}
	w := store.WorkerRecord{ModelProfile: &store.SessionModelProfileSnapshot{Action: action, Plan: &plan}}
	for _, mode := range []string{"", "auto", "plan"} {
		w.ExecutionMode = mode
		got := workerExecutionPreference(w)
		want := action
		if mode == "plan" { want = plan }
		if got.Provider != want.Provider || got.Model != want.Model || got.Thinking != want.Thinking { t.Fatalf("%q selected wrong profile: %+v", mode, got) }
	}
	if !reflect.DeepEqual(w.ModelProfile.Action, action) || !reflect.DeepEqual(*w.ModelProfile.Plan, plan) { t.Fatal("selection mutated pinned profile") }
}

// Requirement: Orchestrator propose(worker_id) adds jobs to the same active
// worker through pending review, rather than a new worker/session plan. Threat:
// ingress rejects active state or replaces reusable worker instructions. The
// actual tool handler with isolated canonical stores is the narrowest ingress
// layer; no dispatch or scheduler is invoked.
func TestWorkerActiveProposalToolStagesJobs(t *testing.T) {
	svc, ss, _, workspaceID := setupWorkerExecutionFixture(t, nil)
	profile, err := svc.ResolveWorkerModelProfile("account", nil)
	if err != nil { t.Fatal(err) }
	ws := ss.Store().WorkerStore()
	w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{Name: "Reusable", Instructions: "Preserve specialist instructions", ModelProfile: profile, InitialLifecycleState: store.WorkerLifecycleStatePending, ProposedBindings: map[string]string{"primary": workspaceID}, WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
	if err != nil { t.Fatal(err) }
	w, err = ws.AcceptWorker("account", "owner", w.ID, w.Revision, w.ProposedBindings, profile)
	if err != nil { t.Fatal(err) }
	args := map[string]any{"worker_id": w.ID, "expected_revision": w.Revision, "automations": []store.WorkerAutomationDefinition{{Name: "hello", ActivationMode: "interval", Schedule: &store.AutomationV2Schedule{Kind: "interval", IntervalSeconds: 300}, Enabled: true, PlanDocument: store.SessionPlanDocument{Title: "Hello", Info: store.SessionPlanInfo{Goal: "Say hello"}, Checkpoints: []store.SessionPlanCheckpoint{{ID: "cp-1", Order: 1, Title: "Hello", Tasks: []string{"Say hello"}, AcceptanceCriteria: []string{"Hello returned"}}}}}}}
	out, err := svc.executeCreateOrProposePendingWorker("orch-session", args, "manage_workers", false, agent.SwarmOrchestratorAgentProfileForContext(store.AgentProfile{}))
	if err != nil { t.Fatal(err) }
	var result struct { Status string `json:"status"`; Worker store.WorkerRecord `json:"worker"` }
	if err := json.Unmarshal([]byte(out), &result); err != nil { t.Fatal(err) }
	if result.Status != "pending_review" || result.Worker.ID != w.ID || result.Worker.Instructions != w.Instructions || result.Worker.LifecycleState != store.WorkerLifecycleStateActive || len(result.Worker.Automations) != 0 || result.Worker.PendingReview == nil || len(result.Worker.PendingReview.Automations) != 1 { t.Fatalf("active tool proposal: %+v", result) }
	runs, _, err := ws.ListWorkerRuns("account", w.ID, 20, "")
	if err != nil || len(runs) != 0 { t.Fatal("proposal dispatched work") }
}
