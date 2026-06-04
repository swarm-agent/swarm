package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gorillaws "github.com/gorilla/websocket"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
)

func TestSessionsV3PrimaryHandlersDoNotUseRuntimeDispatchOrRoutes(t *testing.T) {
	body, err := os.ReadFile("sessions_v3_primary.go")
	if err != nil {
		t.Fatalf("read sessions_v3_primary.go: %v", err)
	}
	for _, required := range []string{"ApplySessionMutation", "SessionMutationCreateSession", "SessionMutationAppendMessage", "RunIntentDispatchBlocked", "ReplaySessionEvents", "ListSessionEvents", "ListSessionMessages"} {
		if !strings.Contains(string(body), required) {
			t.Fatalf("sessions_v3_primary.go missing required V3 primary storage symbol %q", required)
		}
	}
	for _, forbidden := range []string{"proxyRoutedSessionRequest", "dispatchRuntime", "RuntimeSession", "routedSessionTarget", "SessionRoute", "CreateSessionWithOptions", "AppendMessage("} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("sessions_v3_primary.go contains forbidden runtime/route/legacy write symbol %q", forbidden)
		}
	}
}

func TestSessionsV3PrimaryCreateListHydrateUsesPrimaryStoreOnly(t *testing.T) {
	server, sessionSvc, _, routeStore, _ := newRoutedSessionTestServerWithSwarmStore(t)

	body := `{"client_request_id":"create-v3-1","workspace_path":"/workspace/v3","workspace_name":"v3","title":"V3 Primary","mode":"auto","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"},"metadata":{"purpose":"cp3"}}`
	createReq := httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewBufferString(body))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(createRec, withTestPrincipal(createReq))
	if createRec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d, body=%s", createRec.Code, http.StatusOK, createRec.Body.String())
	}

	var createPayload struct {
		OK         bool                                 `json:"ok"`
		Session    pebblestore.SessionSnapshot          `json:"session"`
		Projection sessionruntime.SessionProjection     `json:"projection"`
		Events     []sessionruntime.SessionEvent        `json:"events"`
		Messages   []pebblestore.MessageSnapshot        `json:"messages"`
		Mutation   sessionruntime.SessionMutationResult `json:"mutation"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &createPayload); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if !createPayload.OK || strings.TrimSpace(createPayload.Session.ID) == "" {
		t.Fatalf("create payload = %+v", createPayload)
	}
	if createPayload.Session.AccountScopeID != testPrincipal().AccountScopeID || createPayload.Session.UserID != testPrincipal().UserID {
		t.Fatalf("session principal = %+v", createPayload.Session)
	}
	if createPayload.Session.Title != "V3 Primary" || createPayload.Session.WorkspacePath != "/workspace/v3" || createPayload.Session.Metadata["purpose"] != "cp3" {
		t.Fatalf("session = %+v", createPayload.Session)
	}
	if createPayload.Projection.LastEventSeq != 1 || len(createPayload.Events) != 1 || createPayload.Events[0].EventType != "session.created" || len(createPayload.Messages) != 0 {
		t.Fatalf("projection/events/messages = %+v %+v %+v", createPayload.Projection, createPayload.Events, createPayload.Messages)
	}
	if createPayload.Mutation.FirstSeq != 1 || createPayload.Mutation.LastSeq != 1 || createPayload.Mutation.ResponseStatus != pebblestore.V3SessionMutationStatusCompleted {
		t.Fatalf("mutation result = %+v", createPayload.Mutation)
	}
	if _, ok, err := sessionSvc.GetSessionProjection(createPayload.Session.ID); err != nil || !ok {
		t.Fatalf("stored projection ok=%t err=%v", ok, err)
	}
	if routes, err := routeStore.List(10); err != nil || len(routes) != 0 {
		t.Fatalf("routes = %+v err=%v, want none", routes, err)
	}
	if _, ok, err := server.topology.GetSessionRouteForAccount(testPrincipal().AccountScopeID, createPayload.Session.ID); err != nil {
		t.Fatalf("get topology route: %v", err)
	} else if ok {
		t.Fatalf("unexpected topology session route for V3 primary session")
	}

	hydrateReq := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+createPayload.Session.ID, nil)
	hydrateRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(hydrateRec, withTestPrincipal(hydrateReq))
	if hydrateRec.Code != http.StatusOK {
		t.Fatalf("hydrate status = %d, want %d, body=%s", hydrateRec.Code, http.StatusOK, hydrateRec.Body.String())
	}
	var hydrated struct {
		OK       bool                          `json:"ok"`
		Session  pebblestore.SessionSnapshot   `json:"session"`
		Events   []sessionruntime.SessionEvent `json:"events"`
		Messages []pebblestore.MessageSnapshot `json:"messages"`
	}
	if err := json.Unmarshal(hydrateRec.Body.Bytes(), &hydrated); err != nil {
		t.Fatalf("decode hydrate response: %v", err)
	}
	if !hydrated.OK || hydrated.Session.ID != createPayload.Session.ID || len(hydrated.Events) != 1 || len(hydrated.Messages) != 0 {
		t.Fatalf("hydrated = %+v", hydrated)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/v3/sessions?limit=10", nil)
	listRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(listRec, withTestPrincipal(listReq))
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d, body=%s", listRec.Code, http.StatusOK, listRec.Body.String())
	}
	var listed struct {
		OK       bool `json:"ok"`
		Sessions []struct {
			Session pebblestore.SessionSnapshot `json:"session"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if !listed.OK || len(listed.Sessions) != 1 || listed.Sessions[0].Session.ID != createPayload.Session.ID {
		t.Fatalf("listed = %+v", listed)
	}
}

