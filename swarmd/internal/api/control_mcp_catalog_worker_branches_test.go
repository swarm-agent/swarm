package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/provider/registry"
	runruntime "swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
)

// Purpose:
//   - Requirement: every input workerControlRoutes lists as written by a
//     Swarm Control manage_workers branch is written by that branch's
//     handler; the refusal check (workerInputsNotApplied) lets through only
//     what the table lists, so a listed input that is not written would be
//     dropped while the call reports success.
//   - Threat/regression: an input listed as applied that the branch never
//     writes (create {worker, name} reported success and kept the old name).
//   - Boundary/authority: workerControlRoutes (the one table the handler and
//     the check read), workerControlCall, controlMCPDeployWorker,
//     controlMCPCreateWorker, controlMCPManageWorker, deployWorkerModel's
//     workerSaveFields; the worker store, budget store and run records are
//     the observable state.
//   - Narrowest layer: Swarm Control's real tools/call on the in-process
//     harness, one call per branch giving every listed write; no provider
//     runs (a ready fixture provider and its catalog model).
func TestEveryWorkerControlBranchWritesWhatItLists(t *testing.T) {
	h := newCatalogParityHarness(t)
	if h.home.s.workspace == nil {
		h.home.s.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(h.home.s.sessions.Store().Underlying()))
	}
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "dev")
	outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "start")
	place := h.seedSwarmAnswerProbes(t, folder)
	// The run service binds a worker to its folder through the workspace
	// service (the workspace authority activation checks).
	runner, ok := h.home.s.runner.(*runruntime.Service)
	if !ok {
		t.Fatalf("the harness's run service is %T", h.home.s.runner)
	}
	runner.SetWorkspaceService(h.home.s.workspace)
	store := h.home.s.sessions.Store()
	workers := store.WorkerStore()
	// A connected fixture provider and its catalog model (no provider runs),
	// as TestWorkerModelPinScopedToken wires them.
	h.home.s.providers = registry.New(onboardingProviderTestAdapter{id: "codex", ready: true})
	h.home.s.providers.RegisterRunner(controlMCPConnectTestRunner{id: "codex"})
	if err := pebblestore.NewModelCatalogStore(store.Underlying()).SetRecord(pebblestore.ModelCatalogRecord{Provider: "codex", Model: "gpt-5.5", ThinkingOptions: []string{"high"}, DefaultThinking: "high"}); err != nil {
		t.Fatal(err)
	}
	record := func(id string) pebblestore.WorkerRecord {
		t.Helper()
		got, found, err := workers.GetWorker(h.home.account, id)
		if err != nil || !found {
			t.Fatalf("worker %s: %v %v", id, found, err)
		}
		return got
	}
	byName := func(name string) pebblestore.WorkerRecord {
		t.Helper()
		listed, err := workers.ListWorkers(h.home.account, pebblestore.ListWorkersQuery{Limit: 500})
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range listed.Workers {
			if w.Name == name {
				return w
			}
		}
		t.Fatalf("no worker named %q", name)
		return pebblestore.WorkerRecord{}
	}
	model := map[string]any{"provider": "codex", "model": "gpt-5.5", "thinking": "high"}
	plan := map[string]any{"title": "Nightly check", "goal": "Check the build.", "checkpoints": []any{map[string]any{"title": "Build", "objective": "Run the build.", "tasks": []any{"make"}, "acceptance_criteria": []any{"it passes"}}}}

	// Each branch's call, keyed by its first trigger: every write it lists
	// with a value this test can find afterwards, and the worker it wrote.
	type outcome struct {
		worker pebblestore.WorkerRecord
		sent   map[string]any
	}
	var scheduled pebblestore.WorkerRecord
	run := func(branch workerControlBranch, call map[string]any) map[string]any {
		t.Helper()
		call["action"] = branch.action
		for _, key := range branch.writes {
			if _, set := call[key]; set || key == "agent" {
				// agent is proved by agentProbes: this harness has no
				// agent service, so a custom agent cannot be set here.
				continue
			}
			switch key {
			case "name", "instructions", "description":
				call[key] = fmt.Sprintf("%s for %s", key, branch.triggers[0])
			case "model":
				call[key] = model
			case "budget":
				call[key] = map[string]any{"daily_usd": float64(4)}
			case "role":
				call[key] = "finder"
			case "workspace_path":
				call[key] = folder
			case "project_id":
				call[key] = "probe-project"
			case "schedule":
				call[key] = map[string]any{"kind": "interval", "interval_seconds": float64(3600)}
			case "scheduled_plan":
				call[key] = plan
			case "automations":
				call[key] = []any{map[string]any{"name": "Extra automation", "activation_mode": "manual", "plan_document": map[string]any{"title": "Extra", "info": map[string]any{"goal": "Do it."}, "checkpoints": []any{map[string]any{"id": "cp1", "order": 1, "title": "Do", "objective": "Do it.", "tasks": []any{"do"}, "acceptance_criteria": []any{"done"}}}}}}
			case "schedule_enabled":
				call[key] = false
			}
		}
		out, refused := h.controlTool(t, catalogParityFullKey, "manage_workers", place(call))
		if refused != "" {
			t.Fatalf("branch %v refused a call giving only its own inputs: %s", branch.triggers, refused)
		}
		data, _ := parityDecode(t, out)["data"].(map[string]any)
		return data
	}
	probes := map[string]func(workerControlBranch) outcome{
		"worker": func(b workerControlBranch) outcome {
			call := map[string]any{"worker": h.workers[""], "name": "Renamed by worker"}
			run(b, call)
			return outcome{record(h.workers[""]), call}
		},
		"preset": func(b workerControlBranch) outcome {
			call := map[string]any{"preset": "designer"}
			data := run(b, call)
			worker, _ := data["worker"].(map[string]any)
			id, _ := worker["id"].(string)
			return outcome{record(id), call}
		},
		"role": func(b workerControlBranch) outcome {
			call := map[string]any{"name": "Branch by role"}
			run(b, call)
			return outcome{byName("Branch by role"), call}
		},
		"workspace_path": func(b workerControlBranch) outcome {
			call := map[string]any{"name": "Branch by folder"}
			run(b, call)
			scheduled = byName("Branch by folder")
			return outcome{scheduled, call}
		},
		"run_id": func(b workerControlBranch) outcome {
			worker := h.jobs[""]["worker"]
			session := pebblestore.SessionSnapshot{ID: "worker-execution-branch-run", AccountScopeID: h.home.account, UserID: h.home.user, Metadata: map[string]any{"worker_id": worker, "worker_execution_run_id": "branch-run"}}
			if err := store.CreateSessionForAccount(session, h.home.user, h.home.account); err != nil {
				t.Fatal(err)
			}
			if _, err := workers.RecordWorkerRun(h.home.account, pebblestore.WorkerRunRecord{ID: "branch-run", WorkerID: worker, UserID: h.home.user, Role: pebblestore.WorkerRoleCoder, SessionID: session.ID, Status: "running", RequestSource: "direct", Input: map[string]any{"prompt": "Probe."}, CreatedAt: 1, StartedAt: 1}); err != nil {
				t.Fatal(err)
			}
			call := map[string]any{"worker_id": worker, "run_id": "branch-run"}
			run(b, call)
			if got, ok, err := workers.GetWorkerRun(h.home.account, worker, "branch-run"); err != nil || !ok || got.Status == "running" {
				t.Fatalf("run_id did not cancel the run: %v %v %+v", ok, err, got)
			}
			return outcome{record(worker), call}
		},
		"schedule_id": func(b workerControlBranch) outcome {
			if scheduled.ID == "" {
				t.Fatal("schedule_id needs the folder branch's scheduled worker first (table order)")
			}
			scheduleID := ""
			for _, a := range scheduled.Automations {
				if a.ActivationMode == "interval" {
					scheduleID = a.ID
				}
			}
			current := record(scheduled.ID)
			call := map[string]any{"worker_id": scheduled.ID, "schedule_id": scheduleID, "expected_revision": float64(current.Revision)}
			run(b, call)
			return outcome{record(scheduled.ID), call}
		},
	}
	// agent: the harness's run service has no agent service, so setting a
	// custom agent cannot succeed here. Each branch must still carry agent
	// to the worker write: that write validates it and refuses, naming the
	// missing service, and nothing is created or changed (a dropped agent
	// would succeed instead).
	agentProbes := map[string]map[string]any{
		"worker":         {"worker": h.workers[""]},
		"role":           {"role": "finder", "name": "Agent probe by role", "instructions": "Probe."},
		"workspace_path": {"workspace_path": folder, "name": "Agent probe by folder", "instructions": "Probe."},
	}
	agentReachesTheWrite := func(branch workerControlBranch) bool {
		t.Helper()
		base, ok := agentProbes[branch.triggers[0]]
		if !ok {
			t.Fatalf("branch %v lists agent as written but has no agent probe", branch.triggers)
		}
		call := map[string]any{"action": branch.action, "agent": "branch-agent"}
		for key, value := range base {
			call[key] = value
		}
		before := record(h.workers[""])
		listed, err := workers.ListWorkers(h.home.account, pebblestore.ListWorkersQuery{Limit: 500})
		if err != nil {
			t.Fatal(err)
		}
		_, refused := h.controlTool(t, catalogParityFullKey, "manage_workers", place(call))
		after, err := workers.ListWorkers(h.home.account, pebblestore.ListWorkersQuery{Limit: 500})
		if err != nil {
			t.Fatal(err)
		}
		return strings.Contains(refused, "agent service is not configured") && len(after.Workers) == len(listed.Workers) && record(h.workers[""]).Revision == before.Revision
	}
	// Where each listed write lands.
	verify := map[string]func(o outcome, key string) bool{
		"name":         func(o outcome, key string) bool { return o.worker.Name == o.sent[key] },
		"instructions": func(o outcome, key string) bool { return o.worker.Instructions == o.sent[key] },
		"description":  func(o outcome, key string) bool { return o.worker.Description == o.sent[key] },
		"role":         func(o outcome, key string) bool { return o.worker.Role == o.sent[key] },
		"model": func(o outcome, key string) bool {
			raw, _ := json.Marshal(o.worker.ModelProfile)
			return o.worker.ModelProfile != nil && strings.Contains(string(raw), "gpt-5.5")
		},
		"budget": func(o outcome, key string) bool {
			budget, err := store.GetWorkerBudget(h.home.account, o.worker.ID)
			return err == nil && budget.DailyCostLimitUSD == 4
		},
		"workspace_path": func(o outcome, key string) bool {
			return o.worker.LocalBindings["primary"] != ""
		},
		"project_id": func(o outcome, key string) bool { return o.worker.Metadata["project_id"] == "probe-project" },
		"schedule": func(o outcome, key string) bool {
			for _, a := range o.worker.Automations {
				if a.ActivationMode == "interval" && a.Schedule != nil {
					return true
				}
			}
			return false
		},
		"scheduled_plan": func(o outcome, key string) bool {
			for _, a := range o.worker.Automations {
				if a.ActivationMode == "interval" && a.PlanDocument.Title == "Nightly check" {
					return true
				}
			}
			return false
		},
		"automations": func(o outcome, key string) bool {
			for _, a := range o.worker.Automations {
				if a.Name == "Extra automation" {
					return true
				}
			}
			return false
		},
		"schedule_enabled": func(o outcome, key string) bool {
			for _, a := range o.worker.Automations {
				if a.ID == o.sent["schedule_id"] {
					return !a.Enabled
				}
			}
			return false
		},
	}
	for _, branch := range workerControlRoutes {
		probe, ok := probes[branch.triggers[0]]
		if !ok {
			t.Fatalf("branch %v has no probe here: add one that gives every input it lists as written", branch.triggers)
		}
		o := probe(branch)
		for _, key := range branch.writes {
			if key == "agent" {
				if !agentReachesTheWrite(branch) {
					t.Errorf("branch %v lists agent as written, but the worker write never saw it", branch.triggers)
				}
				continue
			}
			check, ok := verify[key]
			if !ok {
				t.Fatalf("branch %v lists %s as written but this test cannot see where it lands: add a verifier", branch.triggers, key)
			}
			if _, sent := o.sent[key]; !sent {
				t.Fatalf("branch %v: the probe did not send %s", branch.triggers, key)
			}
			if !check(o, key) {
				t.Errorf("branch %v lists %s as written, but the call did not write it (worker %+v)", branch.triggers, key, o.worker)
			}
		}
	}
}
