package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// rebaseRouteRepo is a real repository where main moved after the job's
// branch agent/run-1 forked: base, the job's commit (a.txt), main's tip
// (b.txt). The folder is checked out on main.
func rebaseRouteRepo(t *testing.T) (folder, base, head, tip string) {
	t.Helper()
	folder = t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = folder
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + folder, "GIT_CONFIG_NOSYSTEM=1"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(folder, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write("base.txt", "base\n")
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	base = git("rev-parse", "HEAD")
	git("checkout", "-q", "-b", "agent/run-1")
	write("a.txt", "job\n")
	git("add", "-A")
	git("commit", "-q", "-m", "job: a")
	head = git("rev-parse", "HEAD")
	git("checkout", "-q", "main")
	write("b.txt", "dev moved\n")
	git("add", "-A")
	git("commit", "-q", "-m", "dev: b")
	tip = git("rev-parse", "HEAD")
	return folder, base, head, tip
}

// Requirement: a finished delivery is rebased on demand through POST
// /v3/job-deliveries/{id}/rebase and manage_jobs rebase, with the one
// rebase function every delivery runs (Runner.RebaseDelivery over the
// daemon's routed Git, JobAutomationRebaseGit): a clean replay moves the
// delivery head and the job's branch and is recorded with its mapping and
// range-diff in the delivery view; the second call answers up_to_date
// through the MCP action; a running delivery is refused (409) and nothing
// moves; the workflow view shows the rebase setting.
// Threat: an on-demand path with its own rules (a second rebase), a rebase
// of a delivery the runner is running, or the new fields missing from the
// API views the SDK reads.
// Owners: handleJobDeliveries (rebase route), rebaseJobDelivery,
// controlMCPRebaseJob, putDeliveryRebases, jobAutomationView over a real
// Pebble store and Git repository. Layer: the HTTP handler and the MCP
// call are the surfaces under test.
func TestRebaseDeliveryRouteAndManageJobsRebaseShareOneFunction(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "rebase.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := jobautomation.NewStore(db)
	server := &Server{}
	server.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: store,
		Rebaser: jobautomation.GitRebaser{Git: JobAutomationRebaseGit, Scratch: filepath.Join(t.TempDir(), "workflow-rebase")}}))
	account := testPrincipal().AccountScopeID
	folder, base, head, tip := rebaseRouteRepo(t)
	auto, _, err := store.Put(account, jobautomation.Automation{Name: "land", Scope: jobautomation.Scope{Folder: folder}, Enabled: true, Rebase: jobautomation.RebaseOff,
		Steps: []jobautomation.Step{{ID: "push", Type: jobautomation.StepRun, Command: "push.sh"}}})
	if err != nil {
		t.Fatal(err)
	}
	if view := jobAutomationView(auto); view["rebase"] != jobautomation.RebaseOff || view["rebase_rounds"] != jobautomation.DefaultRebaseRounds {
		t.Fatalf("workflow view rebase fields: %v %v", view["rebase"], view["rebase_rounds"])
	}
	jobID := "worker/wkr-1/run-1"
	d, _, err := store.CreateDelivery(auto, jobautomation.Result{JobID: jobID, Kind: jobautomation.KindWorker, RunID: "run-1", Folder: folder, Status: jobautomation.StatusSucceeded,
		Git: &jobautomation.ResultGit{Branch: "agent/run-1", BaseBranch: "main", BaseCommit: base, Head: head, Commits: []jobautomation.ResultCommit{{SHA: head}}}})
	if err != nil {
		t.Fatal(err)
	}
	d.State = jobautomation.StateRunning
	if err := store.SaveDelivery(d); err != nil {
		t.Fatal(err)
	}
	post := func(body string) *httptest.ResponseRecorder {
		req := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, JobDeliveriesPath+"/"+d.ID+"/rebase", bytes.NewBufferString(body))),
			&pebblestore.ScopedTokenRecord{Scopes: []string{"automations:read", "automations:write"}})
		w := httptest.NewRecorder()
		server.handleJobDeliveries(w, req)
		return w
	}
	// Running: refused, nothing moves.
	if w := post(`{}`); w.Code != http.StatusConflict {
		t.Fatalf("running delivery: %d %s", w.Code, w.Body)
	}
	if got, _ := store.GetDelivery(account, d.ID); got.Head != head || len(got.Rebases) != 0 {
		t.Fatalf("a refused rebase changed the delivery: %+v", got)
	}
	d.State = jobautomation.StateFailed
	if err := store.SaveDelivery(d); err != nil {
		t.Fatal(err)
	}
	if w := post(`{"onto": "-bad"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad onto: %d %s", w.Code, w.Body)
	}

	w := post(`{"onto": "main"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("rebase: %d %s", w.Code, w.Body)
	}
	var answer struct {
		Outcome  string         `json:"outcome"`
		Onto     string         `json:"onto"`
		Rebase   map[string]any `json:"rebase"`
		Delivery map[string]any `json:"delivery"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	stored, _ := store.GetDelivery(account, d.ID)
	if answer.Outcome != jobautomation.RebaseRebased || answer.Onto != tip || stored.Head == head || stored.RebasedFrom != head || stored.State != jobautomation.StateFailed {
		t.Fatalf("answer %+v; stored head %s rebased_from %s state %s", answer, stored.Head, stored.RebasedFrom, stored.State)
	}
	if answer.Rebase["trigger"] != jobautomation.RebaseTriggerRequest || !strings.Contains(answer.Rebase["range_diff"].(string), "job: a") {
		t.Fatalf("rebase record view = %v", answer.Rebase)
	}
	if rebases, _ := answer.Delivery["rebases"].([]any); len(rebases) != 1 || answer.Delivery["target_checked"] != tip {
		t.Fatalf("delivery view = %v", answer.Delivery)
	}
	branch, err := JobAutomationRebaseGit(context.Background(), folder, nil, "rev-parse", "refs/heads/agent/run-1")
	if err != nil || strings.TrimSpace(string(branch)) != stored.Head {
		t.Fatalf("the job's branch is at %s (%v), want %s", branch, err, stored.Head)
	}
	if main, _ := JobAutomationRebaseGit(context.Background(), folder, nil, "rev-parse", "refs/heads/main"); strings.TrimSpace(string(main)) != tip {
		t.Fatalf("the target moved to %s", main)
	}

	// The MCP action rebases the job's newest delivery through the route.
	c := &controlMCPCall{server: server, request: requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)),
		&pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:read", "sessions:write", "automations:read", "automations:write"}})}
	c.next = http.HandlerFunc(server.handleJobDeliveries)
	out, err := controlMCPRebaseJob(c, map[string]any{"job_id": jobID})
	if err != nil {
		t.Fatal(err)
	}
	if view, _ := out.(map[string]any); view["outcome"] != jobautomation.RebaseUpToDate {
		t.Fatalf("second rebase through manage_jobs = %v", out)
	}
	if _, err := controlMCPRebaseJob(c, map[string]any{"job_id": "worker/wkr-1/none"}); err == nil || !strings.Contains(err.Error(), "no workflow delivery") {
		t.Fatalf("a job without a delivery: %v", err)
	}
}