func TestSessionsV3PrimaryHydratesAfterStoreRestart(t *testing.T) {
	t.Setenv("SWARM_API_NO_AUTH", "1")
	storePath := filepath.Join(t.TempDir(), "sessions-v3-primary.pebble")
	server, _, closeStore := newSessionsV3PrimaryAPITestServer(t, storePath)

	createReq := httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewBufferString(`{"client_request_id":"restart-create","workspace_path":"/workspace/restart","title":"Restarted V3","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(createRec, withTestPrincipal(createReq))
	if createRec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d, body=%s", createRec.Code, http.StatusOK, createRec.Body.String())
	}
	var created struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if err := closeStore(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	restarted, _, closeRestarted := newSessionsV3PrimaryAPITestServer(t, storePath)
	defer func() { _ = closeRestarted() }()
	hydrateReq := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+created.Session.ID, nil)
	hydrateRec := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(hydrateRec, withTestPrincipal(hydrateReq))
	if hydrateRec.Code != http.StatusOK {
		t.Fatalf("hydrate after restart status = %d, want %d, body=%s", hydrateRec.Code, http.StatusOK, hydrateRec.Body.String())
	}
	var hydrated struct {
		Session    pebblestore.SessionSnapshot      `json:"session"`
		Projection sessionruntime.SessionProjection `json:"projection"`
		Events     []sessionruntime.SessionEvent    `json:"events"`
	}
	if err := json.Unmarshal(hydrateRec.Body.Bytes(), &hydrated); err != nil {
		t.Fatalf("decode hydrate response: %v", err)
	}
	if hydrated.Session.ID != created.Session.ID || hydrated.Projection.ProjectionHighWatermarkSeq != 1 || len(hydrated.Events) != 1 {
		t.Fatalf("hydrated after restart = %+v", hydrated)
	}
}

func TestSessionsV3PrimaryCreateRejectsProtectedAuthorityMetadata(t *testing.T) {
	server, sessionSvc, _, routeStore, _ := newRoutedSessionTestServerWithSwarmStore(t)
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewBufferString(`{"client_request_id":"protected-metadata","workspace_path":"/workspace/v3","metadata":{"runtime_swarm_id":"container-swarm"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "reserved") {
		t.Fatalf("body = %s, want reserved metadata error", rec.Body.String())
	}
	if sessions, err := sessionSvc.ListSessionsForAccount(testPrincipal().AccountScopeID, 10); err != nil || len(sessions) != 0 {
		t.Fatalf("sessions = %+v err=%v, want none", sessions, err)
	}
	if routes, err := routeStore.List(10); err != nil || len(routes) != 0 {
		t.Fatalf("routes = %+v err=%v, want none", routes, err)
	}
}

func TestSessionsV3PrimaryMessagesCommitUserMessageAndPendingExecutorIntent(t *testing.T) {
	server, sessionSvc, _, routeStore, _ := newRoutedSessionTestServerWithSwarmStore(t)
	create := httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewBufferString(`{"client_request_id":"cp4-create","workspace_path":"/workspace/cp4","title":"CP4"}`))
	create.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(createRec, withTestPrincipal(create))
	if createRec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d, body=%s", createRec.Code, http.StatusOK, createRec.Body.String())
	}
	var created struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	messageReq := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.Session.ID+"/messages", bytes.NewBufferString(`{"client_request_id":"cp4-message","role":"user","content":"hello cp4","metadata":{"purpose":"cp4"}}`))
	messageReq.Header.Set("Content-Type", "application/json")
	messageRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(messageRec, withTestPrincipal(messageReq))
	if messageRec.Code != http.StatusOK {
		t.Fatalf("message status = %d, want %d, body=%s", messageRec.Code, http.StatusOK, messageRec.Body.String())
	}
	var payload struct {
		OK         bool                                 `json:"ok"`
		Session    pebblestore.SessionSnapshot          `json:"session"`
		Projection sessionruntime.SessionProjection     `json:"projection"`
		Message    *pebblestore.MessageSnapshot         `json:"message"`
		RunIntent  *sessionruntime.SessionRunIntent     `json:"run_intent"`
		Messages   []pebblestore.MessageSnapshot        `json:"messages"`
		Events     []sessionruntime.SessionEvent        `json:"events"`
		Mutation   sessionruntime.SessionMutationResult `json:"mutation"`
	}
	if err := json.Unmarshal(messageRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode message response: %v", err)
	}
	if !payload.OK || payload.Message == nil || payload.Message.Content != "hello cp4" || payload.Message.Role != "user" {
		t.Fatalf("message payload = %+v", payload)
	}
	if payload.Session.MessageCount != 1 || payload.Projection.LastEventSeq != 2 || len(payload.Events) != 2 || len(payload.Messages) != 1 {
		t.Fatalf("session/projection/events/messages = %+v %+v %+v %+v", payload.Session, payload.Projection, payload.Events, payload.Messages)
	}
	if payload.RunIntent == nil || payload.RunIntent.Status != sessionruntime.RunIntentPendingExecutor || payload.RunIntent.BlockedReason != "" || payload.RunIntent.EventSeq != 2 {
		t.Fatalf("run intent = %+v", payload.RunIntent)
	}
	if payload.Mutation.FirstSeq != 2 || payload.Mutation.Message == nil || payload.Mutation.RunIntent == nil {
		t.Fatalf("mutation = %+v", payload.Mutation)
	}
	if routes, err := routeStore.List(10); err != nil || len(routes) != 0 {
		t.Fatalf("routes = %+v err=%v, want none", routes, err)
	}
	messages, err := sessionSvc.ListSessionMessages(created.Session.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Content != "hello cp4" || messages[0].Metadata["purpose"] != "cp4" {
		t.Fatalf("stored messages = %+v", messages)
	}
	getReq := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+created.Session.ID+"/messages?after_seq=0&limit=10", nil)
	getRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(getRec, withTestPrincipal(getReq))
	if getRec.Code != http.StatusOK {
		t.Fatalf("get messages status = %d, want %d, body=%s", getRec.Code, http.StatusOK, getRec.Body.String())
	}
	var listed struct {
		OK       bool                          `json:"ok"`
		Messages []pebblestore.MessageSnapshot `json:"messages"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode get messages response: %v", err)
	}
	if !listed.OK || len(listed.Messages) != 1 || listed.Messages[0].ID != messages[0].ID {
		t.Fatalf("listed messages = %+v", listed)
	}
}

func TestSessionsV3PrimaryMessageWithInvalidDispatchAuthorityStillCommitsAndBlocks(t *testing.T) {
	server, sessionSvc, _, routeStore, _ := newRoutedSessionTestServerWithSwarmStore(t)
	create := httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewBufferString(`{"client_request_id":"cp4-invalid-authority-create","workspace_path":"/workspace/cp4","title":"CP4"}`))
	create.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(createRec, withTestPrincipal(create))
	if createRec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d, body=%s", createRec.Code, http.StatusOK, createRec.Body.String())
	}
	var created struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	messageReq := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.Session.ID+"/messages", bytes.NewBufferString(`{"client_request_id":"cp4-invalid-authority-message","role":"user","content":"durable despite invalid authority","dispatch_authority":{"runtime_swarm_id":"container-swarm","authority_container_id":"container-1"}}`))
	messageReq.Header.Set("Content-Type", "application/json")
	messageRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(messageRec, withTestPrincipal(messageReq))
	if messageRec.Code != http.StatusOK {
		t.Fatalf("message status = %d, want %d, body=%s", messageRec.Code, http.StatusOK, messageRec.Body.String())
	}
	var payload struct {
		RunIntent *sessionruntime.SessionRunIntent `json:"run_intent"`
		Message   *pebblestore.MessageSnapshot     `json:"message"`
	}
	if err := json.Unmarshal(messageRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode message response: %v", err)
	}
	if payload.Message == nil || payload.Message.Content != "durable despite invalid authority" {
		t.Fatalf("message payload = %+v", payload.Message)
	}
	if payload.RunIntent == nil || payload.RunIntent.Status != sessionruntime.RunIntentDispatchBlocked || !strings.Contains(payload.RunIntent.BlockedReason, "runtime placement not found") {
		t.Fatalf("run intent = %+v", payload.RunIntent)
	}
	messages, err := sessionSvc.ListSessionMessages(created.Session.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Content != "durable despite invalid authority" {
		t.Fatalf("messages = %+v", messages)
	}
	if routes, err := routeStore.List(10); err != nil || len(routes) != 0 {
		t.Fatalf("routes = %+v err=%v, want none", routes, err)
	}
}

func TestSessionsV3PrimaryDispatchAuthorityRecordsSpecificBlockedReasonsWithoutBlockingMessage(t *testing.T) {
	baseAuthority := `"runtime_swarm_id":"host-swarm-id","runtime_kind":"host","workspace_binding_id":"binding-primary-v2","authority_host_swarm_id":"host-swarm-id","placement_generation":1,"binding_generation":1,"source_workspace_path":"/workspace/cp8","runtime_workspace_path":"/workspace/cp8"`
	tests := []struct {
		name       string
		authority  string
		mutate     func(t *testing.T, server *Server)
		wantStatus string
		want       string
	}{
		{
			name:       "accepted authority records pending executor intent",
			authority:  baseAuthority,
			wantStatus: sessionruntime.RunIntentPendingExecutor,
		},
		{
			name:      "stale binding generation",
			authority: baseAuthority,
			mutate: func(t *testing.T, server *Server) {
				mutateSessionsV2WorkspaceBinding(t, server, "binding-primary-v2", func(binding *pebblestore.TopologyWorkspaceBindingRecord) {
					binding.BindingGeneration = 2
				})
			},
			want: "workspace binding generation mismatch",
		},
		{
			name:      "unavailable target",
			authority: strings.ReplaceAll(baseAuthority, `"runtime_swarm_id":"host-swarm-id"`, `"runtime_swarm_id":"missing-swarm"`),
			want:      "runtime placement not found",
		},
		{
			name:      "placement authority mismatch",
			authority: strings.ReplaceAll(baseAuthority, `"authority_host_swarm_id":"host-swarm-id"`, `"authority_host_swarm_id":"other-host"`),
			want:      "placement authority host mismatch",
		},
		{
			name:      "account mismatch",
			authority: baseAuthority + `,"account_scope_id":"other-account"`,
			want:      "account mismatch",
		},
		{
			name:      "runtime kind mismatch",
			authority: strings.ReplaceAll(baseAuthority, `"runtime_kind":"host"`, `"runtime_kind":"container"`),
			want:      "runtime kind mismatch",
		},
		{
			name:      "missing executor runtime",
			authority: `"workspace_binding_id":"binding-primary-v2"`,
			want:      "missing executor runtime",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantStatus := tt.wantStatus
			if wantStatus == "" {
				wantStatus = sessionruntime.RunIntentDispatchBlocked
			}
			server, sessionSvc, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
			seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", "binding-primary-v2", "/workspace/cp8")
			if tt.mutate != nil {
				tt.mutate(t, server)
			}
			created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "cp8-create-"+strings.NewReplacer(" ", "-", "/", "-", "_", "-").Replace(tt.name), "CP8", "/workspace/cp8")
			body := fmt.Sprintf(`{"client_request_id":%q,"role":"user","content":"cp8 durable","dispatch_authority":{%s}}`, "cp8-message-"+strings.NewReplacer(" ", "-", "/", "-", "_", "-").Replace(tt.name), tt.authority)
			req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/messages", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(req))
			if rec.Code != http.StatusOK {
				t.Fatalf("message status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}
			var payload struct {
				RunIntent *sessionruntime.SessionRunIntent `json:"run_intent"`
				Message   *pebblestore.MessageSnapshot     `json:"message"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode message response: %v", err)
			}
			if payload.Message == nil || payload.Message.Content != "cp8 durable" {
				t.Fatalf("message payload = %+v", payload.Message)
			}
			if payload.RunIntent == nil || payload.RunIntent.Status != wantStatus || (tt.want != "" && !strings.Contains(payload.RunIntent.BlockedReason, tt.want)) {
				t.Fatalf("run intent = %+v, want status %q blocked reason containing %q", payload.RunIntent, wantStatus, tt.want)
			}
			messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
			if err != nil {
				t.Fatalf("list messages: %v", err)
			}
			if len(messages) != 1 || messages[0].Content != "cp8 durable" {
				t.Fatalf("messages = %+v", messages)
			}
			if routes, err := routeStore.List(10); err != nil || len(routes) != 0 {
				t.Fatalf("routes = %+v err=%v, want none", routes, err)
			}
		})
	}
}

