package run

import (
	"context"
	"reflect"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: workers capture the canonical account default once, accept an
// explicit review choice, and reuse it in future V3 jobs. Threat: changing the
// account default or a foreign/stale acceptance changes another worker or run.
// The shared execution service with real temporary Git/Pebble/V3 preparation is
// the narrowest layer proving model and task/session identity before enqueue.
func TestWorkerModelDefaultOverrideAndPinnedJobs(t *testing.T) {
	runs, ss, execution, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	initial, err := runs.ResolveWorkerModelProfile("account", nil)
	if err != nil { t.Fatal(err) }
	settings, err := runs.agentModelSettings.GetForAccount("account")
	if err != nil { t.Fatal(err) }
	if initial.UseAccountDefault || initial.Action.Model != settings.Swarm.Action.Model || initial.Plan == nil || initial.Plan.Model != settings.Swarm.Plan.Model { t.Fatalf("default not captured: %+v", initial) }
	ws := ss.Store().WorkerStore()
	create := func(name string) store.WorkerRecord {
		w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{Name: name, Instructions: "Review only", ModelProfile: initial, InitialLifecycleState: store.WorkerLifecycleStatePending, ProposedBindings: map[string]string{"primary": workspaceID}, WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
		if err != nil { t.Fatal(err) }
		return w
	}
	one, two := create("First worker"), create("Second worker")
	override := store.CloneSessionModelProfileSnapshot(initial)
	// Select a different catalog-owned provider rather than invent a model list.
	_, _, catalog, found, err := runs.model.RecommendedCatalogDefaults("google")
	if err != nil || !found { t.Fatalf("second catalog model: %v", err) }
	override.Action = store.ModelProfileSelection{Provider: "google", Model: catalog.Model, Thinking: catalog.DefaultThinking}
	override.Plan = &override.Action
	override, err = runs.ResolveWorkerModelProfile("account", override)
	if err != nil { t.Fatal(err) }
	invalid := store.CloneSessionModelProfileSnapshot(initial)
	invalid.Action.Model = "missing-catalog-model"
	if _, err = execution.Accept("account", "owner", one.ID, one.Revision, invalid); err == nil { t.Fatal("invalid selection accepted") }
	if _, err = execution.Accept("foreign", "owner", one.ID, one.Revision, override); err == nil { t.Fatal("foreign acceptance succeeded") }
	if _, err = execution.Accept("account", "owner", one.ID, one.Revision+1, override); err == nil { t.Fatal("stale acceptance succeeded") }
	unchanged, _, _ := ws.GetWorker("account", one.ID)
	if unchanged.Revision != one.Revision || unchanged.LifecycleState != store.WorkerLifecycleStatePending { t.Fatalf("rejection mutated: %+v", unchanged) }
	one, err = execution.Accept("account", "owner", one.ID, one.Revision, override)
	if err != nil { t.Fatal(err) }
	two, err = execution.Accept("account", "owner", two.ID, two.Revision)
	if err != nil { t.Fatal(err) }
	if !reflect.DeepEqual(one.ModelProfile.Action, override.Action) || !reflect.DeepEqual(two.ModelProfile.Action, initial.Action) { t.Fatal("workers lost independent choices") }
	// Change account defaults directly in this isolated fixture; product code must not.
	record, _, err := store.NewAgentModelSettingsStore(ss.Store().Underlying()).GetForAccount("account")
	if err != nil { t.Fatal(err) }
	record.Swarm.Action = store.AgentModelAssignment{Provider: override.Action.Provider, Model: override.Action.Model, Thinking: override.Action.Thinking}
	record.Swarm.Plan = record.Swarm.Action
	if _, err = store.NewAgentModelSettingsStore(ss.Store().Underlying()).PutForAccount(record); err != nil { t.Fatal(err) }
	for i, w := range []store.WorkerRecord{one, two, two} {
		r, err := execution.Dispatch(ctx, "account", "owner", store.WorkerRunAdmission{WorkerID: w.ID, RequestSource: "direct", Input: map[string]any{"prompt": "Review"}, IdempotencyKey: []string{"first", "second", "subsequent"}[i]})
		if err != nil { t.Fatal(err) }
		snapshot, found, err := ss.GetSession(r.SessionID)
		if err != nil || !found || !reflect.DeepEqual(snapshot.ModelProfile, w.ModelProfile) || snapshot.Title != w.Name || snapshot.Preference.Model != w.ModelProfile.Plan.Model { t.Fatalf("job did not pin worker choice/title: %+v %v", snapshot, err) }
		task, found, err := ss.Store().GetProjectTask("account", "proj_fixture", "task_"+r.ID)
		if err != nil || !found || task.Title != w.Name || task.Agent != "swarm" { t.Fatalf("presentation changed runtime identity: %+v %v", task, err) }
	}
}

// Requirement: unavailable initial settings use only the resolved account
// default and disclose the fallback; an explicit invalid review never falls
// back. ResolveWorkerModelProfile is the narrowest catalog/settings boundary.
func TestWorkerModelDefaultFallbackIsVisibleAndExplicitInvalidDoesNotFallback(t *testing.T) {
	runs, ss, _, _ := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	initial, err := runs.ResolveWorkerModelProfile("account", nil)
	if err != nil { t.Fatal(err) }
	models := store.NewModelStore(ss.Store().Underlying())
	if _, err = models.SetPreferenceForAccount("account", "owner", initial.Action.Provider, initial.Action.Model, initial.Action.Thinking, initial.Action.ServiceTier, initial.Action.ContextMode); err != nil { t.Fatal(err) }
	settings := store.NewAgentModelSettingsStore(ss.Store().Underlying())
	record, _, err := settings.GetForAccount("account")
	if err != nil { t.Fatal(err) }
	record.Swarm.Action.Model = "unavailable-model"
	if _, err = settings.PutForAccount(record); err != nil { t.Fatal(err) }
	fallback, err := runs.ResolveWorkerModelProfile("account", nil)
	if err != nil || fallback.ResolutionWarning == "" || fallback.Action != initial.Action || fallback.Plan == nil || *fallback.Plan != initial.Action { t.Fatalf("missing visible account fallback: %+v %v", fallback, err) }
	explicit := store.CloneSessionModelProfileSnapshot(initial)
	explicit.Action.Model = "unavailable-model"
	if _, err = runs.ResolveWorkerModelProfile("account", explicit); err == nil { t.Fatal("explicit invalid selection silently fell back") }
}
