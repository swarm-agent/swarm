package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type sessionsV3ReconnectTestPayload struct {
	OK                        bool                                           `json:"ok"`
	Rev                       uint64                                         `json:"rev"`
	SnapshotEndpointCursor    string                                         `json:"snapshot_endpoint_cursor"`
	SessionsByID              map[string]pebblestore.SessionSnapshot         `json:"sessions_by_id"`
	ProjectionsBySession      map[string]pebblestore.V3SessionProjection     `json:"projections_by_session"`
	RunIntentsBySession       map[string][]pebblestore.V3SessionRunIntent    `json:"run_intents_by_session"`
	CurrentRunIntentBySession map[string]pebblestore.V3SessionRunIntent      `json:"current_run_intent_by_session"`
	Subscriptions             []sessionsV3ReconnectSubscription              `json:"subscriptions"`
	Worksets                  []V3RealtimeWorksetSubscriptionRequest         `json:"worksets"`
	SessionOrder              []string                                       `json:"session_order"`
	DiagnosticsBySession      map[string][]sessionsV3ReconnectDiagnostic     `json:"diagnostics_by_session"`
	MessagesBySession         map[string]any                                 `json:"messages_by_session"`
	EventsBySession           map[string]any                                 `json:"events_by_session"`
	PlansBySession            map[string]any                                 `json:"plans_by_session"`
	PlanRevisionsBySession    map[string]any                                 `json:"plan_revisions_by_session"`
	ClientID                  string                                         `json:"client_id"`
	Surface                   string                                         `json:"surface"`
	WorksetID                 string                                         `json:"workset_id"`
	Realtime                  sessionsV3ReconnectRealtimeInstructionTestWire `json:"realtime"`
}

type sessionsV3ReconnectRealtimeInstructionTestWire struct {
	StreamPath string            `json:"stream_path"`
	Resume     V3RealtimeMessage `json:"resume"`
}

func TestSessionsV3ReconnectIncludesPendingExecutorFromDurableRunIntent(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "reconnect-pending", "Reconnect Pending")
	recordSessionsV3ReconnectRunIntent(t, server, created.ID, "run-pending", sessionruntime.RunIntentPendingExecutor, 1000)

	payload := postSessionsV3Reconnect(t, server)
	if !payload.OK {
		t.Fatalf("reconnect ok = false")
	}
	if payload.SessionsByID[created.ID].ID != created.ID {
		t.Fatalf("pending session missing from sessions_by_id: %+v", payload.SessionsByID)
	}
	current := payload.CurrentRunIntentBySession[created.ID]
	if current.RunID != "run-pending" || current.Status != sessionruntime.RunIntentPendingExecutor {
		t.Fatalf("current intent = %+v", current)
	}
	assertSessionsV3ReconnectSubscription(t, payload, created.ID)
}

func TestSessionsV3ReconnectIncludesRunningAndExcludesTerminal(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runningSession := createSessionsV3PrimaryTestSession(t, server, "reconnect-running", "Reconnect Running")
	terminalSession := createSessionsV3PrimaryTestSession(t, server, "reconnect-terminal", "Reconnect Terminal")
	recordSessionsV3ReconnectRunIntent(t, server, runningSession.ID, "run-running", sessionruntime.RunIntentPendingExecutor, 1000)
	recordSessionsV3ReconnectRunIntent(t, server, runningSession.ID, "run-running", sessionruntime.RunIntentRunning, 2000)
	recordSessionsV3ReconnectRunIntent(t, server, terminalSession.ID, "run-terminal", sessionruntime.RunIntentPendingExecutor, 3000)
	recordSessionsV3ReconnectRunIntent(t, server, terminalSession.ID, "run-terminal", sessionruntime.RunIntentCompleted, 4000)

	payload := postSessionsV3Reconnect(t, server)
	if payload.SessionsByID[runningSession.ID].ID != runningSession.ID {
		t.Fatalf("running session missing: %+v", payload.SessionsByID)
	}
	if got := payload.CurrentRunIntentBySession[runningSession.ID]; got.RunID != "run-running" || got.Status != sessionruntime.RunIntentRunning {
		t.Fatalf("running current intent = %+v", got)
	}
	if _, ok := payload.SessionsByID[terminalSession.ID]; ok {
		t.Fatalf("terminal session must be inactive in reconnect response: %+v", payload.SessionsByID[terminalSession.ID])
	}
	assertSessionsV3ReconnectSubscription(t, payload, runningSession.ID)
}