func TestSessionsV3PrimaryFakeModelVerticalSlicePersistsAssistantOnce(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	server.v3SessionExecutor = newSessionV3Executor(server)
	created := createSessionsV3PrimaryTestSession(t, server, "fake-model-create", "fake model")
	body := `{"client_request_id":"fake-model-message","role":"user","content":"hello fake model"}`
	post := func() {
		req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/messages", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, withTestPrincipal(req))
		if rec.Code != http.StatusOK {
			t.Fatalf("post message status = %d body=%s", rec.Code, rec.Body.String())
		}
	}
	post()
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)
	post()
	if !server.WaitForInFlightRuns(2 * time.Second) {
		t.Fatalf("executor did not drain")
	}
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 2 || messages[0].Role != "user" || messages[1].Role != "assistant" || !strings.Contains(messages[1].Content, "hello fake model") {
		t.Fatalf("messages = %+v", messages)
	}
	listed := getSessionsV3PrimaryTestMessages(t, server, created.ID, 0, 10)
	if len(listed) != 2 || listed[0].Role != "user" || listed[0].Content != "hello fake model" || listed[1].Role != "assistant" || !strings.Contains(listed[1].Content, "hello fake model") {
		t.Fatalf("GET /v3/sessions/{id}/messages listed = %+v", listed)
	}
	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var assistantStarted, assistantCompleted int
	for _, event := range events {
		switch event.EventType {
		case "session.assistant.started":
			assistantStarted++
		case "session.assistant.completed":
			assistantCompleted++
		}
	}
	if assistantStarted != 1 || assistantCompleted != 1 {
		t.Fatalf("assistant events started=%d completed=%d events=%+v", assistantStarted, assistantCompleted, events)
	}
}

