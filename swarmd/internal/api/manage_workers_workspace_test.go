package api

import (
	"encoding/json"
	"strings"
	"testing"

	runruntime "swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
)

// Purpose:
//   - Requirement: manage_workers request (and test) on a worker that works
//     in several workspaces (no single bound workspace) can name the
//     workspace, as an absolute path or a workspace id, from the
//     Orchestrator, from Swarm Control and on a linked machine; the run is
//     admitted in exactly that workspace. Without it the request is refused
//     with the clear ErrWorkerWorkspaceRequired message that names the
//     workspace field, and a folder that is not a registered workspace is
//     refused; neither admits a run.
//   - Threat/regression: a linked-machine caller could not start a job on a
//     multi-workspace worker at all (no field reached workspace resolution),
//     or a named workspace that is ignored, or one that is not the
//     account's own, admitting a run somewhere else.
//   - Boundary/authority: tool.Runtime.executeManageWorkers (both surfaces)
//     -> api.Server.ManageWorkers / workerCatalogWorkspace
//     (resolveWorkerJobWorkspace) -> run.Service.dispatchWorkerTool ->
//     WorkerExecutionService.Dispatch (ResolveWorkerRunWorkspace,
//     activeWorkspace), and with machine CallFleetWork -> the machine's
//     POST /v3/worker-actions/request. Real stores on two linked in-process
//     daemons. The harness wires no agent/model authorities, so the
//     admitted run's session preparation fails after admission and no agent
//     turn runs; the stored admission record is the postcondition.
//   - Narrowest layer: the field crosses the catalog, the worker service,
//     the run service, admission and the linked route; only routed calls
//     prove the run's pinned workspace.
func TestManageWorkersRequestNamesTheWorkspaceOfAMultiWorkspaceWorker(t *testing.T) {
	h := newCatalogParityHarness(t)
	home := h.home.s.sessions.Store()
	folders := workspace.NewService(pebblestore.NewWorkspaceStore(home.Underlying()))
	h.home.s.workspace = folders
	h.home.s.runner.(*runruntime.Service).SetWorkspaceService(folders)
	h.remote.s.runner.(*runruntime.Service).SetWorkspaceService(h.remote.s.workspace)
	added := map[string]workspace.Resolution{}
	for _, name := range []string{"a", "b"} {
		folder := t.TempDir()
		outboxPinGit(t, folder, "init", "-q", "-b", "dev")
		outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "start")
		entry, err := folders.AddForPrincipal(h.p, folder, "folder-"+name, "", false)
		if err != nil {
			t.Fatal(err)
		}
		added[name] = entry
	}
	// Both folders belong to one project, as a worker's job needs.
	if err := home.PutProject(h.home.account, &pebblestore.ProjectRecord{ID: "two-folder-project", AccountID: h.home.account, Name: "Two folders", Workspaces: []pebblestore.ProjectWorkspaceRef{
		{WorkspaceID: added["a"].WorkspaceID, Path: added["a"].WorkspacePath, Role: "primary_code"},
		{WorkspaceID: added["b"].WorkspaceID, Path: added["b"].WorkspacePath, Role: "primary_code"},
	}}); err != nil {
		t.Fatal(err)
	}
	worker := h.workers[""]
	w, _, _ := home.WorkerStore().GetWorker(h.home.account, worker)
	if len(w.LocalBindings) != 0 || len(w.WorkspaceRequirements) != 0 {
		t.Fatalf("precondition: the worker is bound to one workspace: %+v", w)
	}
	// Only an active worker admits a requested run.
	if active, err := home.WorkerStore().ConfigureWorkerBindings(h.home.account, h.home.user, worker, w.Revision, nil, true); err != nil || active.LifecycleState != pebblestore.WorkerLifecycleStateActive {
		t.Fatalf("activate worker: %+v %v", active, err)
	}
	runs := func(store *pebblestore.SessionStore, account, id string) map[string]pebblestore.WorkerRunRecord {
		t.Helper()
		list, _, err := store.WorkerStore().ListWorkerRuns(account, id, 100, "")
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]pebblestore.WorkerRunRecord{}
		for _, r := range list {
			out[r.ID] = r
		}
		return out
	}
	// admitted runs call and returns the one run it admitted. This harness
	// wires no agent or model authorities, so preparing the admitted run's
	// session fails after admission; that one failure is accepted, any
	// other refusal is not.
	admitted := func(store *pebblestore.SessionStore, account, id string, call func() string) pebblestore.WorkerRunRecord {
		t.Helper()
		before := runs(store, account, id)
		if refusal := call(); refusal != "" && !strings.Contains(refusal, "preparation authorities unavailable") {
			t.Fatalf("request refused: %s", refusal)
		}
		var added []pebblestore.WorkerRunRecord
		for runID, r := range runs(store, account, id) {
			if _, ok := before[runID]; !ok {
				added = append(added, r)
			}
		}
		if len(added) != 1 {
			t.Fatalf("request admitted %d runs, want 1", len(added))
		}
		return added[0]
	}

	// Without workspace: the clear error, naming the field; no run.
	out, err := h.orchestratorTool(t, "manage_workers", map[string]any{"action": "request", "worker_id": worker, "prompt": "Do.", "idempotency_key": "ws-none"})
	refusal := out
	if err != nil {
		refusal = err.Error()
	}
	if !strings.Contains(refusal, "several workspaces") || !strings.Contains(refusal, "workspace: its absolute path or workspace id") {
		t.Fatalf("a request without workspace = %q", refusal)
	}
	// A folder that is not a registered workspace is refused too.
	out, err = h.orchestratorTool(t, "manage_workers", map[string]any{"action": "request", "worker_id": worker, "prompt": "Do.", "workspace": t.TempDir(), "idempotency_key": "ws-unknown"})
	refusal = out
	if err != nil {
		refusal = err.Error()
	}
	if !strings.Contains(refusal, "not a registered workspace") {
		t.Fatalf("a request in an unregistered folder = %q", refusal)
	}
	if n := len(runs(home, h.home.account, worker)); n != 0 {
		t.Fatalf("a refused request admitted %d runs", n)
	}

	// The Orchestrator names the workspace by path: the run is admitted there.
	byPath := admitted(home, h.home.account, worker, func() string {
		if _, err := h.orchestratorTool(t, "manage_workers", map[string]any{"action": "request", "worker_id": worker, "prompt": "Do.", "workspace": added["b"].WorkspacePath, "idempotency_key": "ws-path"}); err != nil {
			return err.Error()
		}
		return ""
	})
	if byPath.WorkspaceID != added["b"].WorkspaceID || byPath.WorkspaceSource != pebblestore.WorkerRunWorkspaceSourceJob {
		t.Fatalf("run admitted in %q (%s), want %q from the job", byPath.WorkspaceID, byPath.WorkspaceSource, added["b"].WorkspaceID)
	}

	// Swarm Control names it by workspace id.
	byID := admitted(home, h.home.account, worker, func() string {
		_, refused := h.controlTool(t, catalogParityFullKey, "manage_workers", map[string]any{"action": "request", "worker_id": worker, "prompt": "Do.", "workspace": added["a"].WorkspaceID, "idempotency_key": "ws-id"})
		return refused
	})
	if byID.WorkspaceID != added["a"].WorkspaceID {
		t.Fatalf("Swarm Control run admitted in %q, want %q", byID.WorkspaceID, added["a"].WorkspaceID)
	}

	// On a linked machine: without workspace the same clear error, with the
	// machine's folder the run is admitted there.
	remoteWorker := h.workers["box"]
	remoteStore := h.remote.s.sessions.Store().WorkerStore()
	rw, _, _ := remoteStore.GetWorker(h.remote.account, remoteWorker)
	if active, err := remoteStore.ConfigureWorkerBindings(h.remote.account, h.remote.user, remoteWorker, rw.Revision, nil, true); err != nil || active.LifecycleState != pebblestore.WorkerLifecycleStateActive {
		t.Fatalf("activate linked worker: %+v %v", active, err)
	}
	project, found, err := h.remote.s.sessions.Store().GetProject(h.remote.account, "remote-project")
	if err != nil || !found || len(project.Workspaces) == 0 {
		t.Fatalf("remote project %+v %v %v", project, found, err)
	}
	_, refused := h.controlTool(t, catalogParityFullKey, "manage_workers", map[string]any{"action": "request", "machine": "box", "worker_id": remoteWorker, "prompt": "Do.", "idempotency_key": "box-none"})
	if !strings.Contains(refused, "several workspaces") {
		t.Fatalf("linked request without workspace = %q", refused)
	}
	if n := len(runs(h.remote.s.sessions.Store(), h.remote.account, remoteWorker)); n != 0 {
		t.Fatalf("a refused linked request admitted %d runs", n)
	}
	linked := admitted(h.remote.s.sessions.Store(), h.remote.account, remoteWorker, func() string {
		_, refused := h.controlTool(t, catalogParityFullKey, "manage_workers", map[string]any{"action": "request", "machine": "box", "worker_id": remoteWorker, "prompt": "Do.", "workspace": project.Workspaces[0].Path, "idempotency_key": "box-path"})
		return refused
	})
	if linked.WorkspaceID != project.Workspaces[0].WorkspaceID {
		t.Fatalf("linked run admitted in %q, want %q", linked.WorkspaceID, project.Workspaces[0].WorkspaceID)
	}
}

