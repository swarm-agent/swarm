package jobautomation

import (
	"context"
	"strings"
	"testing"
)

// validatingAgents is recordingAgents behind the same check the daemon's
// built-in agent start (api startBuiltinAgentStep) applies to every request:
// the step it was given must pass NormalizeSteps, else the start is refused.
// refuse, when set, refuses every start (a daemon whose run-time path still
// rejected legacy steps before this fix).
type validatingAgents struct {
	*recordingAgents
	refuse bool
}

func (a *validatingAgents) Start(ctx context.Context, req AgentRequest) (string, error) {
	if a.refuse {
		return "", &RefusedError{Reason: "refused before the fix"}
	}
	if err := NormalizeSteps([]Step{{ID: "agent", Type: StepAgent, Worker: req.Worker, Brief: req.Brief, Fixes: req.Fixes, Checkout: req.Checkout}}); err != nil {
		return "", &RefusedError{Reason: err.Error()}
	}
	return a.recordingAgents.Start(ctx, req)
}

// storeLegacyRevision saves a valid workflow, then rewrites its stored
// definition the way a revision saved before 12ba4a681 looks (an agent
// step with the given worker and checkout), which a save refuses today.
func storeLegacyRevision(t *testing.T, h *harness, folder, worker, checkout string) Automation {
	t.Helper()
	saved := h.automation(Automation{Name: "after merge", Scope: Scope{Folder: folder, Worker: "coder-1"}, When: WhenSucceeded, Enabled: true, Steps: []Step{
		{ID: "script", Type: StepAgent, Worker: worker, Checkout: CheckoutNew, Brief: "Run deliver/after-merge.sh in the folder."},
	}})
	var stored Automation
	if found, err := h.store.db.GetJSON(definitionKey(testAccount, saved.ID), &stored); err != nil || !found {
		t.Fatalf("read stored definition: found=%v err=%v", found, err)
	}
	stored.Steps[0].Checkout = checkout
	if err := h.store.db.PutJSON(definitionKey(testAccount, saved.ID), stored); err != nil {
		t.Fatal(err)
	}
	return stored
}

// Requirement: a workflow revision stored before 12ba4a681 with an agent
// step {worker: swarm, checkout: none} still runs: the stored revision is
// loaded to run the step with checkout new (the only checkout swarm and
// coder have now), and the step's run records a note saying so.
// Threat: the regression this fixes, a saved workflow that worked failing
// at run time with "checkout must be new" after a daemon update, with no
// warning; or a migration that runs silently, so the owner never resaves.
// Owners: Runner.runStep (LegacyCheckoutMigration on the delivery's pinned
// Definition) and StepRecord.Note over the real Pebble store; the agent
// start is stubbed behind the same NormalizeSteps check the daemon's
// built-in agent start applies, the narrowest layer that shows the step
// would otherwise be refused.
func TestLegacySwarmCheckoutNoneRunsWithCheckoutNewAndRecordsTheNote(t *testing.T) {
	for _, tc := range []struct{ worker, checkout string }{
		{"swarm", CheckoutNone}, {"coder", CheckoutNone}, {"swarm", CheckoutSame}, {"Swarm", " None "},
	} {
		t.Run(tc.worker+"/"+tc.checkout, func(t *testing.T) {
			h := newHarness(t)
			agents := &validatingAgents{recordingAgents: h.agents}
			h.runner.agent = agents
			folder := t.TempDir()
			h.process()
			storeLegacyRevision(t, h, folder, tc.worker, tc.checkout)
			h.results.results["run-1"] = workerResult(folder, "run-1")
			h.jobEnd("run-1", "", "succeeded", "direct")
			h.process()

			if len(h.agents.started) != 1 || h.agents.started[0].Checkout != CheckoutNew {
				t.Fatalf("started = %+v, want one start with checkout new", h.agents.started)
			}
			list := h.deliveries("worker/coder-1/run-1")
			if len(list) != 1 {
				t.Fatalf("deliveries = %+v", list)
			}
			d := list[0]
			step := d.Steps[0]
			if d.State != StateSucceeded || step.State != StateSucceeded || step.Reason != "" {
				t.Fatalf("delivery %s step %+v, want succeeded", d.State, step)
			}
			want := LegacyCheckoutNote(strings.ToLower(strings.TrimSpace(tc.checkout)))
			if step.Note != want || !strings.Contains(step.Note, "migrated to new") {
				t.Fatalf("note = %q, want %q", step.Note, want)
			}
			// The pinned definition is the stored revision, not rewritten.
			if d.Definition[0].Checkout != tc.checkout {
				t.Fatalf("pinned definition checkout = %q, want the stored %q", d.Definition[0].Checkout, tc.checkout)
			}
		})
	}
}