func TestSessionsV3ReconnectExcludesLifecycleActiveOnlySession(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "reconnect-lifecycle", "Reconnect Lifecycle")
	recordSessionsV3ReconnectLifecycle(t, server, created.ID, true)

	payload := postSessionsV3Reconnect(t, server)
	if _, ok := payload.SessionsByID[created.ID]; ok {
		t.Fatalf("lifecycle.active-only session must not be active in reconnect response: %+v", payload.SessionsByID[created.ID])
	}
	if len(payload.Subscriptions) != 0 {
		t.Fatalf("subscriptions = %+v, want none", payload.Subscriptions)
	}
}

func TestSessionsV3ReconnectIncludesCanonicalActiveWithStaleInactiveLifecycle(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "reconnect-stale-lifecycle", "Reconnect Stale Lifecycle")
	recordSessionsV3ReconnectRunIntent(t, server, created.ID, "run-canonical", sessionruntime.RunIntentPendingExecutor, 1000)
	recordSessionsV3ReconnectRunIntent(t, server, created.ID, "run-canonical", sessionruntime.RunIntentRunning, 2000)
	recordSessionsV3ReconnectLifecycle(t, server, created.ID, false)

	payload := postSessionsV3Reconnect(t, server)
	if payload.SessionsByID[created.ID].ID != created.ID {
		t.Fatalf("canonical active session missing with stale inactive lifecycle: %+v", payload.SessionsByID)
	}
	current := payload.CurrentRunIntentBySession[created.ID]
	if current.RunID != "run-canonical" || current.Status != sessionruntime.RunIntentRunning {
		t.Fatalf("current canonical intent = %+v", current)
	}
	assertSessionsV3ReconnectSubscription(t, payload, created.ID)
}

func TestSessionsV3ReconnectOrdersSessionsDeterministically(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	older := createSessionsV3PrimaryTestSession(t, server, "reconnect-order-older", "Reconnect Order Older")
	newer := createSessionsV3PrimaryTestSession(t, server, "reconnect-order-newer", "Reconnect Order Newer")
	recordSessionsV3ReconnectRunIntent(t, server, older.ID, "run-older", sessionruntime.RunIntentPendingExecutor, 1000)
	recordSessionsV3ReconnectRunIntent(t, server, newer.ID, "run-newer", sessionruntime.RunIntentPendingExecutor, 2000)

	payload := postSessionsV3Reconnect(t, server)
	if len(payload.SessionOrder) < 2 || payload.SessionOrder[0] != newer.ID || payload.SessionOrder[1] != older.ID {
		t.Fatalf("session_order = %+v, want newest active run intent first", payload.SessionOrder)
	}
}

func TestSessionsV3ReconnectWorksetContractIncludesRealtimeResume(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "reconnect-workset-create", "Reconnect Workset", "/workspace/reconnect-workset")
	recordSessionsV3ReconnectRunIntent(t, server, created.ID, "run-workset", sessionruntime.RunIntentPendingExecutor, 2000)
	recordSessionsV3ReconnectRunIntent(t, server, created.ID, "run-workset", sessionruntime.RunIntentRunning, 3000)

	payload := postSessionsV3ReconnectBody(t, server, `{
		"surface":"desktop",
		"client_id":"desktop-client-1",
		"workset":{
			"workset_id":"desktop:global",
			"selector":{"kind":"global","global":true,"recent":{"limit":10}},
			"resources":{"messages":true,"events":true,"run_intents":true},
			"include_active":true,
			"auto_subscribe_sessions":true
		}
	}`)
	if payload.ClientID != "desktop-client-1" || payload.Surface != "desktop" || payload.WorksetID != "desktop:global" {
		t.Fatalf("client/workset identity = client %q surface %q workset %q", payload.ClientID, payload.Surface, payload.WorksetID)
	}
	if payload.SessionsByID[created.ID].ID != created.ID {
		t.Fatalf("workset reconnect missing created session: %+v", payload.SessionsByID)
	}
	if payload.SnapshotEndpointCursor == "" {
		t.Fatalf("workset reconnect missing snapshot_endpoint_cursor")
	}
	if len(payload.Worksets) != 1 {
		t.Fatalf("worksets = %+v, want one workset subscription", payload.Worksets)
	}
	workset := payload.Worksets[0]
	if workset.WorksetID != "desktop:global" || workset.SubscriptionID == "" || !workset.AutoSubscribeSessions || workset.Selector.Kind != "global" || !workset.Selector.Global {
		t.Fatalf("workset subscription = %+v", workset)
	}
	assertSessionsV3ReconnectSubscription(t, payload, created.ID)
	if payload.Realtime.StreamPath != V3RealtimeStreamPath {
		t.Fatalf("realtime stream_path = %q", payload.Realtime.StreamPath)
	}
	resume := payload.Realtime.Resume
	if resume.Protocol != V3RealtimeProtocol || resume.ProtocolVersion != V3RealtimeProtocolVersion || resume.Kind != V3RealtimeKindResume || resume.EndpointCursor != payload.SnapshotEndpointCursor {
		t.Fatalf("resume frame = %+v snapshot=%q", resume, payload.SnapshotEndpointCursor)
	}
	if len(resume.Worksets) != 1 || resume.Worksets[0].WorksetID != "desktop:global" || !resume.Worksets[0].AutoSubscribeSessions {
		t.Fatalf("resume worksets = %+v", resume.Worksets)
	}
	if len(resume.Subscriptions) == 0 || resume.Subscriptions[0].SessionID == "" || resume.Subscriptions[0].EndpointCursor != payload.SnapshotEndpointCursor {
		t.Fatalf("resume subscriptions = %+v snapshot=%q", resume.Subscriptions, payload.SnapshotEndpointCursor)
	}
	if err := ValidateV3RealtimeMessage(resume); err != nil {
		t.Fatalf("reconnect realtime resume rejected by contract: %v", err)
	}
	for _, resource := range resume.Worksets[0].Resources {
		if !v3RealtimeWorksetResourceAllowed(resource) {
			t.Fatalf("resume included sync-only resource %q in %+v", resource, resume.Worksets[0].Resources)
		}
	}
	for _, forbidden := range []string{"permissions", "usage", "preferences", "agent_model_policy"} {
		if sessionsV3ReconnectContainsString(resume.Worksets[0].Resources, forbidden) {
			t.Fatalf("resume included websocket-unsupported resource %q in %+v", forbidden, resume.Worksets[0].Resources)
		}
	}
}