// Purpose:
//   - Requirement: manage_jobs retry_now reaches the one retry service
//     (POST /v3/provider-limit-waits/retry -> Server.RetryLimitWaitsNow):
//     with nothing waiting it answers retried 0 with a note (a no-op, not an
//     error); a job id that is not a worker job or a task is refused; a key
//     without sessions:write is refused at the route.
//   - Threat/regression: an MCP action that reshapes or bypasses the route,
//     or a read key making work run again.
//   - Boundary/authority: controlMCPCall.callTool -> Server.ManageJobs ->
//     controlMCPRetryNow -> handleLimitRetry on the parity harness daemon.
//   - Narrowest layer: the catalog call, the scope gate and the route are
//     only proven together through Swarm Control's tools/call.
func TestManageJobsRetryNowRunsTheRetryRoute(t *testing.T) {
	h := newCatalogParityHarness(t)
	out, refused := h.controlTool(t, catalogParityFullKey, "manage_jobs", map[string]any{"action": "retry_now"})
	if refused != "" {
		t.Fatalf("retry_now refused: %s", refused)
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("envelope %s: %v", out, err)
	}
	if envelope.Data["retried"] != float64(0) || envelope.Data["note"] != "nothing waits on a provider's limit" {
		t.Fatalf("retry_now with nothing waiting = %v", envelope.Data)
	}
	if _, refused := h.controlTool(t, catalogParityFullKey, "manage_jobs", map[string]any{"action": "retry_now", "job_id": "media/p/t"}); !strings.Contains(refused, "job_id must be a worker job") {
		t.Fatalf("retry_now with a media job = %q", refused)
	}
	readOnly := []string{"swarm:read", "swarm:write", "sessions:read", "automations:read", "signals:read"}
	if _, refused := h.controlTool(t, readOnly, "manage_jobs", map[string]any{"action": "retry_now"}); refused == "" {
		t.Fatal("retry_now ran for a key without sessions:write")
	}
}
