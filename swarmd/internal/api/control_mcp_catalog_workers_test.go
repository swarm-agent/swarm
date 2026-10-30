package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	runruntime "swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
	"swarm/packages/swarmd/internal/worktree"
)

// seedWorkers gives the home and the linked machine their worker service
// (the run service's ManageWorkers with its worker execution service; no run
// is ever started: admission's enqueue refuses) and an active worker each
// ("active", h.workers[machine]).
func (h *catalogParityHarness) seedWorkers(t *testing.T) {
	t.Helper()
	h.workers = map[string]string{}
	for _, machine := range []struct {
		name          string
		s             *Server
		account, user string
	}{{"", h.home.s, h.home.account, h.home.user}, {"box", h.remote.s, h.remote.account, h.remote.user}} {
		if machine.s.runner != nil {
			if _, ok := machine.s.runner.(workerCatalogRunner); !ok {
				t.Fatalf("machine %q has a run service without ManageWorkers: %T", machine.name, machine.s.runner)
			}
		} else {
			// A fixture catalog model and account model settings, as run's
			// worker model tests use: worker records resolve their model
			// (no provider is ever called).
			db := machine.s.sessions.Store().Underlying()
			if err := pebblestore.NewModelCatalogStore(db).SetRecord(pebblestore.ModelCatalogRecord{Provider: "fixture-provider", Model: "fixture-model", ThinkingOptions: []string{"low"}, DefaultThinking: "low"}); err != nil {
				t.Fatal(err)
			}
			assigned := pebblestore.AgentModelAssignment{Provider: "fixture-provider", Model: "fixture-model", Thinking: "low"}
			settings := pebblestore.NewAgentModelSettingsStore(db)
			if _, err := settings.PutForAccount(pebblestore.AgentModelSettingsRecord{AccountScopeID: machine.account, Swarm: pebblestore.SwarmAgentModelAssignments{Action: assigned, Plan: assigned}, SystemAgents: pebblestore.SystemAgentModelAssignments{Compact: assigned, Finder: assigned, Coder: assigned, Designer: assigned, Router: assigned}}); err != nil {
				t.Fatal(err)
			}
			models := model.NewService(pebblestore.NewModelStore(db), nil, model.NewCatalogService(pebblestore.NewModelCatalogStore(db)))
			runner := runruntime.NewService(machine.s.sessions, models, nil, nil, nil, nil, nil, nil)
			runner.SetAgentModelSettingsService(agentmodelsettings.NewService(settings))
			execution, err := runruntime.NewWorkerExecutionService(runner, machine.s.sessions.Store(), &worktree.Service{}, machine.s.applySessionV3PrimaryMutation, func(identity.Principal, pebblestore.V3SessionRunIntent) bool { return false })
			if err != nil {
				t.Fatal(err)
			}
			runner.SetWorkerExecutionService(execution)
			machine.s.runner = runner
		}
		w, err := machine.s.sessions.Store().WorkerStore().CreateWorker(machine.account, machine.user, pebblestore.CreateWorkerRequest{Name: "Parity active", Instructions: "Check the build.", Role: "finder"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		h.workers[machine.name] = w.ID
	}
}

// manageWorkersParityCases holds a case for every manage_workers action,
// here and on the linked machine box. Reads run on the seeded job worker
// ("job:worker"); changes run on records that refuse them the same way on
// both surfaces (a stale expected_revision, a workspace that does not
// exist, an automation or worker that does not exist, activation), so
// neither call changes what the other reads. The successful flows are
// TestManageWorkersFromBothSurfaces.
func manageWorkersParityCases() map[string][]catalogParityCase {
	everywhere := func(args ...any) []catalogParityCase {
		var out []catalogParityCase
		for _, machine := range []string{"", "box"} {
			call := parityArgs(args...)
			if machine != "" {
				call["machine"] = machine
			}
			out = append(out, catalogParityCase{orchestrator: call, control: call, compare: parityExact})
		}
		return out
	}
	missing := map[string]any{"primary": "ws_missing"}
	stale := float64(999)
	return map[string][]catalogParityCase{
		"help":               everywhere("action", "help"),
		"list":               everywhere("action", "list", "limit", float64(5)),
		"inspect":            everywhere("action", "inspect", "worker_id", "job:worker"),
		"create":             everywhere("action", "create", "name", "Parity new", "instructions", "Do it.", "local_bindings", missing),
		"update":             everywhere("action", "update", "worker_id", "job:worker", "expected_revision", stale, "name", "Renamed"),
		"attach":             everywhere("action", "attach", "worker_id", "job:worker", "expected_revision", stale, "automation", map[string]any{"name": "Nightly", "activation_mode": "manual"}),
		"disable_automation": everywhere("action", "disable_automation", "worker_id", "job:worker", "automation_id", "no-such-automation", "expected_revision", stale),
		"delete":             everywhere("action", "delete", "worker_id", "job:worker", "expected_revision", stale),
		"test":               everywhere("action", "test", "worker_id", "no-such-worker", "prompt", "Try.", "idempotency_key", "parity-test"),
		"request":            everywhere("action", "request", "worker_id", "no-such-worker", "prompt", "Do.", "idempotency_key", "parity-request"),
		"activate":           everywhere("action", "activate", "worker_id", "job:worker", "expected_revision", stale),
		"pause":              everywhere("action", "pause", "worker_id", "job:worker", "expected_revision", stale),
		"resume":             everywhere("action", "resume", "worker_id", "job:worker", "expected_revision", stale),
		"archive":            everywhere("action", "archive", "worker_id", "job:worker", "expected_revision", stale),
		"propose":            everywhere("action", "propose", "name", "Parity proposal", "instructions", "Do it.", "local_bindings", missing),
		"review":             everywhere("action", "review", "worker_id", "job:worker"),
		"context":            everywhere("action", "context", "worker_id", "job:worker"),
		"progress":           everywhere("action", "progress", "worker_id", "job:worker"),
		"assign_task":        everywhere("action", "assign_task", "worker_id", "no-such-worker", "task", "Check.", "idempotency_key", "parity-assign"),
	}
}

// workersEnvelope checks the catalog envelope and returns its state and data.
func workersEnvelope(t *testing.T, out string) (string, map[string]any) {
	t.Helper()
	if err := tool.CheckEnvelope(out); err != nil {
		t.Fatalf("not a catalog envelope: %v\n%s", err, out)
	}
	var envelope struct {
		Tool  string         `json:"tool"`
		State string         `json:"state"`
		Data  map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil || envelope.Tool != "manage_workers" {
		t.Fatalf("envelope %s: %v", out, err)
	}
	return envelope.State, envelope.Data
}

func workerRecordOf(t *testing.T, data map[string]any) map[string]any {
	t.Helper()
	worker, _ := data["worker"].(map[string]any)
	if worker == nil {
		t.Fatalf("no worker in %v", data)
	}
	return worker
}

// Purpose:
//   - Requirement: the Orchestrator and an MCP client run workers from the
//     catalog's manage_workers with the same actions and answers, here and on
//     a linked machine: Swarm Control creates a worker (pending the person's
//     acceptance, as the Orchestrator's), the Orchestrator reads it; the
//     Orchestrator updates an active worker and Swarm Control reads the
//     change; the Orchestrator pauses it and Swarm Control resumes it; the
//     Orchestrator gives it a task with assign_task (new for it) through the
//     swarm_assign_worker_task handler; on the linked machine the
//     Orchestrator pauses that machine's worker and Swarm Control reads it
//     paused, and both list the machine's workers as the bounded summary.
//   - Threat/regression: a worker action one surface cannot reach, a worker
//     made through Swarm Control that runs without acceptance, a change not
//     stored, or a linked action that does not reach the machine's record.
//   - Boundary/authority: tool.Runtime.executeManageWorkers (both surfaces)
//     -> api.Server.ManageWorkers -> run.Service.ManageWorkers (or the
//     swarm_assign_worker_task handler) and, with machine, CallFleetWork ->
//     the machine's POST /v3/worker-actions/{action}. Real stores on two
//     linked in-process daemons; no run starts (admission of assign_task is
//     recorded from the request the handler sent) and no provider is reached.
//   - Narrowest layer: the flow crosses both surfaces, the service, the
//     worker store and the linked route, so only routed calls prove it.
func TestManageWorkersFromBothSurfaces(t *testing.T) {
	h := newCatalogParityHarness(t)
	store := h.home.s.sessions.Store()
	full := catalogParityFullKey
	control := func(args map[string]any) (string, map[string]any) {
		t.Helper()
		out, refused := h.controlTool(t, full, "manage_workers", args)
		if refused != "" {
			t.Fatalf("Swarm Control manage_workers %v refused: %s", args["action"], refused)
		}
		return workersEnvelope(t, out)
	}
	orchestrator := func(args map[string]any) (string, map[string]any) {
		t.Helper()
		out, err := h.orchestratorTool(t, "manage_workers", args)
		if err != nil {
			t.Fatalf("Orchestrator manage_workers %v: %v", args["action"], err)
		}
		return workersEnvelope(t, out)
	}

	// Swarm Control creates a worker in a real folder: it waits for the
	// person's acceptance, and the Orchestrator reads the same record.
	h.home.s.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(store.Underlying()))
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "dev")
	outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "start")
	added, err := h.home.s.workspace.AddForPrincipal(h.p, folder, "project", "", false)
	if err != nil {
		t.Fatal(err)
	}
	_, created := control(map[string]any{"action": "create", "name": "From Swarm Control", "instructions": "Watch the build.", "local_bindings": map[string]any{"primary": added.WorkspaceID}, "idempotency_key": "e2e-create"})
	createdWorker := workerRecordOf(t, created)
	createdID, _ := createdWorker["id"].(string)
	if created["status"] != "created" || createdWorker["lifecycle_state"] != string(pebblestore.WorkerLifecycleStatePending) || createdID == "" {
		t.Fatalf("create through Swarm Control: %v", created)
	}
	record, found, err := store.WorkerStore().GetWorker(h.home.account, createdID)
	if err != nil || !found || record.LifecycleState != pebblestore.WorkerLifecycleStatePending {
		t.Fatalf("created worker %+v %v %v", record, found, err)
	}
	if _, read := orchestrator(map[string]any{"action": "inspect", "worker_id": createdID}); workerRecordOf(t, read)["name"] != "From Swarm Control" {
		t.Fatalf("Orchestrator inspect: %v", read)
	}

	// The Orchestrator updates an active worker; Swarm Control reads it.
	active := h.workers[""]
	current, _, _ := store.WorkerStore().GetWorker(h.home.account, active)
	_, updated := orchestrator(map[string]any{"action": "update", "worker_id": active, "expected_revision": float64(current.Revision), "name": "Renamed by the Orchestrator"})
	if workerRecordOf(t, updated)["name"] != "Renamed by the Orchestrator" {
		t.Fatalf("update: %v", updated)
	}
	if _, read := control(map[string]any{"action": "inspect", "worker_id": active}); workerRecordOf(t, read)["name"] != "Renamed by the Orchestrator" {
		t.Fatalf("Swarm Control inspect after update: %v", read)
	}

	// The Orchestrator pauses it, Swarm Control resumes it.
	current, _, _ = store.WorkerStore().GetWorker(h.home.account, active)
	if _, paused := orchestrator(map[string]any{"action": "pause", "worker_id": active, "expected_revision": float64(current.Revision)}); paused["status"] != string(pebblestore.WorkerLifecycleStatePaused) {
		t.Fatalf("pause: %v", paused)
	}
	current, _, _ = store.WorkerStore().GetWorker(h.home.account, active)
	if current.LifecycleState != pebblestore.WorkerLifecycleStatePaused {
		t.Fatalf("pause not stored: %+v", current)
	}
	if _, resumed := control(map[string]any{"action": "resume", "worker_id": active, "expected_revision": float64(current.Revision)}); resumed["status"] != string(pebblestore.WorkerLifecycleStateActive) {
		t.Fatalf("resume: %v", resumed)
	}

	// The Orchestrator gives it a task: the swarm_assign_worker_task handler
	// sends the worker's direct route the task as its prompt.
	routes := h.home.s.internalRouteHandler()
	var admitted map[string]any
	h.home.s.controlMCPRoutes = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v3/workers/"+active+"/direct" {
			routes.ServeHTTP(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&admitted)
		writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "run": map[string]any{"id": "assigned-run", "worker_id": active, "status": "queued"}})
	})
	_, assigned := orchestrator(map[string]any{"action": "assign_task", "worker_id": active, "task": "Check the nightly build.", "idempotency_key": "e2e-assign"})
	h.home.s.controlMCPRoutes = nil
	if admitted["prompt"] != "Check the nightly build." || admitted["idempotency_key"] != "e2e-assign" {
		t.Fatalf("assign_task admission: %v", admitted)
	}
	if run, _ := assigned["run"].(map[string]any); run == nil || assigned["idempotency_key"] != "e2e-assign" {
		t.Fatalf("assign_task: %v", assigned)
	}

	// On the linked machine: the Orchestrator pauses its worker, Swarm
	// Control reads it paused; both list it as the bounded summary.
	remoteWorker := h.workers["box"]
	remoteStore := h.remote.s.sessions.Store().WorkerStore()
	remoteCurrent, _, _ := remoteStore.GetWorker(h.remote.account, remoteWorker)
	orchestrator(map[string]any{"action": "pause", "machine": "box", "worker_id": remoteWorker, "expected_revision": float64(remoteCurrent.Revision)})
	if got, _, _ := remoteStore.GetWorker(h.remote.account, remoteWorker); got.LifecycleState != pebblestore.WorkerLifecycleStatePaused {
		t.Fatalf("linked pause not stored: %+v", got)
	}
	if _, read := control(map[string]any{"action": "inspect", "machine": "box", "worker_id": remoteWorker}); workerRecordOf(t, read)["lifecycle_state"] != string(pebblestore.WorkerLifecycleStatePaused) {
		t.Fatalf("linked inspect: %v", read)
	}
	for _, list := range []func(map[string]any) (string, map[string]any){control, orchestrator} {
		_, listed := list(map[string]any{"action": "list", "machine": "box"})
		raw, _ := json.Marshal(listed)
		if !strings.Contains(string(raw), remoteWorker) || strings.Contains(string(raw), "instructions") {
			t.Fatalf("linked list is not the bounded summary: %s", raw)
		}
	}
}