func TestSessionsV3ExecutorRejectsDuplicateEnqueueRun(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	exec := newSessionV3Executor(server)
	exec.startDelay = 250 * time.Millisecond
	server.v3SessionExecutor = exec
	job := sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: "v3session_duplicate_enqueue", RunID: "run-duplicate"}
	if !exec.EnqueueRun(job) {
		t.Fatalf("first enqueue returned false")
	}
	if exec.EnqueueRun(job) {
		t.Fatalf("duplicate enqueue returned true while original run was still queued/in-flight")
	}
	server.CancelInFlightRuns()
	if !server.WaitForInFlightRuns(2 * time.Second) {
		t.Fatalf("executor did not drain after cancellation")
	}
}

func TestSessionsV3PrimaryPostReturnsBeforeModelCompletion(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	exec.modelDelay = 500 * time.Millisecond
	server.v3SessionExecutor = exec
	created := createSessionsV3PrimaryTestSession(t, server, "nonblocking-create", "nonblocking")
	body := `{"client_request_id":"nonblocking-message","role":"user","content":"do not block on model"}`
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/messages", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	started := time.Now()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	elapsed := time.Since(started)
	if rec.Code != http.StatusOK {
		t.Fatalf("post message status = %d body=%s", rec.Code, rec.Body.String())
	}
	if elapsed >= 250*time.Millisecond {
		t.Fatalf("POST blocked for %s; want return before fake model delay", elapsed)
	}
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages after POST: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "user" || messages[0].Content != "do not block on model" {
		t.Fatalf("messages immediately after POST = %+v, want committed user only", messages)
	}
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)
	listed := getSessionsV3PrimaryTestMessages(t, server, created.ID, 0, 10)
	if len(listed) != 2 || listed[0].Role != "user" || listed[1].Role != "assistant" {
		t.Fatalf("GET /messages after completion = %+v", listed)
	}
}