func TestSessionsV3ReconnectRealtimeResumeAcceptedByStreamUnmodified(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "reconnect-resume-unmodified", "Reconnect Resume Unmodified", "/workspace/reconnect-resume")
	recordSessionsV3ReconnectRunIntent(t, server, created.ID, "run-resume-unmodified", sessionruntime.RunIntentPendingExecutor, 1000)

	payload := postSessionsV3ReconnectBody(t, server, `{
		"surface":"desktop",
		"client_id":"desktop-client-resume-unmodified",
		"workset":{
			"workset_id":"desktop:global",
			"selector":{"kind":"global","global":true,"recent":{"limit":10}},
			"resources":{"messages":true,"events":true,"run_intents":true},
			"include_active":true,
			"auto_subscribe_sessions":true
		}
	}`)
	resume := payload.Realtime.Resume
	if len(resume.Worksets) != 1 {
		t.Fatalf("resume worksets = %+v, want one", resume.Worksets)
	}
	if sessionsV3ReconnectContainsString(resume.Worksets[0].Resources, "permissions") {
		t.Fatalf("unmodified reconnect resume still contains permissions: %+v", resume.Worksets[0].Resources)
	}

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()

	hello := readV3RealtimeFrameIncludingHello(t, conn)
	if hello.Kind != V3RealtimeKindHello {
		t.Fatalf("first realtime frame = %+v, want hello", hello)
	}
	writeV3RealtimeMessage(t, conn, resume)
	for i := 0; i < 4; i++ {
		frame := readV3RealtimeFrame(t, conn)
		if frame.Kind == V3RealtimeKindAuthDenied {
			t.Fatalf("unmodified reconnect resume rejected by stream: %+v", frame)
		}
		if frame.Kind == V3RealtimeKindReplayStart || frame.Kind == V3RealtimeKindReplayDone || frame.Kind == V3RealtimeKindEndpointWatermark {
			return
		}
	}
	t.Fatalf("stream did not accept unmodified reconnect resume before timeout")
}