// Purpose:
//   - Requirement: each manage_workers action is listed and callable only for
//     a key holding its level (the swarm_* worker tool's) and one of the
//     worker routes' scopes; a key bound to one worker, agent or trigger is
//     refused every action at Swarm Control and at a machine's
//     POST /v3/worker-actions/{action}, with nothing changed.
//   - Threat/regression: a read or write key creating, changing or deleting
//     workers through the catalog, or a worker-bound key managing another
//     worker (the same bypass the reviewer found for manage_jobs).
//   - Boundary/authority: controlMCPCall.visibleTools/callCatalogTool,
//     catalogBoundKey, handleWorkerActions, on the parity harness daemons.
//   - Narrowest layer: listing, the call gate and the linked route are
//     separate entries; each is called directly.
func TestManageWorkersLevelsScopesAndBoundKeys(t *testing.T) {
	h := newCatalogParityHarness(t)
	listed := func(record *pebblestore.ScopedTokenRecord) map[string]bool {
		t.Helper()
		req := requestWithScopedToken(requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/mcp", nil), h.home.user, h.home.account), record)
		c := &controlMCPCall{request: req, server: h.home.s}
		for _, candidate := range c.visibleTools() {
			if candidate.Name != "manage_workers" {
				continue
			}
			actions := map[string]bool{}
			for _, action := range candidate.InputSchema["properties"].(map[string]any)["action"].(map[string]any)["enum"].([]string) {
				actions[action] = true
			}
			return actions
		}
		return nil
	}
	all := listed(&pebblestore.ScopedTokenRecord{ID: "full", Scopes: catalogParityFullKey})
	for _, action := range manageWorkersParityCasesNames() {
		if !all[action] {
			t.Fatalf("the default listing lacks manage_workers %s: %v", action, all)
		}
	}
	write := listed(&pebblestore.ScopedTokenRecord{ID: "write", Scopes: []string{"swarm:read", "swarm:write", "automations:read", "automations:write"}})
	if !write["assign_task"] || !write["propose"] || !write["list"] || write["create"] || write["delete"] || write["pause"] || write["update"] {
		t.Fatalf("write key listing: %v", write)
	}
	read := listed(&pebblestore.ScopedTokenRecord{ID: "read", Scopes: []string{"swarm:read", "automations:read"}})
	if !read["list"] || !read["inspect"] || read["assign_task"] || read["propose"] || read["create"] {
		t.Fatalf("read key listing: %v", read)
	}
	if none := listed(&pebblestore.ScopedTokenRecord{ID: "sessions", Scopes: []string{"swarm:read", "swarm:manage", "sessions:read", "sessions:write"}}); len(none) != 0 {
		t.Fatalf("a key without automations scopes lists manage_workers: %v", none)
	}
	active := h.workers[""]
	store := h.home.s.sessions.Store().WorkerStore()
	before, _, _ := store.GetWorker(h.home.account, active)
	pause := map[string]any{"action": "pause", "worker_id": active, "expected_revision": float64(before.Revision)}
	if _, refused := h.controlTool(t, []string{"swarm:read", "swarm:write", "automations:write"}, "manage_workers", pause); !strings.Contains(refused, "needs swarm:manage") {
		t.Fatalf("write key pause: %q", refused)
	}
	if _, refused := h.controlTool(t, []string{"swarm:read", "swarm:manage", "automations:read"}, "manage_workers", pause); !strings.Contains(refused, "automations:write") {
		t.Fatalf("key without automations:write pause: %q", refused)
	}
	bound := []*pebblestore.ScopedTokenRecord{
		{ID: "worker-bound", Scopes: catalogParityFullKey, WorkerID: "some-other-worker"},
		{ID: "agent-bound", Scopes: catalogParityFullKey, AgentName: "support"},
	}
	for _, key := range bound {
		if got := listed(key); len(got) != 0 {
			t.Fatalf("%s key lists manage_workers: %v", key.ID, got)
		}
		req := requestWithScopedToken(requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/mcp", nil), h.home.user, h.home.account), key)
		c := &controlMCPCall{request: req, server: h.home.s}
		params, _ := json.Marshal(map[string]any{"name": "manage_workers", "arguments": pause})
		result, rpcErr := c.callTool(params)
		if rpcErr != nil {
			t.Fatalf("tools/call: %+v", rpcErr)
		}
		text, _ := result["content"].([]map[string]any)[0]["text"].(string)
		if result["isError"] != true || !strings.Contains(text, "bound to one worker, agent or trigger") {
			t.Fatalf("%s key pause through Swarm Control: %v", key.ID, result)
		}
		body, _ := json.Marshal(map[string]any{"worker_id": active, "expected_revision": float64(before.Revision)})
		linked := requestWithScopedToken(requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, WorkerActionsPath+"/pause", strings.NewReader(string(body))), h.home.user, h.home.account), key)
		linked.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.home.s.handleWorkerActions(rec, linked)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s key on %s: %d %s", key.ID, WorkerActionsPath, rec.Code, rec.Body.String())
		}
	}
	after, _, _ := store.GetWorker(h.home.account, active)
	if after.Revision != before.Revision || after.LifecycleState != before.LifecycleState {
		t.Fatalf("a refused call changed the worker: %+v -> %+v", before, after)
	}
	// The machine's route holds a scoped key to its scopes.
	body, _ := json.Marshal(map[string]any{})
	req := requestWithScopedToken(requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, WorkerActionsPath+"/list", strings.NewReader(string(body))), h.remote.user, h.remote.account), &pebblestore.ScopedTokenRecord{ID: "remote-sessions", Scopes: []string{"sessions:read"}})
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.remote.s.handleWorkerActions(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a sessions:read key listed workers on the machine's route: %d %s", rec.Code, rec.Body.String())
	}
}

// manageWorkersParityCasesNames are the actions the parity cases cover.
func manageWorkersParityCasesNames() []string {
	var names []string
	for name := range manageWorkersParityCases() {
		names = append(names, name)
	}
	return names
}
