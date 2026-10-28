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
	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

const sendingDeviceTestClient = "client-pc"

// connectSendingDeviceTestClient wires devices the way the daemon does: the
// client session lives only in a separate secrets store, and the account
// allows device tools for it in the main store.
func connectSendingDeviceTestClient(t *testing.T, server *Server, permissionSvc *permission.Service, principal identity.Principal) {
	t.Helper()
	secrets, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secrets.Close() })
	permissionSvc.SetClientSessionLister(pebblestore.NewIdentitySessionStore(secrets))
	now := time.Now().UnixMilli()
	if err := secrets.PutJSON(pebblestore.KeyIdentityClientSession(sendingDeviceTestClient), pebblestore.ClientSessionRecord{SessionID: sendingDeviceTestClient, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Device: "pc", ExpiresAt: now + int64(time.Hour/time.Millisecond), LastSeenAt: now}); err != nil {
		t.Fatal(err)
	}
	main := server.sessions.Store().Underlying()
	rules := pebblestore.DefaultAccountPermissions()
	rules.Devices[sendingDeviceTestClient] = pebblestore.DevicePermissionRule{Files: pebblestore.RuleAllowed, Browser: pebblestore.RuleAllowed}
	if _, err := main.SetAccountPermissions(principal.AccountScopeID, 0, rules); err != nil {
		t.Fatal(err)
	}
}

// withSendingDeviceTestActor attaches the authenticated actor a client token
// produces for principal; clientSessionID "" is a caller without a device.
func withSendingDeviceTestActor(req *http.Request, principal identity.Principal, clientSessionID string) *http.Request {
	actor := testActorContext()
	actor.Principal = principal
	actor.UserID = principal.UserID
	actor.AccountScopeID = principal.AccountScopeID
	actor.ClientSessionID = clientSessionID
	ctx := context.WithValue(req.Context(), productActorRequestContextKey, actor)
	ctx = context.WithValue(ctx, productPrincipalRequestContextKey, actor.Principal)
	ctx = identity.ContextWithPrincipal(ctx, actor.Principal)
	return req.WithContext(ctx)
}

// claimSendingDeviceTestRun performs the executor's real run-intent write
// (recordRunStatus -> sessionV3RunIntentForJob) that used to erase the device.
func claimSendingDeviceTestRun(t *testing.T, server *Server, principal identity.Principal, sessionID, runID, status string) {
	t.Helper()
	executor := &sessionV3Executor{server: server}
	eventType := "session.assistant.started"
	if status != sessionruntime.RunIntentRunning {
		eventType = "session.run.completed"
	}
	if _, err := executor.recordRunStatus(sessionV3ExecutorJob{Principal: principal, SessionID: sessionID, RunID: runID}, status, "", eventType); err != nil {
		t.Fatalf("record %s: %v", status, err)
	}
}

// assertSendingDeviceTestCall asserts the running run still names the device
// and that machine_run through Claude Code's provider-managed tool invoker is
// authorized and recorded for that device.
func assertSendingDeviceTestCall(t *testing.T, server *Server, permissionSvc *permission.Service, principal identity.Principal, sessionID, runID, callID string) {
	t.Helper()
	intent, ok, err := server.sessions.GetV3SessionRunIntent(sessionID, runID)
	if err != nil || !ok || intent.SendingClientSessionID != sendingDeviceTestClient {
		t.Fatalf("running run intent = %+v ok=%v err=%v, want sending device %q", intent, ok, err, sendingDeviceTestClient)
	}
	invoker := server.runner.(*runruntime.Service).NewProviderManagedToolInvoker(runruntime.ProviderManagedToolInvokerConfig{SessionID: sessionID, PermissionSessionID: sessionID, RunID: runID, Principal: principal, ProviderID: "claudecode", SessionMode: sessionruntime.ModeAuto})
	// The device never answers here; the call only has to be authorized and
	// recorded for the sending device before the wait is abandoned.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = invoker.ExecuteTool(ctx, provideriface.ToolInvocation{CallID: callID, Name: "machine_run", Arguments: `{"command":"true"}`})
	records, err := permissionSvc.ListPermissions(sessionID, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.CallID == callID {
			if record.TargetDevice != sendingDeviceTestClient || record.RunID != runID {
				t.Fatalf("device call record = %+v, want target %q", record, sendingDeviceTestClient)
			}
			return
		}
	}
	t.Fatalf("device call %q was not routed to the sending device; records=%+v", callID, records)
}

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
	connectSendingDeviceTestClient(t, server, permissionSvc, principal)
	pref := pebblestore.ModelPreference{Provider: "claudecode", Model: "test-model", Thinking: "medium"}

	send := func(t *testing.T, sessionID, requestID, runID, clientSessionID string) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"client_request_id": requestID, "run_id": runID, "role": "user", "content": "run a command on my computer"})
		req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+sessionID+"/messages", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, withSendingDeviceTestActor(req, principal, clientSessionID))
		if rec.Code != http.StatusOK {
			t.Fatalf("message status = %d body=%s", rec.Code, rec.Body.String())
		}
	}

	t.Run("new session first message", func(t *testing.T) {
		session := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "device-new", "device new", t.TempDir(), pref)
		send(t, session.ID, "first", "run-first", sendingDeviceTestClient)
		claimSendingDeviceTestRun(t, server, principal, session.ID, "run-first", sessionruntime.RunIntentRunning)
		assertSendingDeviceTestCall(t, server, permissionSvc, principal, session.ID, "run-first", "call-first")
	})

	t.Run("follow-up message", func(t *testing.T) {
		session := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "device-follow", "device follow", t.TempDir(), pref)
		// The conversation began elsewhere, without a device.
		send(t, session.ID, "opening", "run-opening", "")
		claimSendingDeviceTestRun(t, server, principal, session.ID, "run-opening", sessionruntime.RunIntentRunning)
		claimSendingDeviceTestRun(t, server, principal, session.ID, "run-opening", sessionruntime.RunIntentCompleted)
		if intent, _, _ := server.sessions.GetV3SessionRunIntent(session.ID, "run-opening"); intent.SendingClientSessionID != "" {
			t.Fatalf("run without a device names one: %+v", intent)
		}
		send(t, session.ID, "followup", "run-followup", sendingDeviceTestClient)
		claimSendingDeviceTestRun(t, server, principal, session.ID, "run-followup", sessionruntime.RunIntentRunning)
		assertSendingDeviceTestCall(t, server, permissionSvc, principal, session.ID, "run-followup", "call-followup")
	})
}