func TestSessionsV3ReconnectGlobalWorksetMatchesBootstrapPrincipalScope(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	store := server.sessions.Store()
	seedSessionsV3ReconnectStoreSession(t, store, "reconnect-global-old", testPrincipal().UserID, testPrincipal().AccountScopeID, "/workspace/reconnect-global-old", 1000)
	seedSessionsV3ReconnectStoreSession(t, store, "reconnect-global-new", testPrincipal().UserID, testPrincipal().AccountScopeID, "/workspace/reconnect-global-new", 3000)
	seedSessionsV3ReconnectStoreSession(t, store, "reconnect-global-mid", testPrincipal().UserID, testPrincipal().AccountScopeID, "/workspace/reconnect-global-mid", 2000)
	seedSessionsV3ReconnectStoreSession(t, store, "reconnect-global-other-user", "other-user", testPrincipal().AccountScopeID, "/workspace/reconnect-global-other-user", 4000)
	seedSessionsV3ReconnectStoreSession(t, store, "reconnect-global-blank-user", "", testPrincipal().AccountScopeID, "/workspace/reconnect-global-blank-user", 5000)
	seedSessionsV3ReconnectStoreSession(t, store, "reconnect-global-other-account", testPrincipal().UserID, "other-account", "/workspace/reconnect-global-other-account", 6000)

	bootstrapRaw := postSessionsV3SyncBootstrapRawBytes(t, server, `{"surface":"desktop","selector":{"kind":"global","global":true},"history":{"mode":"none"},"resources":{"run_intents":true},"include_active":true}`)
	var bootstrap struct {
		SessionOrder []string `json:"session_order"`
	}
	if err := json.Unmarshal(bootstrapRaw, &bootstrap); err != nil {
		t.Fatalf("decode bootstrap response: %v", err)
	}

	reconnect := postSessionsV3ReconnectBody(t, server, `{
		"surface":"desktop",
		"client_id":"desktop-client-global",
		"workset":{
			"workset_id":"desktop:global",
			"selector":{"kind":"global","global":true},
			"history":{"mode":"none"},
			"resources":{"run_intents":true},
			"include_active":true,
			"auto_subscribe_sessions":true
		}
	}`)

	want := []string{"reconnect-global-new", "reconnect-global-mid", "reconnect-global-old"}
	if !stringSlicesEqual(reconnect.SessionOrder, want) {
		t.Fatalf("global reconnect order = %+v, want %+v", reconnect.SessionOrder, want)
	}
	if !stringSlicesEqual(reconnect.SessionOrder, bootstrap.SessionOrder) {
		t.Fatalf("global reconnect order = %+v, bootstrap order = %+v", reconnect.SessionOrder, bootstrap.SessionOrder)
	}
	for _, sessionID := range want {
		if reconnect.SessionsByID[sessionID].ID != sessionID {
			t.Fatalf("global reconnect sessions_by_id missing visible principal session %s: %+v", sessionID, reconnect.SessionsByID)
		}
	}
	for _, leaked := range []string{"reconnect-global-other-user", "reconnect-global-blank-user", "reconnect-global-other-account"} {
		if _, ok := reconnect.SessionsByID[leaked]; ok {
			t.Fatalf("global reconnect leaked %s: %+v", leaked, reconnect.SessionOrder)
		}
	}
	if len(reconnect.Subscriptions) != 0 {
		t.Fatalf("global reconnect subscriptions = %+v, want none without active runs", reconnect.Subscriptions)
	}
	if len(reconnect.Realtime.Resume.Subscriptions) != 0 {
		t.Fatalf("global reconnect resume subscriptions = %+v, want none without active runs", reconnect.Realtime.Resume.Subscriptions)
	}
	if len(reconnect.Realtime.Resume.Worksets) != 1 || reconnect.Realtime.Resume.Worksets[0].Selector.Kind != "global" || !reconnect.Realtime.Resume.Worksets[0].Selector.Global {
		t.Fatalf("global reconnect resume worksets = %+v, want one global workset", reconnect.Realtime.Resume.Worksets)
	}
}

