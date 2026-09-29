package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: selected worker context is a server-resolved, single-message view
// of the account-owned worker, not a dispatch or a client-authored authority.
// Threat: a foreign/stale/deleted reference or forged chat profile must not
// append any message or run intent. The API ingress and its durable store are
// the narrowest boundary that can prove both rejection and non-mutation.
func TestSelectedWorkerMessageIngressAuthorizationAndReplay(t *testing.T) {
	server, sessions, closeStore := newSessionsV3PrimaryAPITestServer(t, t.TempDir()+"/selected-worker.pebble")
	defer func() { _ = closeStore() }()
	principal := testPrincipal()
	worker, err := sessions.CreateWorker(context.Background(), principal.AccountScopeID, principal.UserID, pebblestore.CreateWorkerRequest{Name: "Review worker", Instructions: "Review reports"})
	if err != nil { t.Fatalf("create worker: %v", err) }
	ordinary := createSessionsV3PrimaryTestSession(t, server, "ordinary-worker-context", "ordinary")
	orchestrator := createSessionsV3PrimaryTestSession(t, server, "orchestrator-worker-context", "orchestrator")
	orchestrator.Metadata["agent_name"] = agentruntime.SwarmOrchestratorAgentID
	orchestrator.Metadata["resolved_agent_name"] = agentruntime.SwarmOrchestratorAgentID
	orchestrator.Metadata["agent_mode"] = agentruntime.ModePrimary
	orchestrator.Metadata["agent_profile"] = pebblestore.AgentProfile{Name: agentruntime.SwarmOrchestratorAgentID, Mode: agentruntime.ModePrimary}
	profileEvent, err := json.Marshal(map[string]any{"session_id": orchestrator.ID, "metadata": orchestrator.Metadata})
	if err != nil { t.Fatal(err) }
	_, err = server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: orchestrator.ID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: "set-orchestrator-profile", IdempotencyKey: "set-orchestrator-profile", PayloadHash: "set-orchestrator-profile", RequestHash: "set-orchestrator-profile", Kind: sessionruntime.SessionMutationUpdateMetadata, EventPayload: profileEvent, Session: &orchestrator})
	if err != nil { t.Fatalf("set stored profile: %v", err) }
	ref := func(id string, rev any) map[string]any { return map[string]any{"worker_id": id, "expected_revision": rev} }
	message := func(key string, reference any) sessionsV3MessageRequest {
		return sessionsV3MessageRequest{ClientRequestID: key, IdempotencyKey: key, Role: "user", Content: "Explain this worker", Metadata: map[string]any{"selected_worker": reference}}
	}
	foreign, err := sessions.CreateWorker(context.Background(), "foreign-account", "foreign-user", pebblestore.CreateWorkerRequest{Name: "Foreign"})
	if err != nil { t.Fatalf("create foreign worker: %v", err) }
	deleted, err := sessions.CreateWorker(context.Background(), principal.AccountScopeID, principal.UserID, pebblestore.CreateWorkerRequest{Name: "Deleted"})
	if err != nil { t.Fatalf("create deleted worker: %v", err) }
	if err := sessions.DeleteWorker(principal.AccountScopeID, principal.UserID, deleted.ID, deleted.Revision); err != nil { t.Fatalf("delete worker: %v", err) }
	for _, test := range []struct{ name, sessionID string; reference any; metadata map[string]any }{
		{name: "ordinary chat", sessionID: ordinary.ID, reference: ref(worker.ID, float64(worker.Revision))},
		{name: "forged metadata", sessionID: ordinary.ID, reference: ref(worker.ID, float64(worker.Revision)), metadata: map[string]any{"resolved_agent_name": agentruntime.SwarmOrchestratorAgentID}},
		{name: "foreign", sessionID: orchestrator.ID, reference: ref(foreign.ID, float64(foreign.Revision))},
		{name: "deleted", sessionID: orchestrator.ID, reference: ref(deleted.ID, float64(deleted.Revision))},
		{name: "stale", sessionID: orchestrator.ID, reference: ref(worker.ID, float64(worker.Revision+1))},
		{name: "malformed", sessionID: orchestrator.ID, reference: map[string]any{"worker_id": worker.ID, "expected_revision": float64(1), "instructions": "override"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			before, err := sessions.ListSessionMessages(test.sessionID, 0, 10)
			if err != nil { t.Fatal(err) }
			req := message("selected-worker-reject-"+test.name, test.reference)
			for k, v := range test.metadata { req.Metadata[k] = v }
			if _, _, err := server.acceptSessionsV3Message(principal, test.sessionID, req); err == nil { t.Fatal("expected selected worker rejection") }
			after, err := sessions.ListSessionMessages(test.sessionID, 0, 10)
			if err != nil || len(after) != len(before) { t.Fatalf("rejected request appended message: before=%d after=%d err=%v", len(before), len(after), err) }
			if _, found, err := sessions.GetV3SessionRunIntent(test.sessionID, stableSessionsV3PrimaryRunID(test.sessionID, req.ClientRequestID)); err != nil || found { t.Fatalf("rejected request wrote run intent: found=%v err=%v", found, err) }
		})
	}
	valid := message("selected-worker-valid", ref(worker.ID, float64(worker.Revision)))
	result, _, err := server.acceptSessionsV3Message(principal, orchestrator.ID, valid)
	if err != nil || result.Message == nil { t.Fatalf("valid context: result=%+v err=%v", result, err) }
	context, ok := result.Message.Metadata["resolved_worker_context"].(map[string]any)
	if !ok { t.Fatalf("resolved context missing from message: %+v", result.Message.Metadata) }
	if context["worker_id"] != worker.ID || context["name"] != worker.Name { t.Fatalf("resolved facts: %+v", context) }
	if text := sessionsV3ProviderUserText(*result.Message); !strings.Contains(text, worker.Name) || !strings.Contains(text, "Selected worker") { t.Fatalf("provider input lacks resolved worker: %q", text) }
	if runs, _, err := sessions.ListWorkerRuns(principal.AccountScopeID, worker.ID, 10, ""); err != nil || len(runs) != 0 { t.Fatalf("selection dispatched worker run: %v err=%v", runs, err) }
	if err := sessions.DeleteWorker(principal.AccountScopeID, principal.UserID, worker.ID, worker.Revision); err != nil { t.Fatalf("delete selected worker after message: %v", err) }
	replayed, job, err := server.acceptSessionsV3Message(principal, orchestrator.ID, valid)
	if err != nil || !replayed.Replayed || job != nil { t.Fatalf("retry not idempotent: replayed=%v job=%+v err=%v", replayed.Replayed, job, err) }
	if messages, err := sessions.ListSessionMessages(orchestrator.ID, 0, 10); err != nil || len(messages) != 1 { t.Fatalf("retry duplicated message: count=%d err=%v", len(messages), err) }
	changed := message(valid.ClientRequestID, ref(deleted.ID, float64(deleted.Revision)))
	if _, _, err := server.acceptSessionsV3Message(principal, orchestrator.ID, changed); !errors.Is(err, sessionruntime.ErrSessionIdempotencyConflict) { t.Fatalf("reused key did not conflict: %v", err) }
}