func TestSessionsV3ExecutorEnabledByNormalServerStartup(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "normal-startup.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	server := NewServer(nil, nil, nil, nil, sessionSvc, nil, nil, nil, nil, nil, nil, eventLog, stream.NewHub(eventLog))
	defer func() {
		server.CancelInFlightRuns()
		server.WaitForInFlightRuns(2 * time.Second)
	}()
	if server.v3SessionExecutor == nil {
		t.Fatalf("normal NewServer startup with session service did not enable V3 session executor")
	}
}

func waitForSessionsV3MessageCount(t *testing.T, sessionSvc *sessionruntime.Service, sessionID string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		messages, err := sessionSvc.ListSessionMessages(sessionID, 0, 10)
		if err != nil {
			t.Fatalf("list messages: %v", err)
		}
		if len(messages) >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	messages, _ := sessionSvc.ListSessionMessages(sessionID, 0, 10)
	t.Fatalf("message count = %d, want %d: %+v", len(messages), want, messages)
}

func TestSessionsV3PrimaryMessageIsIdempotent(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	create := httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewBufferString(`{"client_request_id":"cp4-idempotent-create","workspace_path":"/workspace/cp4","title":"CP4"}`))
	create.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(createRec, withTestPrincipal(create))
	if createRec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d, body=%s", createRec.Code, http.StatusOK, createRec.Body.String())
	}
	var created struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	body := `{"client_request_id":"cp4-same-message","role":"user","content":"idempotent cp4"}`
	post := func() (int, string) {
		req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.Session.ID+"/messages", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, withTestPrincipal(req))
		return rec.Code, rec.Body.String()
	}
	status, firstBody := post()
	if status != http.StatusOK {
		t.Fatalf("first status = %d body=%s", status, firstBody)
	}
	status, secondBody := post()
	if status != http.StatusOK {
		t.Fatalf("second status = %d body=%s", status, secondBody)
	}
	var second struct {
		Mutation sessionruntime.SessionMutationResult `json:"mutation"`
		Events   []sessionruntime.SessionEvent        `json:"events"`
	}
	if err := json.Unmarshal([]byte(secondBody), &second); err != nil {
		t.Fatalf("decode second response: %v", err)
	}
	if !second.Mutation.Replayed || second.Mutation.PrimarySeq != 2 {
		t.Fatalf("second mutation = %+v", second.Mutation)
	}
	events, err := sessionSvc.ListSessionEvents(created.Session.ID, 0, 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) < 2 || len(second.Events) < 2 || events[1].EventType != "session.message.appended" {
		t.Fatalf("events persisted=%+v second=%+v", events, second.Events)
	}
}

func TestSessionsV3PrimaryConcurrentDistinctMessagesAllocateContiguousSeq(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	create := httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewBufferString(`{"client_request_id":"cp4-concurrent-create","workspace_path":"/workspace/cp4","title":"CP4"}`))
	create.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(createRec, withTestPrincipal(create))
	if createRec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d, body=%s", createRec.Code, http.StatusOK, createRec.Body.String())
	}
	var created struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	const workers = 12
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := fmt.Sprintf(`{"client_request_id":"cp4-concurrent-%02d","role":"user","content":"message %02d"}`, i, i)
			req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.Session.ID+"/messages", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(req))
			if rec.Code != http.StatusOK {
				errs <- fmt.Sprintf("message %d status = %d body=%s", i, rec.Code, rec.Body.String())
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	events, err := sessionSvc.ListSessionEvents(created.Session.ID, 0, workers+2)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != workers+1 {
		t.Fatalf("events = %d, want %d: %+v", len(events), workers+1, events)
	}
	for i, event := range events {
		wantSeq := uint64(i + 1)
		if event.Seq != wantSeq {
			t.Fatalf("event[%d].seq=%d want %d events=%+v", i, event.Seq, wantSeq, events)
		}
	}
	messages, err := sessionSvc.ListSessionMessages(created.Session.ID, 0, workers+1)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != workers {
		t.Fatalf("messages = %d, want %d: %+v", len(messages), workers, messages)
	}
	for i, message := range messages {
		wantSeq := uint64(i + 2)
		if message.GlobalSeq != wantSeq {
			t.Fatalf("message[%d].global_seq=%d want %d messages=%+v", i, message.GlobalSeq, wantSeq, messages)
		}
	}
}

