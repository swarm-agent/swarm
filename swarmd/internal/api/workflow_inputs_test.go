package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: ImportWorkflow/SaveWorkflow must create independent instances and
// ExportWorkflow must not leak instance values. Real store-backed shared service
// calls are the narrowest proof of persistence, idempotence and refused writes.
func TestWorkflowInputsTransfer(t *testing.T) {
	s, db := newRunOwnerServer(t)
	caller := workflowCallerFromRequest(withTestPrincipal(httptest.NewRequest("POST", "/", nil)))
	folder := t.TempDir()
	def := map[string]any{"swarm_workflow": 1, "name": "pipeline", "scope": map[string]any{"folder": folder}, "inputs": map[string]any{"brand": map[string]any{"description": "Brand"}}, "steps": []any{map[string]any{"type": "agent", "worker": "coder", "brief": "write {{inputs.brand}}"}}}
	for _, brand := range []string{"swarm", "nameflow"} {
		args := map[string]any{"definition": def, "name": brand, "with": map[string]string{"brand": brand}}
		if _, err := s.ImportWorkflow(caller, args); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ImportWorkflow(caller, args); err != nil {
			t.Fatal(err)
		}
	}
	list := storedAutomations(t, db)
	if len(list) != 2 {
		t.Fatal(list)
	}
	for _, a := range list {
		if a.With["brand"] != a.Name || a.Steps[0].Brief != "write {{inputs.brand}}" {
			t.Fatal(a)
		}
		exported, err := s.ExportWorkflow(caller, map[string]any{"id": a.ID})
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(exported)
		if strings.Contains(string(data), `"with"`) || !strings.Contains(string(data), "{{inputs.brand}}") || !strings.Contains(string(data), `"inputs"`) {
			t.Fatal(string(data))
		}
	}
	if _, err := s.ImportWorkflow(caller, map[string]any{"definition": def, "name": "missing"}); !jobautomation.IsValidationError(err) {
		t.Fatal(err)
	}
	if len(storedAutomations(t, db)) != 2 {
		t.Fatal("refused import mutated store")
	}
}

// Purpose: workflowWorker must resolve exactly one active same-account name,
// never a cross-account or ambiguous candidate. Real worker store transitions
// prove the lookup boundary without dispatching any jobs.
func TestWorkflowInputsWorkerNames(t *testing.T) {
	f := newReopenFixture(t)
	p := testPrincipal()
	ws := f.server.sessions.Store().WorkerStore()
	create := func(key string) string {
		w, err := ws.CreateWorker(p.AccountScopeID, p.UserID, pebblestore.CreateWorkerRequest{Name: "Brand writer", Role: pebblestore.WorkerRoleCoder, IdempotencyKey: key}, nil)
		if err != nil {
			t.Fatal(err)
		}
		w, err = ws.ActivateWorker(p.AccountScopeID, p.UserID, w.ID, w.Revision, nil)
		if err != nil {
			t.Fatal(err)
		}
		return w.ID
	}
	id := create("brand-one")
	got, err := f.server.workflowWorker(p.AccountScopeID, "Brand writer")
	if err != nil || got != id {
		t.Fatal(got, err)
	}
	for _, account := range []string{p.AccountScopeID, "other-account"} {
		if _, err := f.server.workflowWorker(account, "missing"); !jobautomation.IsValidationError(err) {
			t.Fatal(err)
		}
	}
	if _, err := f.server.workflowWorker("other-account", "Brand writer"); !jobautomation.IsValidationError(err) {
		t.Fatal(err)
	}
	create("brand-two")
	if _, err := f.server.workflowWorker(p.AccountScopeID, "Brand writer"); !jobautomation.IsValidationError(err) || !strings.Contains(err.Error(), "2 active matches") {
		t.Fatal(err)
	}
	if got, err := f.server.workflowWorker(p.AccountScopeID, id); err != nil || got != id {
		t.Fatal(got, err)
	}
}