// Requirement: a delivery pinned to a legacy revision that failed is rerun
// (POST /v3/job-deliveries/{id}/rerun, Store.Rerun) and runs instead of
// failing again, with the same pinned revision even after the owner saved a
// fixed one.
// Threat: a pinned delivery that can never succeed because its rerun reuses
// the legacy revision and fails it the same way.
// Owners: Store.Rerun and Runner.runStep over the real Pebble store; the
// first attempt is refused by the stub as the daemon before the fix did.
func TestRerunOfDeliveryPinnedToLegacyRevisionRuns(t *testing.T) {
	h := newHarness(t)
	agents := &validatingAgents{recordingAgents: h.agents, refuse: true}
	h.runner.agent = agents
	folder := t.TempDir()
	h.process()
	legacy := storeLegacyRevision(t, h, folder, "swarm", CheckoutNone)
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()
	failed := h.deliveries("worker/coder-1/run-1")
	if len(failed) != 1 || failed[0].State != StateFailed {
		t.Fatalf("first delivery = %+v, want failed", failed)
	}

	// The owner saves a fixed revision; the failed delivery stays pinned.
	fixed := h.automation(Automation{Name: "after merge", Scope: Scope{Folder: folder, Worker: "coder-1"}, When: WhenSucceeded, Enabled: true, Steps: []Step{
		{ID: "script", Type: StepAgent, Worker: "swarm", Checkout: CheckoutNew, Brief: "Run deliver/after-merge.sh in the folder (fixed)."},
	}})
	if fixed.Revision == legacy.Revision {
		t.Fatalf("fixed revision %d is the legacy revision", fixed.Revision)
	}

	agents.refuse = false
	re, ok, err := h.store.Rerun(testAccount, failed[0].ID)
	if err != nil || !ok {
		t.Fatalf("rerun: ok=%v err=%v", ok, err)
	}
	if re.AutomationRevision != legacy.Revision || re.Definition[0].Checkout != CheckoutNone {
		t.Fatalf("rerun pinned revision %d checkout %q, want %d none", re.AutomationRevision, re.Definition[0].Checkout, legacy.Revision)
	}
	h.process()

	after := h.deliveries("worker/coder-1/run-1")
	if len(after) != 1 || after[0].ID != failed[0].ID {
		t.Fatalf("deliveries after rerun = %+v", after)
	}
	d := after[0]
	if d.State != StateSucceeded || d.Steps[0].State != StateSucceeded || d.Steps[0].Note != LegacyCheckoutNote(CheckoutNone) {
		t.Fatalf("rerun delivery %s step %+v, want succeeded with the note", d.State, d.Steps[0])
	}
	if n := len(h.agents.started); n != 1 || h.agents.started[0].Checkout != CheckoutNew || !strings.Contains(h.agents.started[0].Brief, "after-merge.sh in the folder.") {
		t.Fatalf("started = %+v, want the pinned step once with checkout new", h.agents.started)
	}
}

// Requirement: saving stays strict: a swarm or coder agent step with
// checkout none or same is refused with today's message, and the finder,
// designer, reviewer and custom-worker checkout rules are unchanged; the
// run-time migration touches only swarm and coder with none or same.
// Threat: the migration widening into a save path (new legacy definitions
// accepted) or rewriting another agent's checkout.
// Owners: Store.Put (Normalize, normalizeAgent) and LegacyCheckoutMigration.
func TestLegacyCheckoutStaysRefusedOnSaveAndOtherAgentsAreUnchanged(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	for _, tc := range []struct{ worker, checkout string }{{"swarm", CheckoutNone}, {"swarm", CheckoutSame}, {"coder", CheckoutNone}, {"coder", CheckoutSame}} {
		_, _, err := h.store.Put(testAccount, Automation{Name: "legacy " + tc.worker + " " + tc.checkout, Scope: Scope{Folder: folder}, Enabled: true, Steps: []Step{
			{ID: "s", Type: StepAgent, Worker: tc.worker, Checkout: tc.checkout, Brief: "b"},
		}})
		want := "step s: " + tc.worker + " changes files, so it works in its own worktree: checkout must be new"
		if !IsValidationError(err) || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s/%s save err = %v, want %q", tc.worker, tc.checkout, err, want)
		}
	}
	if list, err := h.store.List(testAccount); err != nil || len(list) != 0 {
		t.Fatalf("refused saves stored %d automations (err %v)", len(list), err)
	}

	unchanged := []Step{
		{ID: "a", Type: StepAgent, Worker: "finder", Checkout: CheckoutNone, Brief: "b"},
		{ID: "b", Type: StepAgent, Worker: "designer", Checkout: CheckoutNone, Brief: "b"},
		{ID: "c", Type: StepAgent, Worker: "reviewer", Checkout: CheckoutSame, Brief: "b"},
		{ID: "d", Type: StepAgent, Worker: "reviewer", Checkout: CheckoutNew, Fixes: FixesSecurity, Brief: "b"},
		{ID: "e", Type: StepAgent, Worker: "wkr_1", Checkout: CheckoutSame, Brief: "b"},
		{ID: "f", Type: StepAgent, Worker: "swarm", Checkout: CheckoutNew, Brief: "b"},
		{ID: "g", Type: StepAgent, Worker: "coder", Brief: "b"},
		{ID: "h", Type: StepAgent, Worker: "finder", Checkout: CheckoutSame, Brief: "b"},
		{ID: "i", Type: StepAgent, Worker: "wkr_1", Checkout: CheckoutNone, Brief: "b"},
	}
	for _, step := range unchanged {
		got, note := LegacyCheckoutMigration(step)
		if note != "" || got.Checkout != step.Checkout {
			t.Fatalf("%s/%s migrated to %q (%q)", step.Worker, step.Checkout, got.Checkout, note)
		}
	}
	// Their save rules are unchanged: finder in a worktree and a custom
	// worker in the folder are still refused.
	for _, step := range unchanged[7:] {
		if err := NormalizeSteps([]Step{step}); !IsValidationError(err) {
			t.Fatalf("%s/%s save err = %v, want refused", step.Worker, step.Checkout, err)
		}
	}
	if err := NormalizeSteps(append([]Step(nil), unchanged[:7]...)); err != nil {
		t.Fatalf("valid steps refused: %v", err)
	}
}
