package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/executioncapacity"
	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// blockingScript is a run step executor whose script runs until released.
type blockingScript struct {
	started chan struct{}
	release chan struct{}
}

func (b *blockingScript) Run(ctx context.Context, _ jobautomation.RunRequest) (jobautomation.RunOutcome, error) {
	close(b.started)
	select {
	case <-b.release:
		return jobautomation.RunOutcome{Output: []byte("checked\n")}, nil
	case <-ctx.Done():
		return jobautomation.RunOutcome{}, ctx.Err()
	}
}

type oneResult struct{ result jobautomation.Result }

func (o oneResult) Result(context.Context, pebblestore.Signal, bool) (*jobautomation.Result, error) {
	copy := o.result
	return &copy, nil
}

func getBoxDrain(t *testing.T, server *Server) executioncapacity.DrainStatus {
	t.Helper()
	w := httptest.NewRecorder()
	server.handleBoxDrain(w, withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v3/box/drain", nil)))
	if w.Code != http.StatusOK {
		t.Fatalf("drain status %d: %s", w.Code, w.Body.String())
	}
	var out executioncapacity.DrainStatus
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode drain: %v", err)
	}
	return out
}

// Requirement: GET /v3/box/busy (the installer's "may Swarm restart now?"
// check for an automatic update) counts a workflow run step whose script
// runs now (workflow_steps): busy, not restart_resumable, and an if_idle
// drain is refused. A drain someone asked for is not ready while one runs
// (GET /v3/box/drain workflow_steps), so the installer does not claim the
// restart over it. Once the script ends the machine is idle again and the
// drain is ready.
// Threat (seen on a forge: a gate killed with exit 143 shortly before an
// automatic update installed): a restart killing a running gate or push
// because nothing counted it, so the step is reported as its own failure
// and its output is lost.
// Owners: Server.boxBusyFrom and Server.addDrainBuildBlockers over
// jobautomation.Runner.RunningScripts, with the real capacity manager.
// Layer: the HTTP handlers the installer calls, over a real runner running
// a real delivery whose step blocks until released.
func TestBoxBusyAndDrainCountRunningWorkflowStepScripts(t *testing.T) {
	server, _, permissionSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "automations.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	account := testPrincipal().AccountScopeID
	folder := t.TempDir()
	feed := pebblestore.NewSignalStore(db)
	automations := jobautomation.NewStore(db)
	script := &blockingScript{started: make(chan struct{}), release: make(chan struct{})}
	runner := jobautomation.NewRunner(jobautomation.Options{Store: automations, Feed: feed, Account: func() string { return account },
		Results: oneResult{result: jobautomation.Result{V: jobautomation.ResultVersion, JobID: "worker/coder-1/run-1", Kind: jobautomation.KindWorker,
			RunID: "run-1", Folder: folder, Status: jobautomation.StatusSucceeded}},
		Run: script})
	server.SetJobAutomations(runner)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := runner.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := automations.Put(account, jobautomation.Automation{Name: "gate", Scope: jobautomation.Scope{Folder: folder}, Enabled: true,
		Steps: []jobautomation.Step{{ID: "gate", Type: jobautomation.StepRun, Command: "deliver/gate.sh"}}}); err != nil {
		t.Fatal(err)
	}
	if idle := getBoxBusy(t, server); idle.Busy || idle.WorkflowSteps != 0 || !idle.RestartResumable {
		t.Fatalf("before the step: %+v", idle)
	}
	if _, err := feed.Append(pebblestore.Signal{Kind: "worker.run.succeeded", Severity: pebblestore.SignalSeverityInfo, Source: pebblestore.SignalSourceSwarmd,
		Account: account, Summary: "Worker run succeeded", DedupKey: "worker.run:run-1:", Refs: map[string]string{"worker_id": "coder-1", "run_id": "run-1"}}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- runner.ProcessOnce(ctx) }()
	select {
	case <-script.started:
	case <-ctx.Done():
		t.Fatal("the step's script never started")
	}

	busy := getBoxBusy(t, server)
	if !busy.Busy || busy.WorkflowSteps != 1 || busy.RestartResumable {
		t.Fatalf("busy while a gate runs = %+v, want busy, workflow_steps 1, not restart_resumable", busy)
	}
	if w := postBoxDrain(t, server, `{"max_seconds":60,"if_idle":true}`); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"workflow_steps":1`) {
		t.Fatalf("if_idle drain while a gate runs: %d %s", w.Code, w.Body.String())
	}
	if permissionSvc.ExecutionCapacity().DrainStatus().Draining {
		t.Fatal("a refused if_idle drain fenced admission")
	}
	if w := postBoxDrain(t, server, `{"max_seconds":60}`); w.Code != http.StatusOK {
		t.Fatalf("requested drain: %d %s", w.Code, w.Body.String())
	}
	if status := getBoxDrain(t, server); status.Ready || status.WorkflowSteps != 1 {
		t.Fatalf("drain while a gate runs = %+v, want not ready, workflow_steps 1", status)
	}

	close(script.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if idle := getBoxBusy(t, server); idle.Busy || idle.WorkflowSteps != 0 || !idle.RestartResumable {
		t.Fatalf("after the step: %+v", idle)
	}
	if status := getBoxDrain(t, server); !status.Ready || status.WorkflowSteps != 0 {
		t.Fatalf("drain after the gate = %+v, want ready", status)
	}
	permissionSvc.ExecutionCapacity().CancelDrain("")
	list, err := automations.JobDeliveries(account, "worker/coder-1/run-1")
	if err != nil || len(list) != 1 || list[0].State != jobautomation.StateSucceeded {
		t.Fatalf("delivery = %+v %v", list, err)
	}
}
