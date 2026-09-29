package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: Human-only POST /v3/workers/{id}/accept endpoint verification.
// Requirement 1: Authenticated user with automations:write accepts a pending worker with exact revision.
// Requirement 2: Transitions worker to active lifecycle state with approved primary workspace binding.
// Requirement 3: Stale expected_revision and non-pending workers reject with 409 Conflict.
// Requirement 4: Cross-account callers receive 404 Not Found without leaking existence.
// Requirement 5: Non-user principals (AI agents, subagents) and scoped trigger tokens receive 403 Forbidden.
// Requirement 6: GET /v3/workers?lifecycle_state=pending returns pending workers for inspection.
// Threat: AI agents or unauthorized clients self-approve or activate pending worker proposals.

func TestWorkerAPI_AcceptPendingWorker(t *testing.T) {
	s, db, h := setupWorkerAPITestServer(t)
	workspaceID := setupWorkerAPIExecution(t, s, db)

	// Create pending worker
	w, err := s.sessions.CreateWorker(context.Background(), "acct-test", "user-test", store.CreateWorkerRequest{
		Name:                  "Security Auditor",
		Description:           "Performs security audits",
		Instructions:          "Audit security vulnerabilities",
		InitialLifecycleState: store.WorkerLifecycleStatePending,
		ProposedBindings:      map[string]string{"primary": workspaceID},
		WorkspaceRequirements: []store.WorkerWorkspaceRequirement{
			{Role: "primary", Description: "Primary workspace", Required: true},
		},
		IdempotencyKey: "test-accept-idemp",
	})
	if err != nil {
		t.Fatalf("failed to create pending worker: %v", err)
	}
	if w.LifecycleState != store.WorkerLifecycleStatePending {
		t.Fatalf("expected pending lifecycle state, got %s", w.LifecycleState)
	}

	// 1. Accept with valid revision
	body := fmt.Sprintf(`{"expected_revision": %d}`, w.Revision)
	rec := executeWorkerAPI(h, http.MethodPost, "/"+w.ID+"/accept", body, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal accept response: %v", err)
	}
	workerData, _ := res["worker"].(map[string]any)
	if workerData["lifecycle_state"] != "active" {
		t.Fatalf("expected active lifecycle state after accept, got %v", workerData["lifecycle_state"])
	}
	if uint64(workerData["revision"].(float64)) != 2 {
		t.Fatalf("expected revision 2 after accept, got %v", workerData["revision"])
	}
	bindings, _ := workerData["local_bindings"].(map[string]any)
	if bindings["primary"] != workspaceID {
		t.Fatalf("expected primary binding %s, got %v", workspaceID, bindings["primary"])
	}

	// Verify durable state in store
	loaded, found, err := s.sessions.GetWorker("acct-test", w.ID)
	if err != nil || !found {
		t.Fatalf("get worker from store: %v, found=%v", err, found)
	}
	if loaded.LifecycleState != store.WorkerLifecycleStateActive {
		t.Fatalf("expected store record to be active, got %s", loaded.LifecycleState)
	}
	if loaded.Revision != 2 {
		t.Fatalf("expected store record revision 2, got %d", loaded.Revision)
	}
	if loaded.LocalBindings["primary"] != workspaceID {
		t.Fatalf("expected store record binding primary=%s, got %v", workspaceID, loaded.LocalBindings)
	}
}