func TestSessionsV3ReconnectGlobalWorksetReturnsFullMembershipButOnlyActiveSubscriptions(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	store := server.sessions.Store()

	for i := 0; i < 300; i++ {
		sessionID := fmt.Sprintf("reconnect-global-inactive-%03d", i)
		seedSessionsV3ReconnectStoreSession(t, store, sessionID, testPrincipal().UserID, testPrincipal().AccountScopeID, fmt.Sprintf("/workspace/reconnect-global-inactive-%03d", i), int64(1000+i))
	}
	activeA := "reconnect-global-active-a"
	activeB := "reconnect-global-active-b"
	seedSessionsV3ReconnectStoreSession(t, store, activeA, testPrincipal().UserID, testPrincipal().AccountScopeID, "/workspace/reconnect-global-active-a", 5000)
	seedSessionsV3ReconnectStoreSession(t, store, activeB, testPrincipal().UserID, testPrincipal().AccountScopeID, "/workspace/reconnect-global-active-b", 6000)
	seedSessionsV3ReconnectStoreSession(t, store, "reconnect-global-active-other-user", "other-user", testPrincipal().AccountScopeID, "/workspace/reconnect-global-active-other-user", 7000)
	seedSessionsV3ReconnectStoreSession(t, store, "reconnect-global-active-other-account", testPrincipal().UserID, "other-account", "/workspace/reconnect-global-active-other-account", 8000)
	recordSessionsV3ReconnectRunIntent(t, server, activeA, "run-active-a", sessionruntime.RunIntentPendingExecutor, 9000)
	recordSessionsV3ReconnectRunIntent(t, server, activeB, "run-active-b", sessionruntime.RunIntentPendingExecutor, 9050)
	recordSessionsV3ReconnectRunIntent(t, server, activeB, "run-active-b", sessionruntime.RunIntentRunning, 9100)
	recordSessionsV3ReconnectRunIntent(t, server, "reconnect-global-active-other-user", "run-other-user", sessionruntime.RunIntentPendingExecutor, 9150)
	recordSessionsV3ReconnectRunIntent(t, server, "reconnect-global-active-other-user", "run-other-user", sessionruntime.RunIntentRunning, 9200)
	recordSessionsV3ReconnectRunIntent(t, server, "reconnect-global-active-other-account", "run-other-account", sessionruntime.RunIntentPendingExecutor, 9250)
	recordSessionsV3ReconnectRunIntent(t, server, "reconnect-global-active-other-account", "run-other-account", sessionruntime.RunIntentRunning, 9300)

	reconnect := postSessionsV3ReconnectBody(t, server, `{
		"surface":"desktop",
		"client_id":"desktop-client-global-active",
		"workset":{
			"workset_id":"desktop:global",
			"selector":{"kind":"global","global":true},
			"history":{"mode":"none"},
			"resources":{"run_intents":true},
			"include_active":true,
			"auto_subscribe_sessions":true
		}
	}`)

	if len(reconnect.SessionOrder) != 302 {
		t.Fatalf("session_order len = %d, want 302", len(reconnect.SessionOrder))
	}
	if len(reconnect.SessionsByID) != 302 {
		t.Fatalf("sessions_by_id len = %d, want 302", len(reconnect.SessionsByID))
	}
	if len(reconnect.Subscriptions) != 2 {
		t.Fatalf("subscriptions = %+v, want exactly two active sessions", reconnect.Subscriptions)
	}
	if len(reconnect.Realtime.Resume.Subscriptions) != 2 {
		t.Fatalf("resume subscriptions = %+v, want exactly two active sessions", reconnect.Realtime.Resume.Subscriptions)
	}
	if got := sessionsV3ReconnectResumeSubscriptionIDs(reconnect.Realtime.Resume.Subscriptions); !stringSlicesEqual(got, []string{activeB, activeA}) {
		t.Fatalf("resume subscription session IDs = %+v, want [%s %s]", got, activeB, activeA)
	}
	if got := sessionsV3ReconnectSubscriptionIDs(reconnect.Subscriptions); !stringSlicesEqual(got, []string{activeB, activeA}) {
		t.Fatalf("subscription session IDs = %+v, want [%s %s]", got, activeB, activeA)
	}
	for i := 0; i < 300; i++ {
		inactiveID := fmt.Sprintf("reconnect-global-inactive-%03d", i)
		for _, subscribedID := range sessionsV3ReconnectSubscriptionIDs(reconnect.Subscriptions) {
			if subscribedID == inactiveID {
				t.Fatalf("inactive session %s was explicitly subscribed: %+v", inactiveID, reconnect.Subscriptions)
			}
		}
	}
	if len(reconnect.Realtime.Resume.Worksets) != 1 || reconnect.Realtime.Resume.Worksets[0].Selector.Kind != "global" {
		t.Fatalf("resume worksets = %+v, want one global workset", reconnect.Realtime.Resume.Worksets)
	}
}

func TestSessionsV3ReconnectExplicitSessionIDsRemainExplicit(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	store := server.sessions.Store()
	seedSessionsV3ReconnectStoreSession(t, store, "reconnect-explicit-inactive", testPrincipal().UserID, testPrincipal().AccountScopeID, "/workspace/reconnect-explicit-inactive", 1000)

	reconnect := postSessionsV3ReconnectBody(t, server, `{
		"surface":"desktop",
		"client_id":"desktop-client-explicit",
		"workset":{
			"workset_id":"desktop:explicit",
			"selector":{"kind":"session_ids","session_ids":["reconnect-explicit-inactive"]},
			"history":{"mode":"none"},
			"resources":{"run_intents":true},
			"auto_subscribe_sessions":true
		}
	}`)

	if !stringSlicesEqual(reconnect.SessionOrder, []string{"reconnect-explicit-inactive"}) {
		t.Fatalf("explicit session_order = %+v", reconnect.SessionOrder)
	}
	if got := sessionsV3ReconnectSubscriptionIDs(reconnect.Subscriptions); !stringSlicesEqual(got, []string{"reconnect-explicit-inactive"}) {
		t.Fatalf("explicit subscriptions = %+v, want one requested inactive session", reconnect.Subscriptions)
	}
	if got := len(reconnect.Realtime.Resume.Subscriptions); got != 1 {
		t.Fatalf("explicit resume subscriptions len = %d, want 1", got)
	}
	if got := sessionsV3ReconnectResumeSubscriptionIDs(reconnect.Realtime.Resume.Subscriptions); !stringSlicesEqual(got, []string{"reconnect-explicit-inactive"}) {
		t.Fatalf("explicit resume subscription IDs = %+v, want [reconnect-explicit-inactive]", got)
	}
}