// Purpose: RestoreWorkflow must restore revision bindings, not export defaults
// or current bindings. Real service/store calls prove the saved template and
// rendered execution value for both required and defaulted inputs.
func TestWorkflowInputsRestoreBindings(t *testing.T) {
	for _, defaulted := range []bool{false, true} {
		t.Run(fmt.Sprint(defaulted), func(t *testing.T) {
			s, db := newRunOwnerServer(t)
			caller := workflowCallerFromRequest(withTestPrincipal(httptest.NewRequest("POST", "/", nil)))
			decl := map[string]any{"description": "brand"}
			if defaulted {
				decl["default"] = "fallback"
			}
			args := map[string]any{"name": "restore-inputs", "scope": map[string]any{"folder": t.TempDir()}, "inputs": map[string]any{"brand": decl}, "with": map[string]string{"brand": "swarm"}, "steps": []any{map[string]any{"type": "agent", "worker": "coder", "brief": "write {{inputs.brand}}"}}}
			if _, err := s.saveWorkflowArgs(caller, args, false, "set"); err != nil {
				t.Fatal(err)
			}
			original := storedAutomations(t, db)[0]
			args["with"] = map[string]string{"brand": "nameflow"}
			if _, err := s.saveWorkflowArgs(caller, args, false, "set"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.RestoreWorkflow(caller, map[string]any{"id": original.ID, "revision": original.Revision}); err != nil {
				t.Fatal(err)
			}
			saved := storedAutomations(t, db)[0]
			if saved.With["brand"] != "swarm" || saved.Steps[0].Brief != "write {{inputs.brand}}" || saved.Revision != 3 {
				t.Fatalf("restored: %+v", saved)
			}
			rendered, err := saved.RenderInputs()
			if err != nil || rendered.Steps[0].Brief != "write swarm" {
				t.Fatal(rendered, err)
			}
		})
	}
}

// Purpose: workflowWorker preserves existing idle IDs but not idle portable
// names. Role/Start retain legacy case-insensitive aliases; Start must still
// refuse coder fixes without admitting a run. This real worker fixture tests
// lookup and admission boundaries without a provider.
func TestWorkflowInputsLegacyWorkerRuntime(t *testing.T) {
	f := newReopenFixture(t)
	p := testPrincipal()
	if id, err := f.server.workflowWorker(p.AccountScopeID, f.workerID); err != nil || id != f.workerID {
		t.Fatal(id, err)
	}
	w, err := f.server.sessions.Store().WorkerStore().CreateWorker(p.AccountScopeID, p.UserID, pebblestore.CreateWorkerRequest{Name: "Brand author", Role: pebblestore.WorkerRoleCoder, IdempotencyKey: "legacy-author", Metadata: map[string]any{"preset": "author-preset"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.workflowWorker(p.AccountScopeID, w.Name); !jobautomation.IsValidationError(err) {
		t.Fatal(err)
	}
	for _, ref := range []string{w.ID, "BRAND AUTHOR", "author-preset"} {
		role, err := (jobAutomationAgents{s: f.server}).Role(context.Background(), p.AccountScopeID, ref)
		if err != nil || role != "coder" {
			t.Fatal(role, err)
		}
		_, err = f.server.JobAutomationAgents().Start(context.Background(), jobautomation.AgentRequest{Account: p.AccountScopeID, Worker: ref, Brief: "review", Fixes: jobautomation.FixesSecurity})
		if err == nil || !strings.Contains(err.Error(), "only for a reviewer worker") {
			t.Fatal(err)
		}
	}
	_, err = f.server.sessions.Store().WorkerStore().CreateWorker(p.AccountScopeID, p.UserID, pebblestore.CreateWorkerRequest{Name: "Brand author", Role: pebblestore.WorkerRoleCoder, IdempotencyKey: "legacy-duplicate", Metadata: map[string]any{"preset": "author-preset"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"BRAND AUTHOR", "author-preset"} {
		if _, err := (jobAutomationAgents{s: f.server}).Role(context.Background(), p.AccountScopeID, ref); err == nil || !strings.Contains(err.Error(), "ambiguous") {
			t.Fatal(err)
		}
	}
	runs, _, err := f.server.sessions.Store().WorkerStore().ListWorkerRuns(p.AccountScopeID, w.ID, 10, "")
	if err != nil || len(runs) != 0 {
		t.Fatal(runs, err)
	}
}

// Purpose: SaveWorkflow must persist a resolved scope ID while retaining its
// template. Real service/store reads and Matches prove name binding cannot
// select another worker; refused names must leave the saved revision unchanged.
func TestWorkflowInputsScopeWorker(t *testing.T) {
	f := newReopenFixture(t)
	f.server.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: jobautomation.NewStore(f.server.sessions.Store().Underlying())}))
	p := testPrincipal()
	ws := f.server.sessions.Store().WorkerStore()
	w, err := ws.CreateWorker(p.AccountScopeID, p.UserID, pebblestore.CreateWorkerRequest{Name: "Brand maker", Role: pebblestore.WorkerRoleCoder, IdempotencyKey: "scope-maker"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = ws.ActivateWorker(p.AccountScopeID, p.UserID, w.ID, w.Revision, nil)
	if err != nil {
		t.Fatal(err)
	}
	caller := workflowCallerFromRequest(withTestPrincipal(httptest.NewRequest("POST", "/", nil)))
	body := WorkflowDefinition{Name: "scoped", Scope: jobautomation.Scope{Folder: t.TempDir(), Worker: "{{inputs.maker}}"}, Inputs: map[string]jobautomation.Input{"maker": {}}, With: map[string]string{"maker": w.Name}, Steps: []jobautomation.Step{{Type: jobautomation.StepAgent, Worker: "coder", Brief: "work"}}}
	saved, _, _, err := f.server.SaveWorkflow(caller, body, "set", false)
	if err != nil {
		t.Fatal(err)
	}
	saved, err = f.server.GetWorkflow(caller, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Scope.Worker != "{{inputs.maker}}" || saved.ScopeWorkerID != w.ID {
		t.Fatalf("scope: %+v", saved)
	}
	result := &jobautomation.Result{Folder: body.Scope.Folder, Status: jobautomation.StatusSucceeded, Worker: &jobautomation.ResultWorker{ID: w.ID}}
	if !saved.Matches(result) {
		t.Fatal("bound worker did not match")
	}
	result.Worker.ID = f.workerID
	if saved.Matches(result) {
		t.Fatal("matched another worker")
	}
	if _, err := ws.CreateWorker(p.AccountScopeID, p.UserID, pebblestore.CreateWorkerRequest{Name: "Inactive maker", Role: pebblestore.WorkerRoleCoder, IdempotencyKey: "inactive-maker"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"missing maker", "Inactive maker"} {
		body.With["maker"] = name
		if _, _, _, err := f.server.SaveWorkflow(caller, body, "set", false); !jobautomation.IsValidationError(err) {
			t.Fatal(err)
		}
		current, err := f.server.GetWorkflow(caller, saved.ID)
		if err != nil || current.Revision != saved.Revision || current.ScopeWorkerID != w.ID {
			t.Fatalf("refusal changed record: %+v %v", current, err)
		}
	}
}