func TestSessionsV3PrimaryEventsReplayCursorAndRestart(t *testing.T) {
	t.Setenv("SWARM_API_NO_AUTH", "1")
	storePath := filepath.Join(t.TempDir(), "sessions-v3-events.pebble")
	server, _, closeStore := newSessionsV3PrimaryAPITestServer(t, storePath)

	createReq := httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewBufferString(`{"client_request_id":"cp5-create","workspace_path":"/workspace/cp5","title":"CP5"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(createRec, withTestPrincipal(createReq))
	if createRec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d, body=%s", createRec.Code, http.StatusOK, createRec.Body.String())
	}
	var created struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	for i := 0; i < 2; i++ {
		body := fmt.Sprintf(`{"client_request_id":"cp5-message-%d","role":"user","content":"cp5 message %d"}`, i, i)
		messageReq := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.Session.ID+"/messages", bytes.NewBufferString(body))
		messageReq.Header.Set("Content-Type", "application/json")
		messageRec := httptest.NewRecorder()
		server.Handler().ServeHTTP(messageRec, withTestPrincipal(messageReq))
		if messageRec.Code != http.StatusOK {
			t.Fatalf("message %d status = %d, want %d, body=%s", i, messageRec.Code, http.StatusOK, messageRec.Body.String())
		}
	}
	if err := closeStore(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	restarted, _, closeRestarted := newSessionsV3PrimaryAPITestServer(t, storePath)
	defer func() { _ = closeRestarted() }()
	replayReq := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+created.Session.ID+"/events?after_seq=1&limit=1", nil)
	replayRec := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(replayRec, withTestPrincipal(replayReq))
	if replayRec.Code != http.StatusOK {
		t.Fatalf("events status = %d, want %d, body=%s", replayRec.Code, http.StatusOK, replayRec.Body.String())
	}
	var replay struct {
		OK               bool                              `json:"ok"`
		Events           []sessionruntime.SessionEvent     `json:"events"`
		Projection       sessionruntime.SessionProjection  `json:"projection"`
		Messages         []pebblestore.MessageSnapshot     `json:"messages"`
		RunIntents       []sessionruntime.SessionRunIntent `json:"run_intents"`
		HighWatermarkSeq uint64                            `json:"high_watermark_seq"`
		NextSeq          uint64                            `json:"next_seq"`
	}
	if err := json.Unmarshal(replayRec.Body.Bytes(), &replay); err != nil {
		t.Fatalf("decode replay response: %v", err)
	}
	if !replay.OK || len(replay.Events) != 1 || replay.Events[0].Seq != 2 || replay.HighWatermarkSeq != 2 || replay.NextSeq != 2 {
		t.Fatalf("replay page = %+v", replay)
	}
	if replay.Projection.LastEventSeq != 3 || replay.Projection.ProjectionHighWatermarkSeq != 3 {
		t.Fatalf("projection = %+v", replay.Projection)
	}
	if len(replay.Messages) != 1 || replay.Messages[0].Content != "cp5 message 0" {
		t.Fatalf("messages = %+v", replay.Messages)
	}
	if len(replay.RunIntents) != 1 || replay.RunIntents[0].EventSeq != 2 {
		t.Fatalf("run intents = %+v", replay.RunIntents)
	}
}

func TestSessionsV3PrimaryCreateIsIdempotent(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	body := `{"client_request_id":"same-create","workspace_path":"/workspace/v3","title":"V3 Idempotent","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`

	post := func() (int, string) {
		req := httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, withTestPrincipal(req))
		return rec.Code, rec.Body.String()
	}
	status, firstBody := post()
	if status != http.StatusOK {
		t.Fatalf("first status = %d body=%s", status, firstBody)
	}
	status, secondBody := post()
	if status != http.StatusOK {
		t.Fatalf("second status = %d body=%s", status, secondBody)
	}

	var first, second struct {
		Session  pebblestore.SessionSnapshot          `json:"session"`
		Events   []sessionruntime.SessionEvent        `json:"events"`
		Mutation sessionruntime.SessionMutationResult `json:"mutation"`
	}
	if err := json.Unmarshal([]byte(firstBody), &first); err != nil {
		t.Fatalf("decode first: %v", err)
	}
	if err := json.Unmarshal([]byte(secondBody), &second); err != nil {
		t.Fatalf("decode second: %v", err)
	}
	if first.Session.ID != second.Session.ID || !second.Mutation.Replayed {
		t.Fatalf("idempotent responses first=%+v second=%+v", first, second)
	}
	events, err := sessionSvc.ListSessionEvents(first.Session.ID, 0, 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 || len(second.Events) != 1 {
		t.Fatalf("events persisted=%+v second=%+v", events, second.Events)
	}
}

func TestSessionsV3PrimaryStreamReplaysDurableEventsAfterRestart(t *testing.T) {
	t.Setenv("SWARM_API_NO_AUTH", "1")
	storePath := filepath.Join(t.TempDir(), "sessions-v3-stream.pebble")
	server, _, closeStore := newSessionsV3PrimaryAPITestServer(t, storePath)
	created := createSessionsV3PrimaryTestSession(t, server, "cp6-create", "CP6")
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "cp6-message-0", "cp6 message 0")
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "cp6-message-1", "cp6 message 1")
	if err := closeStore(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	restarted, _, closeRestarted := newSessionsV3PrimaryAPITestServer(t, storePath)
	defer func() { _ = closeRestarted() }()
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		restarted.Handler().ServeHTTP(w, withTestPrincipal(r))
	}))
	defer httpServer.Close()

	conn := dialSessionsV3PrimaryStream(t, httpServer.URL, created.ID, "after_seq=1")
	defer conn.Close()
	started := readSessionsV3PrimaryStreamFrame(t, conn)
	if started.Type != "replay.started" || !started.OK || started.HighWatermarkSeq != 3 {
		t.Fatalf("replay started = %+v", started)
	}
	first := readSessionsV3PrimaryStreamFrame(t, conn)
	second := readSessionsV3PrimaryStreamFrame(t, conn)
	if first.Type != "event" || first.Event == nil || first.Event.Seq != 2 || second.Event == nil || second.Event.Seq != 3 {
		t.Fatalf("stream replay events first=%+v second=%+v", first, second)
	}
	complete := readSessionsV3PrimaryStreamFrame(t, conn)
	if complete.Type != "replay.complete" || complete.LastSeq != 3 || complete.NextSeq != 3 {
		t.Fatalf("replay complete = %+v", complete)
	}
}