func TestSessionsV3ReconnectWorksetRequiresClientID(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions:reconnect", bytes.NewBufferString(`{"workset":{"selector":{"kind":"global","global":true,"recent":{"limit":10}},"auto_subscribe_sessions":true}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("reconnect without client_id status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestSessionsV3ReconnectWorksetCursorEqualsWorksetSnapshotRevision(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	seed := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "reconnect-snapshot-seed", "Reconnect Snapshot Seed", "/workspace/reconnect-snapshot")

	var racing sessionruntime.SessionMutationResult
	cleanup := pebblestore.SetV3SessionWorksetAfterSnapshotHookForTest(func() {
		racing = appendV3RealtimeTestMessage(t, server, seed.ID, "message-reconnect-racing", "racing mutation")
	})
	defer cleanup()

	payload := postSessionsV3ReconnectBody(t, server, `{
		"surface":"desktop",
		"client_id":"desktop-client-cursor",
		"workset":{
			"workset_id":"desktop:workspace:cursor",
			"selector":{"kind":"workspace","workspace_path":"/workspace/reconnect-snapshot"},
			"resources":{"run_intents":true},
			"include_active":true,
			"auto_subscribe_sessions":true
		}
	}`)
	if racing.RealtimeOutbox == nil {
		t.Fatalf("test setup did not create racing outbox")
	}
	if payload.Rev >= racing.RealtimeOutbox.EndpointSeq {
		t.Fatalf("test setup failed: payload rev=%d racing endpoint=%d", payload.Rev, racing.RealtimeOutbox.EndpointSeq)
	}
	assertV3RealtimeSignedCursorSeq(t, server, payload.SnapshotEndpointCursor, payload.Rev)
	if payload.Realtime.Resume.EndpointCursor != payload.SnapshotEndpointCursor {
		t.Fatalf("resume cursor = %q, want snapshot %q", payload.Realtime.Resume.EndpointCursor, payload.SnapshotEndpointCursor)
	}
	for _, sub := range payload.Subscriptions {
		if sub.EndpointCursor != payload.SnapshotEndpointCursor {
			t.Fatalf("subscription cursor = %q, want %q", sub.EndpointCursor, payload.SnapshotEndpointCursor)
		}
	}
	for _, sub := range payload.Realtime.Resume.Subscriptions {
		if sub.EndpointCursor != payload.SnapshotEndpointCursor {
			t.Fatalf("resume subscription cursor = %q, want %q", sub.EndpointCursor, payload.SnapshotEndpointCursor)
		}
	}

	payload.Realtime.Resume.Worksets[0].Resources = []string{"sessions", "projections", "run_intents"}
	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()
	writeV3RealtimeMessage(t, conn, payload.Realtime.Resume)
	var replayed bool
	for i := 0; i < 4; i++ {
		frame := readV3RealtimeFrame(t, conn)
		if frame.Kind == V3RealtimeKindEvent && frame.Event != nil && frame.Event.ID == racing.Event.ID {
			replayed = true
			break
		}
	}
	if !replayed {
		t.Fatalf("racing mutation was not replayed after snapshot cursor")
	}
}

func TestSessionsV3ReconnectMetadataOnlyOmitsUnrequestedResourceMaps(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "reconnect-metadata-only", "Reconnect Metadata Only", "/workspace/reconnect-metadata")

	raw := postSessionsV3ReconnectRawMap(t, server, `{
		"surface":"desktop",
		"client_id":"desktop-client-metadata",
		"workset":{
			"workset_id":"desktop:metadata",
			"selector":{"kind":"workspace","workspace_path":"/workspace/reconnect-metadata"},
			"history":{"mode":"none"},
			"resources":{"messages":false,"events":false,"run_intents":false,"active_plan":false,"plan_revisions":false},
			"include_active":false,
			"auto_subscribe_sessions":true
		}
	}`)
	for _, key := range []string{"messages_by_session", "events_by_session", "run_intents_by_session", "permissions_by_session", "usage_by_session", "plans_by_session", "plan_revisions_by_session", "preferences_by_session", "agent_model_policy_by_session"} {
		if _, ok := raw[key]; ok {
			t.Fatalf("metadata-only reconnect included %s: %+v", key, raw[key])
		}
	}
}

