package jobautomation

import (
	"errors"
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func openTestStore(t *testing.T) (*pebblestore.Store, *Store) {
	t.Helper()
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "jobautomation.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, NewStore(db)
}

func testAutomation(folder string) Automation {
	return Automation{Name: "push", Scope: Scope{Folder: folder}, When: WhenSucceeded, Enabled: true,
		Steps: []Step{{ID: "push", Type: StepRun, Command: "deliver/push.sh"}}}
}

// Requirement: automation definitions are durable, account-owned and
// revisioned: an update by name keeps the id and raises the revision; an
// invalid definition is refused before anything is written; another account
// neither sees nor deletes it.
// Threat: a silent overwrite without a revision change (a running delivery
// could not tell which steps it started with), a definition that escapes the
// folder (a command with ..), or one account reading another's automations.
// Owner: Store.Put / Get / List / Delete and Automation.Normalize; the store
// over a real Pebble database is the narrowest layer.
func TestDefinitionsAreRevisionedValidatedAndAccountScoped(t *testing.T) {
	_, store := openTestStore(t)
	first, created, err := store.Put("acct-a", testAutomation("/srv/site"))
	if err != nil || !created || first.Revision != 1 || first.ID == "" {
		t.Fatalf("create = %+v %v %v", first, created, err)
	}
	if first.Steps[0].TimeoutSeconds != DefaultStepTimeoutSeconds || first.Retries != 0 {
		t.Fatalf("defaults not applied: %+v", first)
	}
	update := testAutomation("/srv/site")
	update.Retries = 2
	second, created, err := store.Put("acct-a", update)
	if err != nil || created || second.ID != first.ID || second.Revision != 2 || second.Retries != 2 {
		t.Fatalf("update = %+v %v %v", second, created, err)
	}
	for name, bad := range map[string]Automation{
		"escaping command": {Name: "x", Scope: Scope{Folder: "/srv/site"}, Steps: []Step{{Type: StepRun, Command: "../outside.sh"}}},
		"command in .git":  {Name: "x", Scope: Scope{Folder: "/srv/site"}, Steps: []Step{{Type: StepRun, Command: "/srv/site/.git/hooks/push"}}},
		"step secrets":     {Name: "x", Scope: Scope{Folder: "/srv/site"}, Steps: []Step{{Type: StepRun, Command: "a.sh", Secrets: []byte(`["T"]`)}}},
		"relative folder":  {Name: "x", Scope: Scope{Folder: "site"}, Steps: []Step{{Type: StepRun, Command: "a.sh"}}},
		"retries":          {Name: "x", Scope: Scope{Folder: "/srv/site"}, Retries: 4, Steps: []Step{{Type: StepRun, Command: "a.sh"}}},
		"timeout":          {Name: "x", Scope: Scope{Folder: "/srv/site"}, Steps: []Step{{Type: StepRun, Command: "a.sh", TimeoutSeconds: 3601}}},
		"no steps":         {Name: "x", Scope: Scope{Folder: "/srv/site"}},
		"agent no brief":   {Name: "x", Scope: Scope{Folder: "/srv/site"}, Steps: []Step{{Type: StepAgent, Worker: "reviewer"}}},
		"when":             {Name: "x", Scope: Scope{Folder: "/srv/site"}, When: "sometimes", Steps: []Step{{Type: StepRun, Command: "a.sh"}}},
		"duplicate ids":    {Name: "x", Scope: Scope{Folder: "/srv/site"}, Steps: []Step{{ID: "a", Type: StepRun, Command: "a.sh"}, {ID: "a", Type: StepRun, Command: "b.sh"}}},
	} {
		if _, _, err := store.Put("acct-a", bad); !IsValidationError(err) {
			t.Fatalf("%s: err = %v, want a validation error", name, err)
		}
	}
	if list, _ := store.List("acct-a"); len(list) != 1 {
		t.Fatalf("refused definitions were stored: %d", len(list))
	}
	if list, _ := store.List("acct-b"); len(list) != 0 {
		t.Fatalf("account b sees %d automations", len(list))
	}
	if _, err := store.Get("acct-b", first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-account get = %v", err)
	}
	if err := store.Delete("acct-b", first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-account delete = %v", err)
	}
	if err := store.Delete("acct-a", first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("acct-a", first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted automation still readable: %v", err)
	}
}

// Requirement: one (job end, automation revision) pair is one delivery with
// a stable id; recording it again changes nothing, and a follow-up of the
// same job is a new delivery.
// Threat: a replayed signal or restart delivering the same job end twice.
// Owner: Store.CreateDelivery and DeliveryID.
func TestDeliveryIsCreatedOncePerJobEndAndFollowup(t *testing.T) {
	_, store := openTestStore(t)
	a, _, err := store.Put("acct-a", testAutomation("/srv/site"))
	if err != nil {
		t.Fatal(err)
	}
	result := Result{JobID: "worker/w1/r1", Folder: "/srv/site", Status: StatusSucceeded}
	first, created, err := store.CreateDelivery(a, result)
	if err != nil || !created {
		t.Fatal(created, err)
	}
	again, created, err := store.CreateDelivery(a, result)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("second create = %v %v %v", again.ID, created, err)
	}
	result.Followup = "run-followup-1"
	followup, created, err := store.CreateDelivery(a, result)
	if err != nil || !created || followup.ID == first.ID {
		t.Fatalf("follow-up delivery = %v %v %v", followup.ID, created, err)
	}
	list, err := store.JobDeliveries("acct-a", "worker/w1/r1")
	if err != nil || len(list) != 2 {
		t.Fatalf("job deliveries = %d %v", len(list), err)
	}
	if first.Round != 0 {
		t.Fatalf("round = %d", first.Round)
	}
}

// Requirement: an explicit re-run runs only the failed or stopped steps (and
// the steps after them that never ran); a succeeded step is never re-run and
// a finished successful delivery has nothing to re-run.
// Owner: Store.Rerun.
func TestRerunResetsOnlyFailedOrStoppedSteps(t *testing.T) {
	_, store := openTestStore(t)
	auto := testAutomation("/srv/site")
	auto.Steps = []Step{{ID: "a", Type: StepRun, Command: "a.sh"}, {ID: "b", Type: StepRun, Command: "b.sh"}, {ID: "c", Type: StepRun, Command: "c.sh"}}
	a, _, err := store.Put("acct-a", auto)
	if err != nil {
		t.Fatal(err)
	}
	d, _, err := store.CreateDelivery(a, Result{JobID: "worker/w1/r1", Folder: "/srv/site"})
	if err != nil {
		t.Fatal(err)
	}
	d.State = StateFailed
	d.Steps[0].State, d.Steps[1].State, d.Steps[2].State = StateSucceeded, StateFailed, StateSkipped
	if err := store.SaveDelivery(d); err != nil {
		t.Fatal(err)
	}
	out, err := store.Rerun("acct-a", d.ID)
	if err != nil || !out.Rerun {
		t.Fatal(out.Rerun, err)
	}
	re := out.Delivery
	if re.State != StatePending || re.Steps[0].State != StateSucceeded || re.Steps[1].State != StatePending || re.Steps[2].State != StatePending {
		t.Fatalf("rerun states = %s %s %s %s", re.State, re.Steps[0].State, re.Steps[1].State, re.Steps[2].State)
	}
	re.State = StateSucceeded
	for i := range re.Steps {
		re.Steps[i].State = StateSucceeded
	}
	if err := store.SaveDelivery(re); err != nil {
		t.Fatal(err)
	}
	if out, err := store.Rerun("acct-a", d.ID); err != nil || out.Rerun || out.Reason != ReasonNothingToRerun {
		t.Fatalf("rerun of a succeeded delivery = %+v %v", out, err)
	}
	if _, err := store.Rerun("acct-b", d.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-account rerun = %v", err)
	}
}