func TestWorkerAPI_AcceptRejections(t *testing.T) {
	s, db, h := setupWorkerAPITestServer(t)
	workspaceID := setupWorkerAPIExecution(t, s, db)

	w, err := s.sessions.CreateWorker(context.Background(), "acct-test", "user-test", store.CreateWorkerRequest{
		Name:                  "Gatekeeper",
		Instructions:          "Guard entrance",
		InitialLifecycleState: store.WorkerLifecycleStatePending,
		ProposedBindings:      map[string]string{"primary": workspaceID},
		WorkspaceRequirements: []store.WorkerWorkspaceRequirement{
			{Role: "primary", Required: true},
		},
		IdempotencyKey: "test-reject-idemp",
	})
	if err != nil {
		t.Fatalf("create pending worker: %v", err)
	}

	// 1. Missing expected_revision (0) -> 400 Bad Request
	rec := executeWorkerAPI(h, http.MethodPost, "/"+w.ID+"/accept", `{"expected_revision": 0}`, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for revision 0, got %d: %s", rec.Code, rec.Body.String())
	}

	// 2. Stale expected_revision (99) -> 409 Conflict
	recStale := executeWorkerAPI(h, http.MethodPost, "/"+w.ID+"/accept", `{"expected_revision": 99}`, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if recStale.Code != http.StatusConflict {
		t.Fatalf("expected 409 for stale revision, got %d: %s", recStale.Code, recStale.Body.String())
	}

	// 3. Cross-account access -> 404 Not Found
	recCross := executeWorkerAPI(h, http.MethodPost, "/"+w.ID+"/accept", fmt.Sprintf(`{"expected_revision": %d}`, w.Revision), workerAPICallOptions{
		account: "acct-1",
		user:    "user-1",
		scopes:  []string{"automations:write"},
	})
	if recCross.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-account accept, got %d: %s", recCross.Code, recCross.Body.String())
	}

	// 4. Agent principal origin -> 403 Forbidden
	recAgent := executeWorkerAPI(h, http.MethodPost, "/"+w.ID+"/accept", fmt.Sprintf(`{"expected_revision": %d}`, w.Revision), workerAPICallOptions{
		agentOrigin: true,
		scopes:      []string{"automations:write"},
	})
	if recAgent.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for agent origin, got %d: %s", recAgent.Code, recAgent.Body.String())
	}

	// 5. Explicit agent principal -> 403 Forbidden
	agentPrincipal := identity.Principal{Type: "agent", UserID: "subagent-1", AccountScopeID: "acct-test"}
	recAgentP := executeWorkerAPI(h, http.MethodPost, "/"+w.ID+"/accept", fmt.Sprintf(`{"expected_revision": %d}`, w.Revision), workerAPICallOptions{
		principal: &agentPrincipal,
		scopes:    []string{"automations:write"},
	})
	if recAgentP.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for agent principal, got %d: %s", recAgentP.Code, recAgentP.Body.String())
	}

	// 6. Scoped trigger token -> 403 Forbidden
	scopedToken := &store.ScopedTokenRecord{
		ID:             "tok-1",
		WorkerID:       w.ID,
		AccountScopeID: "acct-test",
		UserID:         "user-test",
		Scopes:         []string{"automations:trigger"},
	}
	recScoped := executeWorkerAPI(h, http.MethodPost, "/"+w.ID+"/accept", fmt.Sprintf(`{"expected_revision": %d}`, w.Revision), workerAPICallOptions{
		scopedToken: scopedToken,
	})
	if recScoped.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for scoped trigger token, got %d: %s", recScoped.Code, recScoped.Body.String())
	}

	// 7. Verify worker is completely unmutated
	loaded, found, err := s.sessions.GetWorker("acct-test", w.ID)
	if err != nil || !found {
		t.Fatalf("get worker: %v", err)
	}
	if loaded.LifecycleState != store.WorkerLifecycleStatePending || loaded.Revision != 1 {
		t.Fatalf("worker state mutated after failed attempts: %+v", loaded)
	}
}

func TestWorkerAPI_ListPendingLifecycleFilter(t *testing.T) {
	s, db, h := setupWorkerAPITestServer(t)
	workspaceID := setupWorkerAPIExecution(t, s, db)

	// Create 1 pending worker and 1 idle worker
	pendingWorker, err := s.sessions.CreateWorker(context.Background(), "acct-test", "user-test", store.CreateWorkerRequest{
		Name:                  "Pending Inspectable",
		Instructions:          "Wait for human",
		InitialLifecycleState: store.WorkerLifecycleStatePending,
		ProposedBindings:      map[string]string{"primary": workspaceID},
		IdempotencyKey:        "idemp-pending-1",
	})
	if err != nil {
		t.Fatalf("create pending: %v", err)
	}

	_, err = s.sessions.CreateWorker(context.Background(), "acct-test", "user-test", store.CreateWorkerRequest{
		Name:                  "Idle Worker",
		Instructions:          "Idle directly",
		InitialLifecycleState: store.WorkerLifecycleStateIdle,
		IdempotencyKey:        "idemp-idle-1",
	})
	if err != nil {
		t.Fatalf("create idle: %v", err)
	}

	// Query with lifecycle_state=pending
	rec := executeWorkerAPI(h, http.MethodGet, "?lifecycle_state=pending", "", workerAPICallOptions{
		scopes: []string{"automations:read"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal list response: %v", err)
	}
	workers, _ := res["workers"].([]any)
	if len(workers) != 1 {
		t.Fatalf("expected 1 pending worker in list, got %d", len(workers))
	}
	w0, _ := workers[0].(map[string]any)
	if w0["id"] != pendingWorker.ID {
		t.Fatalf("expected worker id %s, got %v", pendingWorker.ID, w0["id"])
	}
	if w0["lifecycle_state"] != "pending" {
		t.Fatalf("expected lifecycle_state pending, got %v", w0["lifecycle_state"])
	}
}