func TestSessionsV3ReconnectWorksetSessionShellOmitsSettingsOnlyMetadata(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "reconnect-metadata-shell", "Reconnect Metadata Shell", "/workspace/reconnect-metadata-shell")

	body := `{"metadata":{"parent_session_id":"parent-1","lineage_kind":"delegated_subagent","agent_profile":{"name":"forbidden"},"tool_contract":{"preset":"forbidden"},"tool_scope":{"preset":"forbidden"},"prompt":"forbidden","provider":"forbidden","model":"forbidden","purpose":"client-only"}}`
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/metadata", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("metadata update status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	raw := postSessionsV3ReconnectRawMap(t, server, fmt.Sprintf(`{
		"surface":"desktop",
		"client_id":"desktop-client-metadata-shell",
		"workset":{
			"workset_id":"desktop:metadata-shell",
			"selector":{"kind":"session_ids","session_ids":[%q]},
			"history":{"mode":"none"},
			"resources":{"messages":false,"events":false,"run_intents":false,"active_plan":false,"plan_revisions":false},
			"include_active":false,
			"auto_subscribe_sessions":true
		}
	}`, created.ID))
	sessionsByID, ok := raw["sessions_by_id"].(map[string]any)
	if !ok {
		t.Fatalf("sessions_by_id missing or wrong type: %+v", raw["sessions_by_id"])
	}
	session, ok := sessionsByID[created.ID].(map[string]any)
	if !ok {
		t.Fatalf("created session missing from sessions_by_id: %+v", sessionsByID)
	}
	metadata, ok := session["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("session metadata missing or wrong type: %+v", session["metadata"])
	}
	for _, key := range []string{"agent_profile", "tool_contract", "tool_scope", "prompt", "provider", "model", "purpose"} {
		if _, ok := metadata[key]; ok {
			t.Fatalf("reconnect shell leaked settings-only metadata %s: %+v", key, metadata)
		}
	}
	if metadata["agent_name"] != "swarm" || metadata["runtime_mode"] == "" || metadata["parent_session_id"] != "parent-1" || metadata["lineage_kind"] != "delegated_subagent" {
		t.Fatalf("reconnect shell dropped required identity metadata: %+v", metadata)
	}
}

func TestSessionsV3ReconnectRequestedRunIntentsRemainAuthoritativeWithoutRemovedMaps(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "reconnect-empty-authoritative", "Reconnect Empty Authoritative", "/workspace/reconnect-empty")

	raw := postSessionsV3ReconnectRawMap(t, server, fmt.Sprintf(`{
		"surface":"desktop",
		"client_id":"desktop-client-empty",
		"workset":{
			"workset_id":"desktop:empty",
			"selector":{"kind":"session_ids","session_ids":[%q]},
			"history":{"mode":"none"},
			"resources":{"run_intents":true,"active_plan":true},
			"auto_subscribe_sessions":true
		}
	}`, created.ID))
	sessionsByID, ok := raw["sessions_by_id"].(map[string]any)
	if !ok || sessionsByID[created.ID] == nil {
		t.Fatalf("sessions_by_id missing created session %s: %+v", created.ID, raw["sessions_by_id"])
	}
	runIntents, ok := raw["run_intents_by_session"].(map[string]any)
	if !ok {
		t.Fatalf("run_intents_by_session missing or wrong type: %+v", raw["run_intents_by_session"])
	}
	if entries, ok := runIntents[created.ID].([]any); !ok || len(entries) != 0 {
		t.Fatalf("run_intents_by_session[%s] = %+v, want authoritative empty array (all=%+v)", created.ID, runIntents[created.ID], runIntents)
	}
	for _, key := range []string{"plans_by_session", "plan_revisions_by_session", "permissions_by_session", "usage_by_session", "preferences_by_session", "agent_model_policy_by_session"} {
		if _, ok := raw[key]; ok {
			t.Fatalf("reconnect emitted removed all-session resource %s: %+v", key, raw[key])
		}
	}
	if _, ok := raw["messages_by_session"]; ok {
		t.Fatalf("messages_by_session should remain omitted when unrequested")
	}
	if _, ok := raw["events_by_session"]; ok {
		t.Fatalf("events_by_session should remain omitted when unrequested")
	}
}

func postSessionsV3Reconnect(t *testing.T, server *Server) sessionsV3ReconnectTestPayload {
	t.Helper()
	return postSessionsV3ReconnectBody(t, server, `{}`)
}

