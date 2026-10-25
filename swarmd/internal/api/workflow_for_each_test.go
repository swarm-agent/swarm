package api

import (
	"swarm/packages/swarmd/internal/jobautomation"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: resolveWorkflowWorkers is the shared save boundary: only active,
// same-account, non-reviewer deployments may receive matrix items. Real worker
// records prove name resolution and rejection without admitting any runs.
func TestForEachWorkerValidation(t *testing.T) {
	f := newReopenFixture(t)
	p := testPrincipal()
	ws := f.server.sessions.Store().WorkerStore()
	for _, role := range []string{store.WorkerRoleCoder, store.WorkerRoleReviewer} {
		w, err := ws.CreateWorker(p.AccountScopeID, p.UserID, store.CreateWorkerRequest{Name: "matrix-" + role, Role: role, IdempotencyKey: "matrix-" + role}, nil)
		if err != nil {
			t.Fatal(err)
		}
		a := jobautomation.Automation{Account: p.AccountScopeID, Steps: []jobautomation.Step{{Type: jobautomation.StepAgent, Worker: w.ID, Brief: "write", ForEach: &jobautomation.ForEach{File: "plan.json"}}}}
		if err := f.server.resolveWorkflowWorkers(&a); !jobautomation.IsValidationError(err) {
			t.Fatal("inactive accepted", err)
		}
		w, err = ws.ActivateWorker(p.AccountScopeID, p.UserID, w.ID, w.Revision, nil)
		if err != nil {
			t.Fatal(err)
		}
		a.Steps[0].Worker = w.Name
		err = f.server.resolveWorkflowWorkers(&a)
		if role == store.WorkerRoleReviewer {
			if !jobautomation.IsValidationError(err) {
				t.Fatal("reviewer accepted", err)
			}
		} else if err != nil || a.Steps[0].Worker != w.ID {
			t.Fatal(a, err)
		}
		a.Account = "other-account"
		a.Steps[0].Worker = w.ID
		if err := f.server.resolveWorkflowWorkers(&a); !jobautomation.IsValidationError(err) {
			t.Fatal("cross-account accepted", err)
		}
		runs, _, err := ws.ListWorkerRuns(p.AccountScopeID, w.ID, 10, "")
		if err != nil || len(runs) != 0 {
			t.Fatal("validation admitted work", runs, err)
		}
	}
}

// Purpose: jobDeliveryView is the shared HTTP/native/MCP delivery boundary.
// Assert all public item fields survive it without exposing the private brief.
func TestForEachDeliveryView(t *testing.T) {
	d := jobautomation.Delivery{Steps: []jobautomation.StepRecord{{Items: []jobautomation.ItemRecord{{Index: 2, Label: "Post", State: "failed", JobID: "job", Error: "failure", Brief: "private brief"}}}}}
	view := jobDeliveryView(d)
	item := view["steps"].([]map[string]any)[0]["items"].([]map[string]any)[0]
	want := map[string]any{"index": 2, "label": "Post", "state": "failed", "job_id": "job", "error": "failure"}
	if len(item) != len(want) {
		t.Fatalf("unexpected fields: %v", item)
	}
	for key, value := range want {
		if item[key] != value {
			t.Fatalf("%s: %v", key, item)
		}
	}
}