// Purpose: a conversation started through /v3/sessions:routed (how the web
// Desktop and the TUI start one, with the first message in the request)
// records the caller's client session as the first message's and first run's
// sending device, so device tools in that run reach that device. Threat:
// handleRoutedSessionStart built the first run intent without the actor's
// ClientSessionID, so the first run named no device and every device call was
// refused with "no device is connected". Boundary: handleRoutedSessionStart's
// atomic create mutation, the executor's claim, and the provider-managed tool
// invoker (AuthorizeToolCall). Negative/idempotency case: replaying the same
// start (same client_request_id) without a device returns the stored session
// and leaves the recorded device unchanged. Narrowest layer: the real route
// over the routed-start fixture's temporary stores; the Router provider is a
// recording fake and no device is contacted.
func TestRoutedSessionStartSendingDeviceReachesDeviceToolCall(t *testing.T) {
	runner := &sessionRouterRecordingRunner{id: "recording", response: provideriface.Response{Text: `{"title":"Device task","worktree_name":"device-task"}`}}
	server, sessions, principal := newRoutedSessionAtomicityServer(t, runner, false, true)
	managedPath := t.TempDir()
	server.SetWorktreeService(&routedWorktreeServiceStub{repositoryFixture: t, fakeWorktreeService: fakeWorktreeService{
		config:     worktreeruntime.Config{Enabled: true, UseCurrentBranch: true, BranchName: "agent/<id>"},
		allocation: worktreeruntime.Allocation{WorkspacePath: managedPath, RepoRoot: managedPath, BranchName: "agent/device-task", WorkspaceID: "device-task"},
	}})
	permissionSvc, ok := server.perm.(*permission.Service)
	if !ok {
		t.Fatalf("server permission service = %T", server.perm)
	}
	connectSendingDeviceTestClient(t, server, permissionSvc, principal)

	const requestID = "routed-device-start"
	start := func(t *testing.T, clientSessionID string) routedSessionAtomicityResponse {
		t.Helper()
		body := map[string]any{"input": "run a command on my computer", "client_request_id": requestID, "plan_mode_requested": false}
		addRoutedSessionTestAuthority(body)
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, RoutedSessionsPath, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, withSendingDeviceTestActor(req, principal, clientSessionID))
		if rec.Code != http.StatusOK {
			t.Fatalf("routed start status = %d body=%s", rec.Code, rec.Body.String())
		}
		return decodeRoutedSessionAtomicityResponse(t, rec)
	}

	created := start(t, sendingDeviceTestClient)
	runID := stableSessionsV3PrimaryRunID(created.SessionID, "routed-message:"+requestID)
	intent, found, err := sessions.GetV3SessionRunIntent(created.SessionID, runID)
	if err != nil || !found || intent.SendingClientSessionID != sendingDeviceTestClient {
		t.Fatalf("routed first run intent = %+v found=%v err=%v, want sending device %q", intent, found, err, sendingDeviceTestClient)
	}
	messages, err := sessions.ListMessages(created.SessionID, 0, 10)
	if err != nil || len(messages) == 0 || messages[0].SendingClientSessionID != sendingDeviceTestClient {
		t.Fatalf("routed first message = %+v err=%v, want sending device %q", messages, err, sendingDeviceTestClient)
	}

	replayed := start(t, "")
	if !replayed.Replayed || replayed.SessionID != created.SessionID {
		t.Fatalf("routed replay = %+v, want replay of %q", replayed, created.SessionID)
	}
	if intent, _, _ := sessions.GetV3SessionRunIntent(created.SessionID, runID); intent.SendingClientSessionID != sendingDeviceTestClient {
		t.Fatalf("replay changed the recorded device: %+v", intent)
	}

	claimSendingDeviceTestRun(t, server, principal, created.SessionID, runID, sessionruntime.RunIntentRunning)
	assertSendingDeviceTestCall(t, server, permissionSvc, principal, created.SessionID, runID, "call-routed")
}