func postSessionsV3ReconnectBody(t *testing.T, server *Server, body string) sessionsV3ReconnectTestPayload {
	t.Helper()
	raw := postSessionsV3ReconnectRawBytes(t, server, body)
	var payload sessionsV3ReconnectTestPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode reconnect response: %v", err)
	}
	return payload
}

func postSessionsV3ReconnectRawMap(t *testing.T, server *Server, body string) map[string]any {
	t.Helper()
	raw := postSessionsV3ReconnectRawBytes(t, server, body)
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode reconnect raw response: %v", err)
	}
	return payload
}

func postSessionsV3ReconnectRawBytes(t *testing.T, server *Server, body string) []byte {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions:reconnect", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("reconnect status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	return rec.Body.Bytes()
}

func postSessionsV3SyncBootstrapRawBytes(t *testing.T, server *Server, body string) []byte {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, V3SyncBootstrapPath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("sync bootstrap status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	return rec.Body.Bytes()
}

func sessionsV3ReconnectContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func seedSessionsV3ReconnectStoreSession(t *testing.T, store *pebblestore.SessionStore, sessionID, userID, accountScopeID, workspacePath string, updatedAt int64) {
	t.Helper()
	if err := store.CreateSession(pebblestore.SessionSnapshot{
		ID:             sessionID,
		UserID:         userID,
		AccountScopeID: accountScopeID,
		WorkspacePath:  workspacePath,
		WorkspaceName:  "workspace",
		Title:          sessionID,
		CreatedAt:      updatedAt,
		UpdatedAt:      updatedAt,
	}); err != nil {
		t.Fatalf("seed reconnect store session %s: %v", sessionID, err)
	}
}

func recordSessionsV3ReconnectRunIntent(t *testing.T, server *Server, sessionID, runID, status string, updatedAt int64) {
	t.Helper()
	clientRequestID := fmt.Sprintf("reconnect-%s-%s-%d", runID, status, updatedAt)
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     clientRequestID + "-hash",
		RequestHash:     clientRequestID + "-hash",
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		EventType:       "session.run_intent.recorded",
		RunIntent: &pebblestore.V3SessionRunIntent{
			RunID:     runID,
			Status:    status,
			UpdatedAt: updatedAt,
		},
		NowUnixMs: updatedAt,
	}); err != nil {
		t.Fatalf("record run intent %s/%s: %v", runID, status, err)
	}
}

func recordSessionsV3ReconnectLifecycle(t *testing.T, server *Server, sessionID string, active bool) {
	t.Helper()
	now := time.Now().UnixMilli()
	clientRequestID := fmt.Sprintf("reconnect-lifecycle-%s-%t", sessionID, active)
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     clientRequestID + "-hash",
		RequestHash:     clientRequestID + "-hash",
		Kind:            sessionruntime.SessionMutationUpsertLifecycle,
		EventType:       "session.lifecycle.updated",
		Lifecycle: &pebblestore.SessionLifecycleSnapshot{
			RunID:     "run-lifecycle-only",
			Active:    active,
			Phase:     "running",
			UpdatedAt: now,
		},
		NowUnixMs: now,
	}); err != nil {
		t.Fatalf("record lifecycle: %v", err)
	}
}

func sessionsV3ReconnectSubscriptionIDs(subscriptions []sessionsV3ReconnectSubscription) []string {
	out := make([]string, 0, len(subscriptions))
	for _, sub := range subscriptions {
		out = append(out, sub.SessionID)
	}
	return out
}

func sessionsV3ReconnectResumeSubscriptionIDs(subscriptions []V3RealtimeSubscriptionRequest) []string {
	out := make([]string, 0, len(subscriptions))
	for _, sub := range subscriptions {
		out = append(out, sub.SessionID)
	}
	return out
}

func assertSessionsV3ReconnectSubscription(t *testing.T, payload sessionsV3ReconnectTestPayload, sessionID string) {
	t.Helper()
	for _, sub := range payload.Subscriptions {
		if sub.SessionID != sessionID {
			continue
		}
		if sub.Protocol != V3RealtimeProtocol || sub.ProtocolVersion != V3RealtimeProtocolVersion || sub.Kind != V3RealtimeKindSubscribe {
			t.Fatalf("subscription protocol fields = %+v", sub)
		}
		if sub.SubscriptionID == "" || sub.EndpointCursor == "" || sub.EndpointCursor != payload.SnapshotEndpointCursor {
			t.Fatalf("subscription cursor/id = %+v, snapshot=%q", sub, payload.SnapshotEndpointCursor)
		}
		return
	}
	t.Fatalf("subscription missing for %q: %+v", sessionID, payload.Subscriptions)
}
