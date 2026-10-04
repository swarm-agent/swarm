package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: every new headless route retains authenticated membership,
// scope and agent-origin checks. Threat: raw HTTP bypass of Orchestrator-only
// authority or cross-account mutation. Real apiMux plus temporary Pebble is the
// narrowest layer proving rejected requests leave context/placement unchanged.
func TestWorkerControlRouteAuthorization(t *testing.T) {
	_, db, h := setupWorkerAPITestServer(t)
	ws := store.NewWorkerStore(db)
	worker, err := ws.CreateWorker("acct-test", "user-test", store.CreateWorkerRequest{Name: "control"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	routes := []struct{ method, path string }{
		{"GET", "context"}, {"PUT", "context"}, {"POST", "ssh-targets"}, {"POST", "gcp-targets"}, {"POST", "target-reference"}, {"GET", "deployments"}, {"POST", "deployments"},
		{"GET", "deployments/deployment-1"}, {"POST", "deployments/deployment-1/approve"}, {"GET", "deployments/deployment-1/commands"}, {"POST", "deployments/deployment-1/commands"}, {"POST", "deployments/deployment-1/commands/cmd-1/ack"}, {"POST", "deployments/deployment-1/jobs"},
	}
	for _, route := range routes {
		t.Run(route.method+route.path, func(t *testing.T) {
			path := "/" + worker.ID + "/" + route.path
			restricted := executeWorkerAPI(h, route.method, path, "{}", workerAPICallOptions{scopedToken: &store.ScopedTokenRecord{AccountScopeID: "acct-test", UserID: "user-test", WorkerID: worker.ID, Scopes: []string{"automations:*"}}})
			if restricted.Code != http.StatusForbidden {
				t.Fatalf("worker credential escalation: %d", restricted.Code)
			}
			agent := executeWorkerAPI(h, route.method, path, "{}", workerAPICallOptions{agentOrigin: true})
			if agent.Code != http.StatusForbidden {
				t.Fatalf("agent bypass: %d %s", agent.Code, agent.Body.String())
			}
			foreign := executeWorkerAPI(h, route.method, path, "{}", workerAPICallOptions{account: "acct-2", user: "user-2"})
			if foreign.Code != http.StatusNotFound {
				t.Fatalf("foreign account: %d %s", foreign.Code, foreign.Body.String())
			}
			scoped := executeWorkerAPI(h, route.method, path, "{}", workerAPICallOptions{scopes: []string{"sessions:read"}})
			if scoped.Code != http.StatusForbidden {
				t.Fatalf("scope bypass: %d %s", scoped.Code, scoped.Body.String())
			}
		})
	}
	c, err := ws.GetWorkerContext("acct-test", worker.ID, 0)
	if err != nil || c.Revision != 0 {
		t.Fatalf("unauthorized context mutation: %+v %v", c, err)
	}
	ds, err := ws.ListWorkerDeployments("acct-test", worker.ID)
	if err != nil || len(ds) != 0 {
		t.Fatalf("unauthorized deployment: %+v %v", ds, err)
	}
}

// Requirement: the full API uses real service/store intent, not fake runtime
// success. Threat: stale approval, transport drift, local fallback or lost job
// pins. The existing hermetic execution fixture has no provider/network worker.
func TestWorkerControlAPIIntent(t *testing.T) {
	s, db, h := setupWorkerAPITestServer(t)
	workspace := setupWorkerAPIExecution(t, s, db)
	ws := store.NewWorkerStore(db)
	worker, err := ws.CreateWorker("acct-test", "user-test", store.CreateWorkerRequest{Name: "control", WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	worker = activateWorkerAPIFixture(t, s, worker, workspace)
	// Canonical topology is the only source of GCP target identity.
	_, err = store.NewTopologyStore(db).PutRuntimeForAccount("acct-test", store.TopologyRuntimeRecord{SwarmID: "target-gcp", Name: "fixture", Transport: "gcp", AccountScopeID: "acct-test", UserID: "user-test"})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path string, body any, want int) map[string]json.RawMessage {
		t.Helper()
		raw, _ := json.Marshal(body)
		res := executeWorkerAPI(h, method, "/"+worker.ID+"/"+path, string(raw), workerAPICallOptions{})
		if res.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, res.Code, res.Body.String())
		}
		var out map[string]json.RawMessage
		if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	call("PUT", "context", map[string]any{"expected_revision": 0, "text": "knowledge", "provenance": "explicit user"}, 200)
	for i := 0; i < 10; i++ {
		registration := store.WorkerSSHRegistration{WorkspaceID: workspace, Name: fmt.Sprintf("target-%d", i), Host: fmt.Sprintf("host-%d.example.invalid", i), User: "fixture", Port: 22, IdempotencyKey: fmt.Sprintf("target-%d", i)}
		first := call("POST", "ssh-targets", registration, 201)
		retry := call("POST", "ssh-targets", registration, 201)
		if string(first["target"]) != string(retry["target"]) {
			t.Fatal("target registration duplicated")
		}
		registration.Host = "different.example.invalid"
		call("POST", "ssh-targets", registration, 409)
	}
	target := store.WorkerTargetReference{Kind: "gcp", ReferenceID: "target-gcp", Capacity: 1}
	call("POST", "target-reference", target, 200)
	out := call("POST", "deployments", store.WorkerDeploymentRequest{WorkerRevision: worker.Revision, ContextRevision: 1, Target: target, Lifecycle: "persistent", IdempotencyKey: "dep-key"}, 201)
	var d store.WorkerDeploymentRecord
	if err = json.Unmarshal(out["deployment"], &d); err != nil {
		t.Fatal(err)
	}
	call("POST", "deployments/"+d.ID+"/approve", map[string]any{"expected_revision": d.Revision + 1, "approval_digest": d.ApprovalDigest}, 409)
	out = call("POST", "deployments/"+d.ID+"/approve", map[string]any{"expected_revision": d.Revision, "approval_digest": d.ApprovalDigest}, 200)
	if err = json.Unmarshal(out["deployment"], &d); err != nil {
		t.Fatal(err)
	}
	call("POST", "deployments/"+d.ID+"/commands", store.WorkerCommandRequest{ExpectedRevision: d.Revision, Generation: d.Generation, Kind: "start", IdempotencyKey: "start-key"}, 503)
	body := map[string]any{"worker_revision": worker.Revision, "deployment_revision": d.Revision, "context_revision": 1, "idempotency_key": "job-key", "input": map[string]any{"prompt": "review"}}
	out = call("POST", "deployments/"+d.ID+"/jobs", body, 202)
	var r store.WorkerRunRecord
	if err = json.Unmarshal(out["run"], &r); err != nil {
		t.Fatal(err)
	}
	if r.Placement == nil || r.Placement.State != "pending_adapter" || r.ModelProfile == nil || string(out["execution_available"]) != "false" {
		t.Fatalf("unpinned or fake execution: %+v", r)
	}
	if _, ok, err := s.sessions.GetSession(r.SessionID); err != nil || ok {
		t.Fatalf("local execution session allocated: %v %v", ok, err)
	}
	execution, _ := s.workerExecutionService()
	if err = execution.Start(t.Context(), r); err != store.ErrWorkerRemoteUnavailable {
		t.Fatalf("local fallback: %v", err)
	}
	call("GET", "deployments", nil, 200)
	call("GET", "deployments/"+d.ID, nil, 200)
	call("GET", "context?revision=1", nil, 200)
	gcpReg := store.WorkerGCPRegistration{Name: "gcp-api-target", RuntimeID: "target-gcp-api", IdempotencyKey: "gcp-idemp-1"}
	call("POST", "gcp-targets", gcpReg, 201)
	cmdOut := call("POST", "deployments/"+d.ID+"/commands", store.WorkerCommandRequest{ExpectedRevision: d.Revision, Generation: d.Generation, Kind: "stop", IdempotencyKey: "stop-key"}, 202)
	var cmd store.WorkerCommandRecord
	if err = json.Unmarshal(cmdOut["command"], &cmd); err != nil {
		t.Fatal(err)
	}
	ackDigest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	call("POST", "deployments/"+d.ID+"/commands/"+cmd.ID+"/ack", store.WorkerCommandAcknowledgement{Generation: d.Generation, Status: "acknowledged", EvidenceDigest: ackDigest}, 200)
	call("GET", "deployments/"+d.ID+"/commands", nil, 200)
	call("POST", fmt.Sprintf("runs/%s/cancel", r.ID), map[string]any{}, 200)
	latest, _, err := ws.GetWorkerRun("acct-test", worker.ID, r.ID)
	if err != nil || latest.Status != "cancelled" || latest.Placement.State != "cancelled_before_dispatch" {
		t.Fatalf("cancel not settled: %+v %v", latest, err)
	}
	call("PUT", "context", map[string]any{"expected_revision": 1, "text": "next knowledge", "provenance": "explicit user"}, 200)
}
