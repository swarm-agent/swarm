package run

import (
	"context"
	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
	"time"
)

// Purpose: Dispatch must use job override > worker pin > role default and capture
// provenance before enqueue. Threat: mutable worker policy changes historical runs
// or retries silently accept different overrides. The real temporary execution
// fixture is the narrowest layer proving receipt plus prepared session selection.
func TestWorkerJobModelPrecedence(t *testing.T) {
	runs, ss, execution, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	defaults, err := runs.ResolveWorkerRoleModelProfile("account", store.WorkerRoleCoder, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, catalog, found, err := runs.model.RecommendedCatalogDefaults("google")
	if err != nil || !found {
		t.Fatalf("catalog: %v", err)
	}
	sel := store.ModelProfileSelection{Provider: "google", Model: catalog.Model, Thinking: catalog.DefaultThinking}
	pin := &store.SessionModelProfileSnapshot{Source: store.SessionModelProfileSourceTemporary, Action: sel, Plan: &sel}
	ws := ss.Store().WorkerStore()
	w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{Name: "Reviewer", Role: store.WorkerRoleCoder, Instructions: "Review", ModelProfile: defaults, InitialLifecycleState: store.WorkerLifecycleStatePending, ProposedBindings: map[string]string{"primary": workspaceID}, WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = execution.Accept("account", "owner", w.ID, w.Revision)
	if err != nil {
		t.Fatal(err)
	}
	w, err = ws.SetWorkerModelProfile("account", "owner", w.ID, w.Revision, pin)
	if err != nil {
		t.Fatal(err)
	}
	dispatch := func(key string, override *store.SessionModelProfileSnapshot) store.WorkerRunRecord {
		t.Helper()
		r, err := execution.Dispatch(ctx, "account", "owner", store.WorkerRunAdmission{WorkerID: w.ID, RequestSource: "direct", Input: map[string]any{"prompt": "Review"}, IdempotencyKey: key, ModelOverride: override})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	pinned := dispatch("pinned", nil)
	if pinned.ModelSource != "pinned" || pinned.ModelProfile.Action.Model != pin.Action.Model {
		t.Fatalf("pin not used: %+v", pinned)
	}
	override := store.CloneSessionModelProfileSnapshot(defaults)
	override.UseAccountDefault = false
	job := dispatch("override", override)
	if job.ModelSource != "job override" || job.ModelProfile.Action != override.Action {
		t.Fatalf("override not used: %+v", job)
	}
	session, ok, err := ss.GetSession(job.SessionID)
	if err != nil || !ok || session.Preference.Model != override.Action.Model {
		t.Fatalf("prepared session: %+v %v", session.Preference, err)
	}
	if _, err := execution.Dispatch(ctx, "account", "owner", store.WorkerRunAdmission{WorkerID: w.ID, RequestSource: "direct", Input: map[string]any{"prompt": "Review"}, IdempotencyKey: "override", ModelOverride: pin}); err == nil {
		t.Fatal("same key accepted different override")
	}
	current, _, _ := ws.GetWorker("account", w.ID)
	_, err = ws.SetWorkerModelProfile("account", "owner", w.ID, current.Revision, &store.SessionModelProfileSnapshot{Source: store.SessionModelProfileSourceSwarmSettings, UseAccountDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	role := dispatch("cleared", nil)
	if role.ModelSource != "role default" || role.ModelProfile.Action != defaults.Action {
		t.Fatalf("clear did not follow role: %+v", role)
	}
	history, _, err := ws.GetWorkerRun("account", w.ID, pinned.ID)
	if err != nil || history.ModelSource != "pinned" || history.ModelProfile.Action != pin.Action {
		t.Fatalf("history changed: %+v %v", history, err)
	}
}
