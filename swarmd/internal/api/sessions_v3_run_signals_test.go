package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a session run that ends failed or interrupted must reach the signal
// feed (session.run.failed / session.run.interrupted, warnings) exactly once,
// naming the session, run and agent but not the failure reason, so a monitor
// subscribed to the feed hears about a broken public chat without polling.
// Threat: a sealed front-desk agent failing silently (the feed previously only
// covered worker runs), a replayed status write paging twice, or private error
// text leaving the machine through a forwarded signal.
// Boundary: sessionV3Executor.recordRunStatusInEpoch -> Server.signalSessionRunEnded.
// Narrowest layer: the executor's real durable status write on a real session,
// read back from the signal store; no provider is involved.
func TestSessionRunFailureAndInterruptionRaiseOneSignal(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "signals.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	server.SetSignalStore(pebblestore.NewSignalStore(db))
	exec := newSessionV3Executor(server)

	signalsFor := func(kind string) []pebblestore.Signal {
		page, err := server.signals.ListAfter(0, 100, pebblestore.SignalFilter{Account: testPrincipal().AccountScopeID})
		if err != nil {
			t.Fatal(err)
		}
		var out []pebblestore.Signal
		for _, sig := range page.Signals {
			if sig.Kind == kind {
				out = append(out, sig)
			}
		}
		return out
	}
	run := func(clientRequestID, runID, terminal, eventType string) pebblestore.SessionSnapshot {
		created := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, clientRequestID, clientRequestID, t.TempDir(), pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
		job := sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: created.ID, RunID: runID, EpochID: "epoch-00000000000000000001"}
		for _, step := range []struct{ status, event string }{
			{sessionruntime.RunIntentPendingExecutor, "session.assistant.queued"},
			{sessionruntime.RunIntentRunning, "session.assistant.started"},
			{terminal, eventType},
			{terminal, eventType}, // replay of the same terminal write
		} {
			if _, err := exec.recordRunStatus(job, step.status, "secret provider detail sk-test-123", step.event); err != nil {
				t.Fatalf("record %s: %v", step.status, err)
			}
		}
		return created
	}

	failed := run("signal-failed", "run-failed", sessionruntime.RunIntentFailed, "session.run.failed")
	got := signalsFor("session.run.failed")
	if len(got) != 1 {
		t.Fatalf("session.run.failed signals = %d, want exactly 1 (replay must not repeat it)", len(got))
	}
	sig := got[0]
	if sig.Severity != pebblestore.SignalSeverityWarning || sig.Refs["session_id"] != failed.ID || sig.Refs["run_id"] != "run-failed" ||
		sig.Account != testPrincipal().AccountScopeID {
		t.Fatalf("signal = %+v", sig)
	}
	if agent := sessionsV3MetadataString(failed.Metadata, "agent_name"); agent != "" && sig.Attrs["agent"] != agent {
		t.Fatalf("signal agent = %q, want %q", sig.Attrs["agent"], agent)
	}
	for _, text := range []string{sig.Summary, sig.DedupKey, sig.Attrs["agent"]} {
		if strings.Contains(text, "secret") || strings.Contains(text, "sk-test") {
			t.Fatalf("failure reason leaked into the signal: %+v", sig)
		}
	}

	run("signal-interrupted", "run-interrupted", sessionruntime.RunIntentInterrupted, "session.run.interrupted")
	if got := signalsFor("session.run.interrupted"); len(got) != 1 || got[0].Refs["run_id"] != "run-interrupted" {
		t.Fatalf("session.run.interrupted signals = %+v", got)
	}

	run("signal-completed", "run-completed", sessionruntime.RunIntentCompleted, "session.assistant.completed")
	page, err := server.signals.ListAfter(0, 100, pebblestore.SignalFilter{Account: testPrincipal().AccountScopeID})
	if err != nil {
		t.Fatal(err)
	}
	for _, sig := range page.Signals {
		if sig.Refs["run_id"] == "run-completed" {
			t.Fatalf("a completed run raised %s", sig.Kind)
		}
	}
}

// The fixtures below create a real V3 session through the create route on a
// host workspace binding, for the run-signal and takeover tests.

