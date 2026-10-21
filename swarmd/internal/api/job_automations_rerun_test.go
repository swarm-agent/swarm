package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: swarm_deliver_job automation=true re-runs only the failed or
// stopped steps of the job's newest delivery (per automation), and only when
// that delivery's head is still the job's current head (read from the job's
// own swarm_get_job view). Older deliveries are never re-run by it; an
// explicit re-run of one named delivery stays POST /v3/job-deliveries/<id>/rerun.
// Threat (forge run, 2026-10-19): a re-run of one failed review re-ran the
// job's whole delivery history, sending long-fixed findings back into the
// coder's session through a stale reopen and re-reviewing old heads.
// Owners: controlMCPDeliverJob, rerunJobAutomations and
// jobautomation.Store.RerunNewest over a real Pebble store and Runner; only
// the daemon routes the job view reads are stubbed (no model provider runs).
// Layer: the MCP tool call is the narrowest layer that holds the job-head
// lookup and the store selection together.
func TestDeliverJobAutomationRerunsOnlyTheNewestDeliveryAtTheJobsHead(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "rerun.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := jobautomation.NewStore(db)
	server := &Server{}
	server.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: store}))
	account := testPrincipal().AccountScopeID
	folder := t.TempDir()
	auto, _, err := store.Put(account, jobautomation.Automation{Name: "review loop", Scope: jobautomation.Scope{Folder: folder}, Enabled: true, Steps: []jobautomation.Step{
		{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer", Brief: "Review."},
		{ID: "fix", Type: jobautomation.StepReopen, From: "review", MaxRounds: 5, If: []jobautomation.Condition{{Step: "review", Verdict: jobautomation.VerdictBlock}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	jobID := "worker/wkr-1/run-1"
	heads := []string{strings.Repeat("1", 40), strings.Repeat("2", 40), strings.Repeat("3", 40)}
	ids := []string{}
	for i, head := range heads {
		followup := ""
		if i > 0 {
			followup = "followup-" + head[:1]
		}
		d, created, err := store.CreateDelivery(auto, jobautomation.Result{JobID: jobID, Kind: jobautomation.KindWorker, RunID: "run-1", Followup: followup, Folder: folder,
			Status: jobautomation.StatusSucceeded, Git: &jobautomation.ResultGit{Head: head, Commits: []jobautomation.ResultCommit{{SHA: head}}}})
		if err != nil || !created {
			t.Fatal(created, err)
		}
		// Each delivery finished with a failed step: the old ones a stale
		// reopen, the newest a failed review.
		d.CreatedAt, d.State = int64(1000*(i+1)), jobautomation.StateFailed
		d.Steps[0].State, d.Steps[1].State = jobautomation.StateSucceeded, jobautomation.StateFailed
		if i == len(heads)-1 {
			d.Steps[0].State, d.Steps[1].State = jobautomation.StateFailed, jobautomation.StateSkipped
		}
		if err := store.SaveDelivery(d); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, d.ID)
	}

	jobHead := heads[2]
	c := &controlMCPCall{server: server, request: requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)),
		&pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:read", "sessions:write", "automations:read", "automations:write"}})}
	c.next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v3/workers/wkr-1/runs/run-1":
			writeJSON(w, 200, map[string]any{"run": map[string]any{"id": "run-1", "status": "succeeded", "role": "coder", "review": map[string]any{"project_id": "proj-1", "task_id": "task-1"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/projects/proj-1/tasks":
			writeJSON(w, 200, map[string]any{"tasks": []any{map[string]any{"id": "task-1", "session_id": "sess-1", "worktree_branch": "agent/run-1", "base_branch": "main"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/projects/proj-1/tasks/task-1":
			writeJSON(w, 200, map[string]any{"task": map[string]any{"id": "task-1", "status": "needs_review", "workspace_path": folder, "worktree_branch": "agent/run-1",
				"delivery_assessment": map[string]any{"state": "candidate_work", "source_oid": jobHead}}})
		case r.Method == http.MethodPost:
			t.Errorf("a re-run reached a write route: %s %s", r.Method, r.URL.Path)
			writeJSON(w, 500, map[string]any{})
		default:
			writeJSON(w, 200, map[string]any{})
		}
	})

	out, err := controlMCPDeliverJob(c, map[string]any{"job_id": jobID, "automation": true})
	if err != nil {
		t.Fatal(err)
	}
	rerun, _ := controlMCPMap(out)["rerun"].([]map[string]any)
	if len(rerun) != 1 || rerun[0]["delivery_id"] != ids[2] {
		t.Fatalf("re-run = %v, want only the newest delivery %s", rerun, ids[2])
	}
	for i, id := range ids {
		d, err := store.GetDelivery(account, id)
		if err != nil {
			t.Fatal(err)
		}
		if i < 2 && (d.State != jobautomation.StateFailed || d.Reruns != 0 || d.Steps[1].State != jobautomation.StateFailed) {
			t.Fatalf("older delivery %d was re-run: state %s reruns %d steps %+v", i, d.State, d.Reruns, d.Steps)
		}
		if i == 2 && (d.State != jobautomation.StatePending || d.Reruns != 1 || d.Steps[0].State != jobautomation.StatePending) {
			t.Fatalf("newest delivery = state %s reruns %d steps %+v", d.State, d.Reruns, d.Steps)
		}
	}

	// The newest delivery finished failed again, then the job moved on to a
	// head with no delivery yet: nothing is re-run (no old head re-reviewed).
	d, _ := store.GetDelivery(account, ids[2])
	d.State, d.Steps[0].State, d.Steps[1].State = jobautomation.StateFailed, jobautomation.StateFailed, jobautomation.StateSkipped
	if err := store.SaveDelivery(d); err != nil {
		t.Fatal(err)
	}
	jobHead = strings.Repeat("4", 40)
	out, err = controlMCPDeliverJob(c, map[string]any{"job_id": jobID, "automation": true})
	if err != nil {
		t.Fatal(err)
	}
	if rerun, _ := controlMCPMap(out)["rerun"].([]map[string]any); len(rerun) != 0 {
		t.Fatalf("a delivery for an old head was re-run: %v", rerun)
	}
	if next, _ := controlMCPMap(out)["next"].(string); !strings.Contains(next, "head") {
		t.Fatalf("next = %q", next)
	}
	for _, id := range ids {
		if d, _ := store.GetDelivery(account, id); d.State != jobautomation.StateFailed {
			t.Fatalf("delivery %s state %s after a stale re-run", id, d.State)
		}
	}
}
