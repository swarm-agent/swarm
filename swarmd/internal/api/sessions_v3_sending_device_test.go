package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a run started by a message keeps the device that sent it all the
// way to the device tool call, for a new conversation's first message and for
// a follow-up. Requirement: POST /v3/sessions/{id}/messages with a client
// session's actor records that client session as the run's sending device;
// the device gate (permission authorizeDeviceCall) routes machine_run there.
// Threat: the executor's claim (recordRunStatus -> sessionV3RunIntentForJob)
// rewrote the run intent without SendingClientSessionID, so by the time a
// tool was authorized the run named no device and every device call was
// refused with "no device is connected". Boundary: the messages route,
// ApplySessionMutation's run-intent transition
// (validateV3RunIntentTransition), and the provider-managed tool invoker that
// Claude Code's MCP bridge calls (run.Service.NewProviderManagedToolInvoker ->
// gateToolCalls -> AuthorizeToolCall). Devices live only in a separate
// secrets store, as in the daemon. Narrowest layer: these handlers and the
// real invoker over temporary stores; no provider or device is contacted.
func TestMessageSendingDeviceReachesDeviceToolCall(t *testing.T) {
	server, _, permissionSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	principal := testPrincipal()
	secrets, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secrets.Close() })
	permissionSvc.SetClientSessionLister(pebblestore.NewIdentitySessionStore(secrets))
	now := time.Now().UnixMilli()
	const device = "client-pc"
	if err := secrets.PutJSON(pebblestore.KeyIdentityClientSession(device), pebblestore.ClientSessionRecord{SessionID: device, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Device: "pc", ExpiresAt: now + int64(time.Hour/time.Millisecond), LastSeenAt: now}); err != nil {
		t.Fatal(err)
	}
	main := server.sessions.Store().Underlying()
	rules := pebblestore.DefaultAccountPermissions()
	rules.Devices[device] = pebblestore.DevicePermissionRule{Files: pebblestore.RuleAllowed, Browser: pebblestore.RuleAllowed}
	if _, err := main.SetAccountPermissions(principal.AccountScopeID, 0, rules); err != nil {
		t.Fatal(err)
	}
	pref := pebblestore.ModelPreference{Provider: "claudecode", Model: "test-model", Thinking: "medium"}

	send := func(t *testing.T, sessionID, requestID, runID, clientSessionID string) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"client_request_id": requestID, "run_id": runID, "role": "user", "content": "run a command on my computer"})
		req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+sessionID+"/messages", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		actor := testActorContext()
		actor.ClientSessionID = clientSessionID
		ctx := context.WithValue(req.Context(), productActorRequestContextKey, actor)
		ctx = context.WithValue(ctx, productPrincipalRequestContextKey, actor.Principal)
		ctx = identity.ContextWithPrincipal(ctx, actor.Principal)
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req.WithContext(ctx))
		if rec.Code != http.StatusOK {
			t.Fatalf("message status = %d body=%s", rec.Code, rec.Body.String())
		}
	}
	executor := &sessionV3Executor{server: server}
	claim := func(t *testing.T, sessionID, runID, status string) {
		t.Helper()
		job := sessionV3ExecutorJob{Principal: principal, SessionID: sessionID, RunID: runID}
		eventType := "session.assistant.started"
		if status != sessionruntime.RunIntentRunning {
			eventType = "session.run.completed"
		}
		if _, err := executor.recordRunStatus(job, status, "", eventType); err != nil {
			t.Fatalf("record %s: %v", status, err)
		}
	}
	deviceCall := func(t *testing.T, sessionID, runID, callID string) {
		t.Helper()
		intent, ok, err := server.sessions.GetV3SessionRunIntent(sessionID, runID)
		if err != nil || !ok || intent.SendingClientSessionID != device {
			t.Fatalf("running run intent = %+v ok=%v err=%v, want sending device %q", intent, ok, err, device)
		}
		invoker := server.runner.(*runruntime.Service).NewProviderManagedToolInvoker(runruntime.ProviderManagedToolInvokerConfig{SessionID: sessionID, PermissionSessionID: sessionID, RunID: runID, Principal: principal, ProviderID: "claudecode", SessionMode: sessionruntime.ModeAuto})
		// The device never answers here; the call only has to be authorized
		// and recorded for the sending device before the wait is abandoned.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = invoker.ExecuteTool(ctx, provideriface.ToolInvocation{CallID: callID, Name: "machine_run", Arguments: `{"command":"true"}`})
		records, err := permissionSvc.ListPermissions(sessionID, 50)
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range records {
			if record.CallID == callID {
				if record.TargetDevice != device || record.RunID != runID {
					t.Fatalf("device call record = %+v, want target %q", record, device)
				}
				return
			}
		}
		t.Fatalf("device call %q was not routed to the sending device; records=%+v", callID, records)
	}

	t.Run("new session first message", func(t *testing.T) {
		session := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "device-new", "device new", t.TempDir(), pref)
		send(t, session.ID, "first", "run-first", device)
		claim(t, session.ID, "run-first", sessionruntime.RunIntentRunning)
		deviceCall(t, session.ID, "run-first", "call-first")
	})

	t.Run("follow-up message", func(t *testing.T) {
		session := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "device-follow", "device follow", t.TempDir(), pref)
		// The conversation began elsewhere, without a device.
		send(t, session.ID, "opening", "run-opening", "")
		claim(t, session.ID, "run-opening", sessionruntime.RunIntentRunning)
		claim(t, session.ID, "run-opening", sessionruntime.RunIntentCompleted)
		if intent, _, _ := server.sessions.GetV3SessionRunIntent(session.ID, "run-opening"); intent.SendingClientSessionID != "" {
			t.Fatalf("run without a device names one: %+v", intent)
		}
		send(t, session.ID, "followup", "run-followup", device)
		claim(t, session.ID, "run-followup", sessionruntime.RunIntentRunning)
		deviceCall(t, session.ID, "run-followup", "call-followup")
	})
}
