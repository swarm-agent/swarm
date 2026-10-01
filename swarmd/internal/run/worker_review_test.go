package run

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
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
		if mode == "plan" {
			want = plan
		}
		if got.Provider != want.Provider || got.Model != want.Model || got.Thinking != want.Thinking {
			t.Fatalf("%q selected wrong profile: %+v", mode, got)
		}
	}
	if !reflect.DeepEqual(w.ModelProfile.Action, action) || !reflect.DeepEqual(*w.ModelProfile.Plan, plan) {
		t.Fatal("selection mutated pinned profile")
	}
}

// Requirement: Orchestrator propose(worker_id) adds jobs to the same active
// worker through pending review, rather than a new worker/session plan. Threat:
// ingress rejects active state or replaces reusable worker instructions. The
// actual tool handler with isolated canonical stores is the narrowest ingress
// layer; no dispatch or scheduler is invoked.
func TestWorkerActiveProposalToolStagesJobs(t *testing.T) {
	svc, ss, _, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool {
		t.Fatal("proposal unexpectedly enqueued execution")
		return false
	})
	profile, err := svc.ResolveWorkerModelProfile("account", nil)
	if err != nil {
		t.Fatal(err)
	}
	ws := ss.Store().WorkerStore()
	w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{Name: "Reusable", Instructions: "Preserve specialist instructions", ModelProfile: profile, InitialLifecycleState: store.WorkerLifecycleStatePending, ProposedBindings: map[string]string{"primary": workspaceID}, WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = ws.AcceptWorker("account", "owner", w.ID, w.Revision, w.ProposedBindings, profile)
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"worker_id": w.ID, "expected_revision": w.Revision, "automations": []store.WorkerAutomationDefinition{{Name: "hello", ActivationMode: "interval", Schedule: &store.AutomationV2Schedule{Kind: "interval", IntervalSeconds: 300}, Enabled: true, PlanDocument: store.SessionPlanDocument{Title: "Hello", Info: store.SessionPlanInfo{Goal: "Say hello"}, Checkpoints: []store.SessionPlanCheckpoint{{ID: "cp-1", Order: 1, Title: "Hello", Tasks: []string{"Say hello"}, AcceptanceCriteria: []string{"Hello returned"}}}}}}}
	out, err := svc.executeCreateOrProposePendingWorker("orch-session", args, "manage_workers", false, agent.SwarmOrchestratorAgentProfileForContext(store.AgentProfile{}))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Status string             `json:"status"`
		Worker store.WorkerRecord `json:"worker"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "pending_review" || result.Worker.ID != w.ID || result.Worker.Instructions != w.Instructions || result.Worker.LifecycleState != store.WorkerLifecycleStateActive || len(result.Worker.Automations) != 0 || result.Worker.PendingReview == nil || len(result.Worker.PendingReview.Automations) != 1 {
		t.Fatalf("active tool proposal: %+v", result)
	}
	runs, _, err := ws.ListWorkerRuns("account", w.ID, 20, "")
	if err != nil || len(runs) != 0 {
		t.Fatal("proposal dispatched work")
	}
}

// Requirement: the reviewed execution role survives V3 session and plan acceptance,
// not just the preference helper. Threat: acceptance rewrites Action to Plan or
// later dispatch ignores reviewed settings. Temporary Git/Pebble preparation with
// counted enqueue is the narrowest boundary proving durable dispatch selections;
// no provider or actual executor runs.
func TestWorkerReviewedRolePersistsThroughDispatchPreparation(t *testing.T) {
	svc, ss, execution, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	profile, err := svc.ResolveWorkerModelProfile("account", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, catalog, found, err := svc.model.RecommendedCatalogDefaults("google")
	if err != nil || !found {
		t.Fatalf("catalog: %v", err)
	}
	profile.Plan = &store.ModelProfileSelection{Provider: "google", Model: catalog.Model, Thinking: catalog.DefaultThinking}
	profile, err = svc.ResolveWorkerModelProfile("account", profile)
	if err != nil {
		t.Fatal(err)
	}
	ws := ss.Store().WorkerStore()
	w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{Name: "Reviewed", Instructions: "Inspect only", ModelProfile: profile, InitialLifecycleState: store.WorkerLifecycleStatePending, ProposedBindings: map[string]string{"primary": workspaceID}, WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = execution.Accept("account", "owner", w.ID, w.Revision)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, mode := range []string{"auto", "plan"} {
		pending, err := ws.UpdateWorker("account", "owner", w.ID, w.Revision, store.UpdateWorkerRequest{ExecutionMode: &mode}, nil)
		if err != nil {
			t.Fatal(err)
		}
		w, err = execution.Accept("account", "owner", w.ID, pending.Revision)
		if err != nil {
			t.Fatal(err)
		}
		run, err := execution.Dispatch(ctx, "account", "owner", store.WorkerRunAdmission{WorkerID: w.ID, RequestSource: "direct", Input: map[string]any{"prompt": "Inspect"}, IdempotencyKey: "reviewed-role-" + mode})
		if err != nil {
			t.Fatal(err)
		}
		snapshot, found, err := ss.GetSession(run.SessionID)
		want := workerExecutionPreference(w)
		if err != nil || !found || snapshot.Preference != want || !reflect.DeepEqual(snapshot.ModelProfile, w.ModelProfile) || run.WorkerRevision != w.Revision {
			t.Fatalf("reviewed role lost: mode=%s snapshot=%+v run=%+v err=%v", mode, snapshot, run, err)
		}
		intent, found, err := ss.GetSessionRunIntent(run.SessionID, run.ID)
		if err != nil || !found || intent.PlanID == "" {
			t.Fatalf("canonical intent missing: %+v %v", intent, err)
		}
	}
}