func TestSessionsV3PrimaryStreamTransitionsFromReplayToLiveEvents(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "cp6-live-create", "CP6 live")
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.Handler().ServeHTTP(w, withTestPrincipal(r))
	}))
	defer httpServer.Close()

	conn := dialSessionsV3PrimaryStream(t, httpServer.URL, created.ID, "after_seq=1")
	defer conn.Close()
	started := readSessionsV3PrimaryStreamFrame(t, conn)
	complete := readSessionsV3PrimaryStreamFrame(t, conn)
	if started.Type != "replay.started" || complete.Type != "replay.complete" || complete.LastSeq != 1 {
		t.Fatalf("initial frames started=%+v complete=%+v", started, complete)
	}

	postSessionsV3PrimaryTestMessage(t, server, created.ID, "cp6-live-message", "cp6 live message")
	live := readSessionsV3PrimaryStreamFrame(t, conn)
	if live.Type != "event" || live.Event == nil || live.Event.Seq != 2 || live.Event.EventType != "session.message.appended" {
		t.Fatalf("live event = %+v", live)
	}
}

func TestSessionsV3PrimaryStreamCursorErrors(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "cp6-cursor-create", "CP6 cursor")
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.Handler().ServeHTTP(w, withTestPrincipal(r))
	}))
	defer httpServer.Close()

	ahead := dialSessionsV3PrimaryStream(t, httpServer.URL, created.ID, "after_seq=2")
	aheadFrame := readSessionsV3PrimaryStreamFrame(t, ahead)
	_ = ahead.Close()
	if aheadFrame.Type != "cursor.error" || aheadFrame.OK || !strings.Contains(aheadFrame.Error, "refetch required") {
		t.Fatalf("ahead cursor frame = %+v", aheadFrame)
	}

	malformed := dialSessionsV3PrimaryStream(t, httpServer.URL, created.ID, "after_seq=not-a-number")
	malformedFrame := readSessionsV3PrimaryStreamFrame(t, malformed)
	_ = malformed.Close()
	if malformedFrame.Type != "error" || !strings.Contains(malformedFrame.Error, "after_seq") {
		t.Fatalf("malformed cursor frame = %+v", malformedFrame)
	}
}