func seedSessionsV3PrimaryAuthority(t *testing.T, server *Server, workspacePath string) string {
	t.Helper()
	if server == nil || server.topology == nil || server.swarmStore == nil {
		t.Fatal("server topology and swarm store are required")
	}
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		t.Fatal("workspace path is required")
	}
	now := time.Now().UnixMilli()
	if _, err := server.swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "host-swarm-id", Name: "host-swarm", Role: "master", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("put local node: %v", err)
	}
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{SwarmID: "host-swarm-id", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, Name: "host-swarm", Role: "master", Relationship: "self", Status: "online", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("upsert local runtime: %v", err)
	}
	if _, err := server.topology.EnsureLocalSelfPlacementForPrincipal(testPrincipal().AccountScopeID, testPrincipal().UserID); err != nil {
		t.Fatalf("ensure self placement: %v", err)
	}
	bindingID := "binding-v3-primary-" + strings.NewReplacer("/", "-", " ", "-", "_", "-").Replace(strings.Trim(workspacePath, "/"))
	if _, err := server.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                       bindingID,
		UserID:                          testPrincipal().UserID,
		AccountScopeID:                  testPrincipal().AccountScopeID,
		SourceWorkspaceID:               "workspace-v3-" + bindingID,
		SourceWorkspaceGeneration:       1,
		SourceWorkspacePath:             workspacePath,
		SourceWorkspaceName:             filepath.Base(workspacePath),
		DestinationRuntimeSwarmID:       "host-swarm-id",
		DestinationAuthorityHostSwarmID: "host-swarm-id",
		DestinationHostSwarmID:          "host-swarm-id",
		DestinationRuntimeKind:          pebblestore.TopologyRuntimeKindHost,
		DestinationWorkspacePath:        workspacePath,
		PlacementGeneration:             1,
		BindingGeneration:               1,
		State:                           pebblestore.TopologyWorkspaceBindingStateBound,
		AccessMode:                      pebblestore.TopologyWorkspaceBindingAccessModeReadWrite,
		MaterializationKind:             pebblestore.TopologyWorkspaceBindingMaterializationSource,
		AttestedByHostSwarmID:           "host-swarm-id",
		Writable:                        true,
	}); err != nil {
		t.Fatalf("upsert v3 binding: %v", err)
	}
	return bindingID
}

func createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t *testing.T, server *Server, clientRequestID, title, workspacePath string, pref pebblestore.ModelPreference) pebblestore.SessionSnapshot {
	t.Helper()
	return createSessionsV3PrimaryTestSessionForAgent(t, server, clientRequestID, title, workspacePath, pref, "swarm")
}

// seedSealedTestAgent stores the sealed agent "frontdesk" (one client tool,
// lookup_order) in the test principal's account, for takeover tests.
func seedSealedTestAgent(t *testing.T, server *Server) string {
	t.Helper()
	account := testPrincipal().AccountScopeID
	if _, err := server.agents.PutCustomToolForAccount(account, pebblestore.AgentCustomToolDefinition{Name: "lookup_order", Kind: pebblestore.AgentCustomToolKindClient, Description: "Look up an order.", InputSchema: lookupOrderSchema, Effect: "read"}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := server.agents.UpsertForAccount(account, agentruntime.UpsertInput{
		Name: "frontdesk", Mode: agentruntime.ModeSubagent, Provider: "test-provider", Model: "test-model", Enabled: pebblestore.BoolPtr(true), Prompt: "Take orders.",
		ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"lookup_order": {Enabled: pebblestore.BoolPtr(true)}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.sealedAgentError(account, "frontdesk"); err != nil {
		t.Fatalf("fixture agent is not sealed: %v", err)
	}
	return "frontdesk"
}

func createSessionsV3PrimaryTestSessionForAgent(t *testing.T, server *Server, clientRequestID, title, workspacePath string, pref pebblestore.ModelPreference, agentName string) pebblestore.SessionSnapshot {
	t.Helper()
	bindingID := seedSessionsV3PrimaryAuthority(t, server, workspacePath)
	payload := map[string]any{
		"client_request_id":    clientRequestID,
		"workspace_path":       workspacePath,
		"workspace_name":       filepath.Base(workspacePath),
		"swarm_id":             "host-swarm-id",
		"workspace_binding_id": bindingID,
		"target_kind":          "host",
		"target_relationship":  "self",
		"title":                title,
		"mode":                 sessionruntime.ModeAuto,
		"agent_name":           agentName,
		"preference":           pref,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal create payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var created struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	return created.Session
}
