package api

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: authorized AI saves must update active worker definitions without a
// human acceptance gate, while unsafe reviewer edits are atomic refusals.
// controlMCPUpdateWorker, handleWorker and WorkerStore.UpdateWorker own this
// boundary; real MCP dispatch and an isolated store prove persisted results.
func TestMCPWorkerDirectUpdate(t *testing.T) {
	c, ws := newMCPPresetTestCall(t)
	c.request = requestWithScopedToken(c.request, &store.ScopedTokenRecord{Scopes: []string{"admin"}})
	account := testPrincipal().AccountScopeID
	deployed, err := controlMCPDeployWorker(c, map[string]any{"name": "direct-reviewer", "role": "reviewer", "instructions": "old guidance"})
	if err != nil {
		t.Fatal(err)
	}
	id := controlMCPMap(controlMCPMap(deployed)["worker"])["id"].(string)
	before, _, err := ws.GetWorker(account, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = controlMCPUpdateWorker(c, map[string]any{"worker_id": id, "instructions": "new guidance"}); err != nil {
		t.Fatal(err)
	}
	after, _, err := ws.GetWorker(account, id)
	if err != nil || after.Instructions != "new guidance" || after.Revision != before.Revision+1 || after.PendingReview != nil || after.LifecycleState != before.LifecycleState {
		t.Fatalf("direct update %+v %v", after, err)
	}
	oldRevision, found, err := ws.GetWorkerRevision(account, id, before.Revision)
	if err != nil || !found || oldRevision.Worker.Instructions != "old guidance" {
		t.Fatalf("old revision changed %v", err)
	}
	newRevision, found, err := ws.GetWorkerRevision(account, id, after.Revision)
	if err != nil || !found || newRevision.Worker.Instructions != "new guidance" || newRevision.CommittedBy != testPrincipal().UserID || newRevision.ChangeSummary == "" {
		t.Fatalf("new revision not recorded %+v %v", newRevision, err)
	}
	if _, err = controlMCPDefineAgent(c, map[string]any{"name": "writing-agent", "instructions": "Write", "tools": "build"}); err != nil {
		t.Fatal(err)
	}
	if _, err = controlMCPUpdateWorker(c, map[string]any{"worker_id": id, "agent": "writing-agent"}); err == nil || !strings.Contains(err.Error(), "write") {
		t.Fatalf("unsafe update accepted %v", err)
	}
	unchanged, _, err := ws.GetWorker(account, id)
	if err != nil || !reflect.DeepEqual(after, unchanged) {
		t.Fatalf("failed save mutated worker %v", err)
	}
	if _, err = c.dispatch(http.MethodPut, "/v3/workers/"+id, nil, map[string]any{"expected_revision": before.Revision, "instructions": "stale"}); err == nil {
		t.Fatal("stale revision accepted")
	}
	if _, err = controlMCPDeployWorker(c, map[string]any{"worker": id, "instructions": "deployed guidance", "description": "updated"}); err != nil {
		t.Fatal(err)
	}
	final, _, err := ws.GetWorker(account, id)
	if err != nil || final.Instructions != "deployed guidance" || final.Description != "updated" || final.PendingReview != nil {
		t.Fatalf("deploy did not save %+v %v", final, err)
	}
	if _, err = controlMCPDeployWorker(c, map[string]any{"worker": id, "role": "coder", "instructions": "forbidden"}); err == nil {
		t.Fatal("role change accepted")
	}
	unchanged, _, err = ws.GetWorker(account, id)
	if err != nil || !reflect.DeepEqual(final, unchanged) {
		t.Fatal("role refusal changed worker")
	}
}

// Purpose: pre-upgrade pending reviews must remain explicitly acceptable even
// after a direct save. AcceptWorker owns this compatibility boundary; an
// isolated persisted legacy record avoids reintroducing proposal creation.
func TestWorkerLegacyReviewAfterDirectSave(t *testing.T) {
	c, ws := newMCPPresetTestCall(t)
	account := testPrincipal().AccountScopeID
	result, err := controlMCPDeployWorker(c, map[string]any{"name": "legacy-review", "role": "finder", "instructions": "original"})
	if err != nil {
		t.Fatal(err)
	}
	id := controlMCPMap(controlMCPMap(result)["worker"])["id"].(string)
	w, _, err := ws.GetWorker(account, id)
	if err != nil {
		t.Fatal(err)
	}
	// Admission receipts must retain the exact pre-save revision.
	admit := func(key string, worker store.WorkerRecord) store.WorkerRunRecord {
		t.Helper()
		model := store.CloneSessionModelProfileSnapshot(worker.ModelProfile)
		if model != nil {
			model.UseAccountDefault, model.ActionUseAccountDefault, model.PlanUseAccountDefault = false, false, false
		}
		run, err := ws.AdmitWorkerRun(account, store.WorkerRunAdmission{WorkerID: id, UserID: testPrincipal().UserID, WorkspaceID: "fixture-workspace", ResolvedWorkspaceID: "fixture-workspace", RequestSource: "direct", Input: map[string]any{"prompt": "Inspect the requested change"}, IdempotencyKey: key, ExpectedWorkerRevision: worker.Revision, ResolvedModelProfile: model})
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	oldRun := admit("before-save", w)
	proposal := w
	proposal.Instructions = "legacy proposed instructions"
	proposal.LocalBindings = nil
	proposal.LifecycleState = store.WorkerLifecycleStatePending
	w.PendingReview = &proposal
	if err = c.server.sessions.Store().Underlying().PutJSON(store.KeyWorker(account, id), w); err != nil {
		t.Fatal(err)
	}
	description := "saved directly"
	saved, err := ws.UpdateWorker(account, testPrincipal().UserID, id, w.Revision, store.UpdateWorkerRequest{Description: &description, Instructions: &description}, nil)
	if err != nil || saved.Description != description || !reflect.DeepEqual(saved.PendingReview, &proposal) {
		t.Fatalf("legacy proposal lost %v", err)
	}
	newRun := admit("after-save", saved)
	for _, check := range []struct {
		run  store.WorkerRunRecord
		want string
	}{{oldRun, "original"}, {newRun, description}} {
		history, found, err := ws.GetWorkerRevision(account, id, check.run.WorkerRevision)
		if err != nil || !found || history.Worker.Instructions != check.want {
			t.Fatalf("pinned instructions %+v %v", history, err)
		}
	}
	if oldRun.WorkerRevision == newRun.WorkerRevision {
		t.Fatal("admission did not pin new revision")
	}
	accepted, err := ws.AcceptWorker(account, testPrincipal().UserID, id, saved.Revision, nil)
	if err != nil || accepted.PendingReview != nil || accepted.Instructions != proposal.Instructions {
		t.Fatalf("legacy acceptance failed %+v %v", accepted, err)
	}
}