func TestSessionsV3PrimaryStandalonePathsWorkWithDispatchServicesDisabled(t *testing.T) {
	t.Setenv("SWARM_API_NO_AUTH", "1")
	storePath := filepath.Join(t.TempDir(), "sessions-v3-standalone.pebble")
	server, _, closeStore := newSessionsV3PrimaryAPITestServer(t, storePath)
	defer func() { _ = closeStore() }()

	created := createSessionsV3PrimaryTestSession(t, server, "cp8-standalone-create", "CP8 standalone")
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "cp8-standalone-message", "standalone message")
	for _, path := range []string{
		"/v3/sessions?limit=10",
		"/v3/sessions/" + created.ID,
		"/v3/sessions/" + created.ID + "/messages?after_seq=0&limit=10",
		"/v3/sessions/" + created.ID + "/events?after_seq=0&limit=10",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, withTestPrincipal(req))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want %d, body=%s", path, rec.Code, http.StatusOK, rec.Body.String())
		}
	}

	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.Handler().ServeHTTP(w, withTestPrincipal(r))
	}))
	defer httpServer.Close()
	conn := dialSessionsV3PrimaryStream(t, httpServer.URL, created.ID, "after_seq=1")
	defer conn.Close()
	started := readSessionsV3PrimaryStreamFrame(t, conn)
	event := readSessionsV3PrimaryStreamFrame(t, conn)
	complete := readSessionsV3PrimaryStreamFrame(t, conn)
	if started.Type != "replay.started" || event.Type != "event" || event.Event == nil || event.Event.Seq != 2 || complete.Type != "replay.complete" {
		t.Fatalf("stream frames started=%+v event=%+v complete=%+v", started, event, complete)
	}
}

func TestSessionsV3PrimaryStreamHandlerDoesNotUseV2RunStreamOrRuntime(t *testing.T) {
	body, err := os.ReadFile("sessions_v3_stream_ws.go")
	if err != nil {
		t.Fatalf("read sessions_v3_stream_ws.go: %v", err)
	}
	for _, required := range []string{"ReplaySessionEvents", "GetSessionProjection", "handleSessionV3PrimaryStream", "sessionV3StreamHub"} {
		if !strings.Contains(string(body), required) {
			t.Fatalf("sessions_v3_stream_ws.go missing required V3 stream symbol %q", required)
		}
	}
	for _, forbidden := range []string{"runStreamManager", "handleRunStream", "proxyManagedHostRunStream", "dispatchRemoteRuntime", "routedSessionTarget", "gorillaws"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("sessions_v3_stream_ws.go contains forbidden runtime/v2 stream symbol %q", forbidden)
		}
	}
}

func createSessionsV3PrimaryTestSession(t *testing.T, server *Server, clientRequestID, title string) pebblestore.SessionSnapshot {
	t.Helper()
	return createSessionsV3PrimaryTestSessionWithWorkspace(t, server, clientRequestID, title, "/workspace/cp6")
}

func createSessionsV3PrimaryTestSessionWithWorkspace(t *testing.T, server *Server, clientRequestID, title, workspacePath string) pebblestore.SessionSnapshot {
	t.Helper()
	body := fmt.Sprintf(`{"client_request_id":%q,"workspace_path":%q,"title":%q}`, clientRequestID, workspacePath, title)
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewBufferString(body))
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
	if strings.TrimSpace(created.Session.ID) == "" {
		t.Fatalf("created session missing id: %+v", created.Session)
	}
	return created.Session
}

func postSessionsV3PrimaryTestMessage(t *testing.T, server *Server, sessionID, clientRequestID, content string) {
	t.Helper()
	body := fmt.Sprintf(`{"client_request_id":%q,"role":"user","content":%q}`, clientRequestID, content)
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+sessionID+"/messages", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("message status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func getSessionsV3PrimaryTestMessages(t *testing.T, server *Server, sessionID string, afterSeq uint64, limit int) []pebblestore.MessageSnapshot {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v3/sessions/%s/messages?after_seq=%d&limit=%d", sessionID, afterSeq, limit), nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET messages status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		OK       bool                          `json:"ok"`
		Messages []pebblestore.MessageSnapshot `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode GET messages response: %v", err)
	}
	if !payload.OK {
		t.Fatalf("GET messages payload not ok: %+v", payload)
	}
	return payload.Messages
}

func dialSessionsV3PrimaryStream(t *testing.T, baseURL, sessionID, rawQuery string) *gorillaws.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(baseURL, "http") + "/v3/sessions/" + sessionID + "/stream"
	if rawQuery != "" {
		wsURL += "?" + rawQuery
	}
	conn, resp, err := gorillaws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial v3 stream: %v status=%d", err, resp.StatusCode)
		}
		t.Fatalf("dial v3 stream: %v", err)
	}
	return conn
}

func readSessionsV3PrimaryStreamFrame(t *testing.T, conn *gorillaws.Conn) sessionV3StreamFrame {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read v3 stream frame: %v", err)
	}
	var frame sessionV3StreamFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode v3 stream frame %s: %v", string(raw), err)
	}
	return frame
}

func newSessionsV3PrimaryAPITestServer(t *testing.T, storePath string) (*Server, *sessionruntime.Service, func() error) {
	t.Helper()
	store, err := pebblestore.Open(storePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		_ = store.Close()
		t.Fatalf("new event log: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	server := NewServer(nil, nil, nil, nil, sessionSvc, nil, nil, nil, nil, nil, nil, eventLog, stream.NewHub(eventLog))
	server.v3SessionExecutor = nil
	closeStore := func() error {
		server.CancelInFlightRuns()
		server.WaitForInFlightRuns(2 * time.Second)
		return store.Close()
	}
	return server, sessionSvc, closeStore
}
