package pebblestore

import "testing"

// Purpose: model-only pins must commit without staged review and cannot rewrite
// admitted receipts. Threat: stale/foreign edits or later pins change run history.
// SetWorkerModelProfile and AdmitWorkerRun are the narrow durable authority layer.
func TestWorkerModelPinReceipt(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ws := NewWorkerStore(db)
	selection := ModelProfileSelection{Provider: "fixture", Model: "pinned"}
	profile := &SessionModelProfileSnapshot{Source: SessionModelProfileSourceTemporary, Action: selection, Plan: &selection}
	w, err := ws.CreateWorker("account", "owner", CreateWorkerRequest{Name: "Reviewer", Instructions: "Review", WorkspaceRequirements: []WorkerWorkspaceRequirement{{Role: "primary", Required: true}}, ModelProfile: profile, InitialLifecycleState: WorkerLifecycleStateIdle}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = ws.ActivateWorker("account", "owner", w.ID, w.Revision, map[string]string{"primary": "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range []string{"foreign", "account"} {
		revision := w.Revision
		if account == "account" {
			revision++
		}
		if _, err := ws.SetWorkerModelProfile(account, "owner", w.ID, revision, profile); err == nil {
			t.Fatal("unauthorized/stale pin accepted")
		}
	}
	pinned, err := ws.SetWorkerModelProfile("account", "owner", w.ID, w.Revision, profile)
	if err != nil || pinned.PendingReview != nil || pinned.ModelProfile.Action.Model != "pinned" {
		t.Fatalf("pin: %+v %v", pinned, err)
	}
	receipt, err := ws.AdmitWorkerRun("account", WorkerRunAdmission{WorkerID: w.ID, UserID: "owner", RequestSource: "direct", IdempotencyKey: "pin", Input: map[string]any{"prompt": "Review"}, ExpectedWorkerRevision: pinned.Revision, ModelSource: "pinned", ResolvedModelProfile: profile})
	if err != nil {
		t.Fatal(err)
	}
	inherited := &SessionModelProfileSnapshot{Source: SessionModelProfileSourceSwarmSettings, UseAccountDefault: true}
	cleared, err := ws.SetWorkerModelProfile("account", "owner", w.ID, pinned.Revision, inherited)
	if err != nil || !cleared.ModelProfile.UseAccountDefault || cleared.PendingReview != nil {
		t.Fatalf("clear: %+v %v", cleared, err)
	}
	loaded, found, err := ws.GetWorkerRun("account", w.ID, receipt.ID)
	if err != nil || !found || loaded.ModelProfile.Action.Model != "pinned" || loaded.ModelSource != "pinned" {
		t.Fatalf("receipt changed: %+v %v", loaded, err)
	}
}
