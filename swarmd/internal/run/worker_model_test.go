package run

import (
	"context"
	"reflect"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: future jobs follow defaults unless overridden, while admitted
// snapshots remain immutable. Threat: account edits replace explicit choices,
// or foreign/stale review and retries rewrite approved jobs.
// The shared execution service with real temporary Git/Pebble/V3 preparation is
// the narrowest layer proving model and task/session identity before enqueue.
func TestWorkerModelDefaultOverrideAndPinnedJobs(t *testing.T) {
	runs, ss, execution, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	initial, err := runs.ResolveWorkerModelProfile("account", nil)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := runs.agentModelSettings.GetForAccount("account")
	if err != nil {
		t.Fatal(err)
	}
	if !initial.UseAccountDefault || initial.Action.Model != settings.Swarm.Action.Model || initial.Plan == nil || initial.Plan.Model != settings.Swarm.Plan.Model {
		t.Fatalf("default not captured: %+v", initial)
	}
	ws := ss.Store().WorkerStore()
	create := func(name string) store.WorkerRecord {
		w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{Name: name, Instructions: "Review only", ModelProfile: initial, InitialLifecycleState: store.WorkerLifecycleStatePending, ProposedBindings: map[string]string{"primary": workspaceID}, WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	one, two := create("First worker"), create("Second worker")
	override := store.CloneSessionModelProfileSnapshot(initial)
	override.UseAccountDefault = false
	// Select a different catalog-owned provider rather than invent a model list.
	_, _, catalog, found, err := runs.model.RecommendedCatalogDefaults("google")
	if err != nil || !found {
		t.Fatalf("second catalog model: %v", err)
	}
	override.Action = store.ModelProfileSelection{Provider: "google", Model: catalog.Model, Thinking: catalog.DefaultThinking}
	override.Plan = &override.Action
	override, err = runs.ResolveWorkerModelProfile("account", override)
	if err != nil {
		t.Fatal(err)
	}
	invalid := store.CloneSessionModelProfileSnapshot(initial)
	invalid.UseAccountDefault = false
	invalid.Action.Model = "missing-catalog-model"
	if _, err = execution.Accept("account", "owner", one.ID, one.Revision, invalid); err == nil {
		t.Fatal("invalid selection accepted")
	}
	if _, err = execution.Accept("foreign", "owner", one.ID, one.Revision, override); err == nil {
		t.Fatal("foreign acceptance succeeded")
	}
	if _, err = execution.Accept("account", "owner", one.ID, one.Revision+1, override); err == nil {
		t.Fatal("stale acceptance succeeded")
	}
	unchanged, _, _ := ws.GetWorker("account", one.ID)
	if unchanged.Revision != one.Revision || unchanged.LifecycleState != store.WorkerLifecycleStatePending {
		t.Fatalf("rejection mutated: %+v", unchanged)
	}
	one, err = execution.Accept("account", "owner", one.ID, one.Revision, override)
	if err != nil {
		t.Fatal(err)
	}
	two, err = execution.Accept("account", "owner", two.ID, two.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(one.ModelProfile.Action, override.Action) || !reflect.DeepEqual(two.ModelProfile.Action, initial.Action) {
		t.Fatal("workers lost independent choices")
	}
	before, err := execution.Dispatch(ctx, "account", "owner", store.WorkerRunAdmission{WorkerID: two.ID, RequestSource: "direct", Input: map[string]any{"prompt": "Review"}, IdempotencyKey: "before-default-change"})
	if err != nil {
		t.Fatal(err)
	}
	// Change account defaults directly in this isolated fixture; product code must not.
	record, _, err := store.NewAgentModelSettingsStore(ss.Store().Underlying()).GetForAccount("account")
	if err != nil {
		t.Fatal(err)
	}
	record.Swarm.Action = store.AgentModelAssignment{Provider: override.Action.Provider, Model: override.Action.Model, Thinking: override.Action.Thinking}
	record.Swarm.Plan = record.Swarm.Action
	if _, err = store.NewAgentModelSettingsStore(ss.Store().Underlying()).PutForAccount(record); err != nil {
		t.Fatal(err)
	}
	for i, w := range []store.WorkerRecord{one, two, two} {
		r, err := execution.Dispatch(ctx, "account", "owner", store.WorkerRunAdmission{WorkerID: w.ID, RequestSource: "direct", Input: map[string]any{"prompt": "Review"}, IdempotencyKey: []string{"first", "second", "subsequent"}[i]})
		if err != nil {
			t.Fatal(err)
		}
		snapshot, found, err := ss.GetSession(r.SessionID)
		if err != nil || !found || snapshot.ModelProfile == nil || snapshot.ModelProfile.UseAccountDefault || snapshot.ModelProfile.Action != override.Action || snapshot.Title != w.Name || snapshot.Preference.Model != override.Action.Model {
			t.Fatalf("job did not pin worker choice/title: %+v %v", snapshot, err)
		}
		task, found, err := ss.Store().GetProjectTask("account", "proj_fixture", "task_"+r.ID)
		if err != nil || !found || task.Title != w.Name || task.Agent != "swarm" {
			t.Fatalf("presentation changed runtime identity: %+v %v", task, err)
		}
	}
	prior, found, err := ss.GetSession(before.SessionID)
	if err != nil || !found || prior.ModelProfile.Action != initial.Action {
		t.Fatalf("admitted snapshot changed: %+v %v", prior, err)
	}
	record.Swarm.Action = store.AgentModelAssignment{Provider: initial.Action.Provider, Model: initial.Action.Model, Thinking: initial.Action.Thinking, ServiceTier: initial.Action.ServiceTier, ContextMode: initial.Action.ContextMode}
	record.Swarm.Plan = record.Swarm.Action
	if _, err = store.NewAgentModelSettingsStore(ss.Store().Underlying()).PutForAccount(record); err != nil {
		t.Fatal(err)
	}
	pinned, err := runs.ResolveWorkerModelProfile("account", one.ModelProfile)
	if err != nil || pinned.Action != override.Action {
		t.Fatalf("account changes replaced explicit override: %+v %v", pinned, err)
	}
	partial := store.CloneSessionModelProfileSnapshot(one.ModelProfile)
	partial.ActionUseAccountDefault = true
	mixed, err := runs.ResolveWorkerModelProfile("account", partial)
	if err != nil || mixed.Action != initial.Action || *mixed.Plan != override.Action || !mixed.ActionUseAccountDefault || mixed.PlanUseAccountDefault {
		t.Fatalf("slot reset replaced independent override: %+v %v", mixed, err)
	}
	reset := store.CloneSessionModelProfileSnapshot(one.ModelProfile)
	reset.UseAccountDefault = true
	resolved, err := runs.ResolveWorkerModelProfile("account", reset)
	if err != nil || resolved.Action != initial.Action || !resolved.UseAccountDefault {
		t.Fatalf("reset did not follow defaults: %+v %v", resolved, err)
	}
}

// Requirement: unavailable initial settings use only the resolved account
// default and disclose the fallback; an explicit invalid review never falls
// back. ResolveWorkerModelProfile is the narrowest catalog/settings boundary.
func TestWorkerModelDefaultFallbackIsVisibleAndExplicitInvalidDoesNotFallback(t *testing.T) {
	runs, ss, _, _ := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	initial, err := runs.ResolveWorkerModelProfile("account", nil)
	if err != nil {
		t.Fatal(err)
	}
	models := store.NewModelStore(ss.Store().Underlying())
	if _, err = models.SetPreferenceForAccount("account", "owner", initial.Action.Provider, initial.Action.Model, initial.Action.Thinking, initial.Action.ServiceTier, initial.Action.ContextMode); err != nil {
		t.Fatal(err)
	}
	settings := store.NewAgentModelSettingsStore(ss.Store().Underlying())
	record, _, err := settings.GetForAccount("account")
	if err != nil {
		t.Fatal(err)
	}
	record.Swarm.Action.Model = "unavailable-model"
	if _, err = settings.PutForAccount(record); err != nil {
		t.Fatal(err)
	}
	fallback, err := runs.ResolveWorkerModelProfile("account", nil)
	if err != nil || fallback.ResolutionWarning == "" || fallback.Action != initial.Action || fallback.Plan == nil || *fallback.Plan != initial.Action {
		t.Fatalf("missing visible account fallback: %+v %v", fallback, err)
	}
	explicit := store.CloneSessionModelProfileSnapshot(initial)
	explicit.UseAccountDefault = false
	explicit.Action.Model = "unavailable-model"
	if _, err = runs.ResolveWorkerModelProfile("account", explicit); err == nil {
		t.Fatal("explicit invalid selection silently fell back")
	}
}

// Requirement: saved model proposals reach future V3 session snapshots only
// after human acceptance, without any acceptance-time client model override.
// Threat: pending policies apply early or account defaults replace the candidate.
// Real WorkerExecutionService preparation with isolated Git/Pebble and enqueue
// receipts is the narrowest session-boundary proof; no provider is invoked.
// ResolveWorkerModelProfile must accept picker standard tiers, persist their
// canonical policy, and leave account settings and pre-review snapshots intact.
func TestWorkerModelSavedUpdateReachesFutureSession(t *testing.T) {
	runs, ss, execution, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	initial, err := runs.ResolveWorkerModelProfile("account", nil)
	if err != nil {
		t.Fatal(err)
	}
	ws := ss.Store().WorkerStore()
	w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{Name: "Review", Instructions: "Review", ModelProfile: initial, InitialLifecycleState: store.WorkerLifecycleStatePending, ProposedBindings: map[string]string{"primary": workspaceID}, WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = execution.Accept("account", "owner", w.ID, w.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, _, catalog, found, err := runs.model.RecommendedCatalogDefaults("google")
	if err != nil || !found {
		t.Fatalf("catalog fixture: %v", err)
	}
	candidate := store.CloneSessionModelProfileSnapshot(initial)
	candidate.UseAccountDefault = false
	candidate.PlanUseAccountDefault = true
	candidate.Action = store.ModelProfileSelection{Provider: "google", Model: catalog.Model, Thinking: catalog.DefaultThinking, ServiceTier: "standard"}
	picker := store.CloneSessionModelProfileSnapshot(candidate)
	settingsBefore, err := runs.agentModelSettings.GetForAccount("account")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err = runs.ResolveWorkerModelProfile("account", candidate)
	if err != nil {
		t.Fatal(err)
	}
	if picker.Action.ServiceTier != "standard" || candidate.Action.ServiceTier != "" || candidate.Action.Provider != picker.Action.Provider || candidate.Action.Model != picker.Action.Model || candidate.Action.Thinking != picker.Action.Thinking || !candidate.PlanUseAccountDefault {
		t.Fatal("resolution did not preserve picker policy with canonical standard tier")
	}
	staged, err := ws.UpdateWorker("account", "owner", w.ID, w.Revision, store.UpdateWorkerRequest{ModelProfile: candidate}, nil)
	if err != nil {
		t.Fatal(err)
	}
	before, err := execution.Dispatch(ctx, "account", "owner", store.WorkerRunAdmission{WorkerID: w.ID, RequestSource: "direct", Input: map[string]any{"prompt": "Review"}, IdempotencyKey: "pending-model"})
	if err != nil {
		t.Fatal(err)
	}
	approved, err := execution.Accept("account", "owner", w.ID, staged.Revision)
	if err != nil || !reflect.DeepEqual(approved.ModelProfile, candidate) {
		t.Fatalf("acceptance lost saved candidate: %+v %v", approved, err)
	}
	after, err := execution.Dispatch(ctx, "account", "owner", store.WorkerRunAdmission{WorkerID: w.ID, RequestSource: "direct", Input: map[string]any{"prompt": "Review"}, IdempotencyKey: "accepted-model"})
	if err != nil {
		t.Fatal(err)
	}
	subsequent, err := execution.Dispatch(ctx, "account", "owner", store.WorkerRunAdmission{WorkerID: w.ID, RequestSource: "direct", Input: map[string]any{"prompt": "Review"}, IdempotencyKey: "subsequent-accepted-model"})
	if err != nil {
		t.Fatal(err)
	}
	saved, found, err := ws.GetWorker("account", w.ID)
	if err != nil || !found || saved.PendingReview != nil || !reflect.DeepEqual(saved.ModelProfile, candidate) {
		t.Fatalf("future runs changed approved policy: %+v %v", saved, err)
	}
	settingsAfter, err := runs.agentModelSettings.GetForAccount("account")
	if err != nil || !reflect.DeepEqual(settingsBefore, settingsAfter) {
		t.Fatal("worker override changed account defaults")
	}
	for _, check := range []struct {
		run       store.WorkerRunRecord
		selection store.ModelProfileSelection
	}{{before, initial.Action}, {after, candidate.Action}, {subsequent, candidate.Action}} {
		snapshot, found, err := ss.GetSession(check.run.SessionID)
		if err != nil || !found || snapshot.ModelProfile == nil || snapshot.ModelProfile.Action != check.selection || snapshot.Preference.Provider != check.selection.Provider || snapshot.Preference.Model != check.selection.Model || snapshot.Preference.Thinking != check.selection.Thinking || snapshot.Preference.ServiceTier != check.selection.ServiceTier || snapshot.Preference.ContextMode != check.selection.ContextMode {
			t.Fatalf("session tuple changed: %+v %v", snapshot, err)
		}
		if snapshot.ModelProfile.UseAccountDefault || snapshot.ModelProfile.ActionUseAccountDefault || snapshot.ModelProfile.PlanUseAccountDefault || *snapshot.ModelProfile.Plan != *initial.Plan {
			t.Fatal("session contains mutable or changed plan policy")
		}
	}
}
