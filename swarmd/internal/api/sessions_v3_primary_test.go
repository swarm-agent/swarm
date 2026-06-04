package api

import (
	"bytes"
	"context"
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

	agentruntime "swarm/packages/swarmd/internal/agent"

	gorillaws "github.com/gorilla/websocket"

	"swarm/packages/swarmd/internal/discovery"
	modelruntime "swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	runruntime "swarm/packages/swarmd/internal/run"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	"swarm/packages/swarmd/internal/tool"
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
	if createPayload.Session.Metadata["agent_name"] != "swarm" || createPayload.Session.Metadata["resolved_agent_name"] != "swarm" || createPayload.Session.Metadata["agent_mode"] != "primary" || createPayload.Session.Metadata["runtime_mode"] != pebblestore.AgentRuntimeModePlanAuto {
		t.Fatalf("server-owned agent metadata = %+v", createPayload.Session.Metadata)
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
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "authority", body: `{"client_request_id":"protected-metadata","workspace_path":"/workspace/v3","metadata":{"runtime_swarm_id":"container-swarm"}}`},
		{name: "agent spoof", body: `{"client_request_id":"protected-agent-metadata","workspace_path":"/workspace/v3","metadata":{"agent_name":"spoof"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, sessionSvc, _, routeStore, _ := newRoutedSessionTestServerWithSwarmStore(t)
			req := httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewBufferString(tc.body))
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
		})
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

func TestSessionsV3PrimaryProviderVerticalSlicePersistsAssistantOnce(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{text: "provider assistant response"}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	server.v3SessionExecutor = newSessionV3Executor(server)
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "provider-vertical-create", "provider model", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	body := `{"client_request_id":"provider-vertical-message","role":"user","content":"hello provider model"}`
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
	if len(messages) != 2 || messages[0].Role != "user" || messages[1].Role != "assistant" || messages[1].Content != "provider assistant response" {
		t.Fatalf("messages = %+v", messages)
	}
	listed := getSessionsV3PrimaryTestMessages(t, server, created.ID, 0, 10)
	if len(listed) != 2 || listed[0].Role != "user" || listed[0].Content != "hello provider model" || listed[1].Role != "assistant" || listed[1].Content != "provider assistant response" {
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

func TestSessionsV3PrimaryDuplicatePostReplayDoesNotDuplicateAssistantOutput(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	installSessionsV3TestProvider(server, "deduped provider answer")
	exec := newSessionV3Executor(server)
	exec.startDelay = 150 * time.Millisecond
	server.v3SessionExecutor = exec
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "duplicate-post-create", "duplicate post", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	body := `{"client_request_id":"duplicate-post-message","role":"user","content":"dedupe this turn"}`
	post := func() {
		req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/messages", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, withTestPrincipal(req))
		if rec.Code != http.StatusOK {
			t.Fatalf("post status = %d body=%s", rec.Code, rec.Body.String())
		}
	}
	post()
	post()
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 2 || messages[0].Role != "user" || messages[1].Role != "assistant" {
		t.Fatalf("messages after duplicate POST replay = %+v", messages)
	}
	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var started, completed int
	for _, event := range events {
		switch event.EventType {
		case "session.assistant.started":
			started++
		case "session.assistant.completed":
			completed++
		}
	}
	if started != 1 || completed != 1 {
		t.Fatalf("assistant events started=%d completed=%d events=%+v", started, completed, events)
	}
}

func TestSessionsV3PrimaryDispatchBlockedNeverInvokesExecutorModel(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{text: "should not run"}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "dispatch-blocked-no-model-create", "blocked no model", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model"})
	body := `{"client_request_id":"dispatch-blocked-no-model-message","role":"user","content":"do not execute","dispatch_authority":{"runtime_swarm_id":"missing-swarm","authority_container_id":"container-1"}}`
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/messages", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("post status = %d body=%s", rec.Code, rec.Body.String())
	}
	intent := waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentDispatchBlocked)
	if !strings.Contains(intent.BlockedReason, "runtime placement not found") {
		t.Fatalf("run intent = %+v", intent)
	}
	if !server.WaitForInFlightRuns(2 * time.Second) {
		t.Fatalf("executor did not drain")
	}
	if runner.callCount != 0 {
		t.Fatalf("provider call count = %d, want 0 for dispatch_blocked", runner.callCount)
	}
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "user" || messages[0].Content != "do not execute" {
		t.Fatalf("messages after dispatch_blocked = %+v", messages)
	}
}

func TestSessionsV3ExecutorFailsStaleRunningRunAfterRestartWithoutResume(t *testing.T) {
	t.Setenv("SWARM_API_NO_AUTH", "1")
	storePath := filepath.Join(t.TempDir(), "sessions-v3-stale-running.pebble")
	server, sessionSvc, closeStore := newSessionsV3PrimaryAPITestServer(t, storePath)
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "stale-running-create", "stale running", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "stale-running-message", "interrupted turn")
	intents, err := sessionSvc.Store().ListV3SessionRunIntents(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list run intents: %v", err)
	}
	if len(intents) != 1 || intents[0].Status != sessionruntime.RunIntentPendingExecutor {
		t.Fatalf("run intents before stale mark = %+v", intents)
	}
	runID := intents[0].RunID
	staleAt := time.Now().Add(-10 * time.Minute).UnixMilli()
	payloadHash, err := sessionV3ExecutorPayloadHash(created.ID, runID, sessionruntime.RunIntentRunning, "", "session.assistant.started", "")
	if err != nil {
		t.Fatalf("payload hash: %v", err)
	}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       created.ID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: sessionV3ExecutorClientRequestID("session.assistant.started", runID),
		IdempotencyKey:  sessionV3ExecutorClientRequestID("session.assistant.started", runID),
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		EventType:       "session.assistant.started",
		RunIntent:       &pebblestore.V3SessionRunIntent{RunID: runID, Status: sessionruntime.RunIntentRunning, UpdatedAt: staleAt},
		NowUnixMs:       staleAt,
	}); err != nil {
		t.Fatalf("mark run running: %v", err)
	}
	if err := closeStore(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	restarted, restartedSessions, closeRestarted := newSessionsV3PrimaryAPITestServer(t, storePath)
	defer func() { _ = closeRestarted() }()
	runner := &sessionsV3RecordingProviderRunner{text: "should not resume"}
	providers := registry.New()
	providers.RegisterRunner(runner)
	restarted.providers = providers
	exec := newSessionV3Executor(restarted)
	exec.startDelay = 0
	restarted.v3SessionExecutor = exec
	intent := waitForSessionsV3RunIntentStatus(t, restartedSessions, created.ID, sessionruntime.RunIntentFailed)
	if !strings.Contains(intent.BlockedReason, "executor interrupted during daemon restart") {
		t.Fatalf("failed intent = %+v", intent)
	}
	if runner.callCount != 0 {
		t.Fatalf("provider call count = %d, want 0 for stale interrupted run", runner.callCount)
	}
	messages, err := restartedSessions.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages after restart: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "user" || messages[0].Content != "interrupted turn" {
		t.Fatalf("messages after stale running recovery = %+v", messages)
	}
}

func TestSessionsV3PrimaryRunStopCancelsActiveExecutorAndSuppressesLateOutput(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	entered := make(chan struct{})
	releaseLate := make(chan struct{})
	runner := &sessionsV3RecordingProviderRunner{
		handler: func(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
			close(entered)
			<-ctx.Done()
			<-releaseLate
			if onEvent != nil {
				onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "late output"})
			}
			return provideriface.Response{Text: "late assistant"}, nil
		},
	}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "active-cancel-create", "active cancel", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "active-cancel-message", "cancel active run")
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatalf("provider was not entered")
	}
	intent := waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentRunning)
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/run/stop", bytes.NewBufferString(fmt.Sprintf(`{"type":"run.stop","run_id":%q,"reason":"stop from test"}`, intent.RunID)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("stop status = %d, body=%s", rec.Code, rec.Body.String())
	}
	failed := waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentFailed)
	if failed.RunID != intent.RunID || failed.BlockedReason != "stop from test" {
		t.Fatalf("failed intent = %+v", failed)
	}
	close(releaseLate)
	if !server.WaitForInFlightRuns(2 * time.Second) {
		t.Fatalf("executor did not drain after active cancel")
	}
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "user" {
		t.Fatalf("messages after cancel = %+v", messages)
	}
	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	for _, event := range events {
		if event.EventType == "session.assistant.delta" || event.EventType == "session.assistant.completed" {
			t.Fatalf("late assistant event persisted after cancel: %+v", event)
		}
	}
}

func TestSessionsV3PrimaryRunStopCancelsBeforeExecutorStart(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{text: "should not run"}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	exec := newSessionV3Executor(server)
	exec.startDelay = 500 * time.Millisecond
	server.v3SessionExecutor = exec
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "queued-cancel-create", "queued cancel", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "queued-cancel-message", "cancel before start")
	intent := waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentPendingExecutor)
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/run/stop", bytes.NewBufferString(fmt.Sprintf(`{"type":"run.stop","run_id":%q}`, intent.RunID)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("queued stop status = %d, body=%s", rec.Code, rec.Body.String())
	}
	failed := waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentFailed)
	if failed.RunID != intent.RunID || failed.BlockedReason != sessionV3RunStopDefaultReason {
		t.Fatalf("failed intent = %+v", failed)
	}
	if !server.WaitForInFlightRuns(2 * time.Second) {
		t.Fatalf("executor did not drain after queued cancel")
	}
	if runner.callCount != 0 {
		t.Fatalf("provider call count = %d, want 0 for queued cancel", runner.callCount)
	}
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "user" {
		t.Fatalf("messages after queued cancel = %+v", messages)
	}
}

func TestSessionsV3PrimaryPostReturnsBeforeModelCompletion(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	installSessionsV3TestProvider(server, "nonblocking provider answer")
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	exec.modelDelay = 500 * time.Millisecond
	server.v3SessionExecutor = exec
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "nonblocking-create", "nonblocking", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
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
		t.Fatalf("POST blocked for %s; want return before provider delay", elapsed)
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

func waitForSessionsV3RunIntentStatus(t *testing.T, sessionSvc *sessionruntime.Service, sessionID, want string) sessionruntime.SessionRunIntent {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		intents, err := sessionSvc.Store().ListV3SessionRunIntents(sessionID, 0, 10)
		if err != nil {
			t.Fatalf("list run intents: %v", err)
		}
		for _, intent := range intents {
			if intent.Status == want {
				return intent
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	intents, _ := sessionSvc.Store().ListV3SessionRunIntents(sessionID, 0, 10)
	t.Fatalf("run intent status %q not found: %+v", want, intents)
	return sessionruntime.SessionRunIntent{}
}

func waitForSessionsV3Title(t *testing.T, sessionSvc *sessionruntime.Service, sessionID, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		session, ok, err := sessionSvc.GetSession(sessionID)
		if err != nil {
			t.Fatalf("get session: %v", err)
		}
		if ok && session.Title == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	session, _, _ := sessionSvc.GetSession(sessionID)
	t.Fatalf("session title = %q, want %q", session.Title, want)
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

func TestSessionsV3PrimaryStreamPublishesExecutorCommittedEventsAndReplaysThem(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	installSessionsV3TestProvider(server, "stream committed assistant")
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "cp4-outbox-create", "CP4 outbox", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
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

	postSessionsV3PrimaryTestMessage(t, server, created.ID, "cp4-outbox-message", "stream committed assistant")
	wantLive := []string{"session.message.appended", "session.assistant.started", "session.assistant.delta", "session.assistant.completed"}
	for i, wantType := range wantLive {
		frame := readSessionsV3PrimaryStreamFrame(t, conn)
		wantSeq := uint64(i + 2)
		if frame.Type != "event" || frame.Event == nil || frame.Event.Seq != wantSeq || frame.Event.EventType != wantType {
			t.Fatalf("live frame %d = %+v, want seq=%d type=%s", i, frame, wantSeq, wantType)
		}
		if _, ok, err := sessionSvc.Store().GetV3SessionEvent(created.ID, frame.Event.Seq); err != nil || !ok {
			t.Fatalf("live event seq %d was not durable before publish: ok=%t err=%v", frame.Event.Seq, ok, err)
		}
	}

	messages := getSessionsV3PrimaryTestMessages(t, server, created.ID, 0, 10)
	if len(messages) != 2 || messages[1].Role != "assistant" || !strings.Contains(messages[1].Content, "stream committed assistant") {
		t.Fatalf("messages after executor completion = %+v", messages)
	}

	replay := dialSessionsV3PrimaryStream(t, httpServer.URL, created.ID, "after_seq=2")
	defer replay.Close()
	replayStarted := readSessionsV3PrimaryStreamFrame(t, replay)
	if replayStarted.Type != "replay.started" || replayStarted.HighWatermarkSeq != 5 {
		t.Fatalf("replay started = %+v", replayStarted)
	}
	for i, wantType := range []string{"session.assistant.started", "session.assistant.delta", "session.assistant.completed"} {
		frame := readSessionsV3PrimaryStreamFrame(t, replay)
		wantSeq := uint64(i + 3)
		if frame.Type != "event" || frame.Event == nil || frame.Event.Seq != wantSeq || frame.Event.EventType != wantType {
			t.Fatalf("replay frame %d = %+v, want seq=%d type=%s", i, frame, wantSeq, wantType)
		}
	}
	replayComplete := readSessionsV3PrimaryStreamFrame(t, replay)
	if replayComplete.Type != "replay.complete" || replayComplete.LastSeq != 5 {
		t.Fatalf("replay complete = %+v", replayComplete)
	}
}

func TestSessionsV3PrimaryLiveStreamPublishesProviderToolProgressAndCommittedCompletion(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := t.TempDir()
	runner := &sessionsV3RecordingProviderRunner{responses: []provideriface.Response{
		{FunctionCalls: []provideriface.FunctionCall{{CallID: "call-live-bash", Name: "bash", Arguments: `{"command":"printf 'live-tool-progress\n'","timeout_ms":1000}`}}},
		{Text: "final answer after live tool progress"},
	}}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	runSvc := runruntime.NewService(sessionSvc, server.model, providers, tool.NewRuntime(1), server.perm.(*permission.Service), server.agents, nil, nil)
	server.runner = runSvc
	server.SetBypassPermissions(true)
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "swarm", Mode: agentruntime.ModePrimary, Provider: "test-provider", Model: "test-model", Thinking: "medium", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{"bash": {Enabled: pebblestore.BoolPtr(true)}}}, Enabled: pebblestore.BoolPtr(true), Prompt: "Swarm prompt"}); err != nil {
		t.Fatalf("upsert bash-enabled swarm agent: %v", err)
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec
	created := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "live-tool-stream-create", "live tool stream", workspace, pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.Handler().ServeHTTP(w, withTestPrincipal(r))
	}))
	defer httpServer.Close()

	conn := dialSessionsV3PrimaryStream(t, httpServer.URL, created.ID, "after_seq=1")
	defer conn.Close()
	started := readSessionsV3PrimaryStreamFrame(t, conn)
	complete := readSessionsV3PrimaryStreamFrame(t, conn)
	if started.Type != "replay.started" || complete.Type != "replay.complete" || complete.LastSeq != 1 {
		t.Fatalf("initial stream frames started=%+v complete=%+v", started, complete)
	}

	postSessionsV3PrimaryTestMessage(t, server, created.ID, "live-tool-stream-message", "run a live bash tool")
	want := []string{"session.tool.started", "session.tool.delta", "session.tool.completed", "session.assistant.completed"}
	seen := make([]string, 0, len(want))
	for len(seen) < len(want) {
		frame := readSessionsV3PrimaryStreamFrame(t, conn)
		if frame.Type != "event" || frame.Event == nil {
			t.Fatalf("live stream frame = %+v, want event", frame)
		}
		eventType := strings.TrimSpace(frame.Event.EventType)
		if eventType == "session.message.appended" || eventType == "session.assistant.started" || eventType == "session.assistant.delta" {
			continue
		}
		seen = append(seen, eventType)
		if eventType == "session.tool.started" || eventType == "session.tool.delta" || eventType == "session.tool.completed" {
			var payload map[string]any
			if err := json.Unmarshal(frame.Event.Payload, &payload); err != nil {
				t.Fatalf("decode tool event payload: %v", err)
			}
			if payload["run_id"] == "" || payload["tool_name"] != "bash" || payload["call_id"] != "call-live-bash" {
				t.Fatalf("tool event payload for %s = %+v", eventType, payload)
			}
		}
	}
	for i, wantType := range want {
		if seen[i] != wantType {
			t.Fatalf("live tool event order = %+v, want %+v", seen, want)
		}
	}
}

func TestSessionsV3PrimaryStreamDoesNotRepublishReplayedMutations(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "cp4-no-republish-create", "CP4 no republish")
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

	postSessionsV3PrimaryTestMessage(t, server, created.ID, "cp4-no-republish-message", "publish once")
	live := readSessionsV3PrimaryStreamFrame(t, conn)
	if live.Type != "event" || live.Event == nil || live.Event.Seq != 2 || live.Event.EventType != "session.message.appended" {
		t.Fatalf("live event = %+v", live)
	}
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "cp4-no-republish-message", "publish once")
	if frame, ok := readOptionalSessionsV3PrimaryStreamFrame(t, conn, 150*time.Millisecond); ok {
		t.Fatalf("duplicate idempotency replay unexpectedly republished frame: %+v", frame)
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

func TestSessionsV3PrimaryStreamHubMarksSlowConsumerAndOtherSubscribersContinue(t *testing.T) {
	hub := newSessionV3StreamHub()
	const sessionID = "v3_test_slow_consumer"
	slow := hub.subscribe(sessionID)
	fast := hub.subscribe(sessionID)
	if slow == nil || fast == nil {
		t.Fatalf("subscribers were not created")
	}
	defer hub.unsubscribe(slow)
	defer hub.unsubscribe(fast)

	const eventCount = sessionV3StreamSubscriberBufSize + 2
	for i := 0; i < eventCount; i++ {
		hub.publish(sessionruntime.SessionEvent{SessionID: sessionID, Seq: uint64(i + 1), EventType: "session.message.appended"})
		select {
		case event := <-fast.send:
			if event.Seq != uint64(i+1) {
				t.Fatalf("fast event %d seq = %d", i, event.Seq)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("fast subscriber did not receive event %d after slow subscriber was removed", i+1)
		}
	}

	select {
	case notice := <-slow.slow:
		if notice.NextSeq == 0 || !strings.Contains(notice.Reason, "slow_consumer") {
			t.Fatalf("slow consumer notice = %+v", notice)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("slow consumer was not marked when subscriber queue filled")
	}

	drainedSlow := 0
	for {
		select {
		case <-slow.send:
			drainedSlow++
		default:
			goto drained
		}
	}
drained:
	if drainedSlow != sessionV3StreamSubscriberBufSize {
		t.Fatalf("slow subscriber buffered events = %d, want %d", drainedSlow, sessionV3StreamSubscriberBufSize)
	}
	hub.publish(sessionruntime.SessionEvent{SessionID: sessionID, Seq: eventCount + 1, EventType: "session.message.appended"})
	select {
	case event := <-slow.send:
		t.Fatalf("slow subscriber received event after slow-consumer removal: %+v", event)
	default:
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

func installSessionsV3TestProvider(server *Server, text string) *sessionsV3RecordingProviderRunner {
	runner := &sessionsV3RecordingProviderRunner{text: text}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	return runner
}

func createSessionsV3PrimaryTestSession(t *testing.T, server *Server, clientRequestID, title string) pebblestore.SessionSnapshot {
	t.Helper()
	return createSessionsV3PrimaryTestSessionWithWorkspace(t, server, clientRequestID, title, "/workspace/cp6")
}

func createSessionsV3PrimaryTestSessionWithWorkspace(t *testing.T, server *Server, clientRequestID, title, workspacePath string) pebblestore.SessionSnapshot {
	t.Helper()
	body := fmt.Sprintf(`{"client_request_id":%q,"workspace_path":%q,"title":%q,"agent_name":"swarm"}`, clientRequestID, workspacePath, title)
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

func readOptionalSessionsV3PrimaryStreamFrame(t *testing.T, conn *gorillaws.Conn, timeout time.Duration) (sessionV3StreamFrame, bool) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		return sessionV3StreamFrame{}, false
	}
	var frame sessionV3StreamFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode v3 stream frame %s: %v", string(raw), err)
	}
	return frame, true
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
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), eventLog)
	if err := agentSvc.EnsureDefaults(); err != nil {
		_ = store.Close()
		t.Fatalf("ensure agent defaults: %v", err)
	}
	if _, _, _, err := agentSvc.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "swarm", Mode: agentruntime.ModePrimary, Enabled: pebblestore.BoolPtr(true), Prompt: "Swarm test primary prompt"}); err != nil {
		_ = store.Close()
		t.Fatalf("create swarm agent: %v", err)
	}
	modelSvc := modelruntime.NewService(pebblestore.NewModelStore(store), eventLog, nil)
	runner := &sessionsV3RecordingProviderRunner{text: "recovered provider answer"}
	providers := registry.New()
	providers.RegisterRunner(runner)
	permissionSvc := permission.NewService(pebblestore.NewPermissionStore(store), eventLog, nil)
	permissionSvc.SetSessionResolver(sessionSvc)
	runSvc := runruntime.NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(1), permissionSvc, agentSvc, nil, nil)
	server := NewServer(nil, agentSvc, modelSvc, runSvc, sessionSvc, nil, nil, nil, providers, permissionSvc, nil, eventLog, stream.NewHub(eventLog))
	server.v3SessionExecutor = nil
	closeStore := func() error {
		server.CancelInFlightRuns()
		server.WaitForInFlightRuns(2 * time.Second)
		return store.Close()
	}
	return server, sessionSvc, closeStore
}

func TestSessionsV3ExecutorRecoversPendingRunAfterRestart(t *testing.T) {
	t.Setenv("SWARM_API_NO_AUTH", "1")
	storePath := filepath.Join(t.TempDir(), "sessions-v3-executor-recovery.pebble")
	server, _, closeStore := newSessionsV3PrimaryAPITestServer(t, storePath)
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "recover-create", "recover pending", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "recover-message", "recover me")
	if err := closeStore(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	restarted, sessionSvc, closeRestarted := newSessionsV3PrimaryAPITestServer(t, storePath)
	defer func() { _ = closeRestarted() }()
	exec := newSessionV3Executor(restarted)
	exec.startDelay = 0
	restarted.v3SessionExecutor = exec
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages after recovery: %v", err)
	}
	if len(messages) != 2 || messages[1].Role != "assistant" || messages[1].Content != "recovered provider answer" {
		t.Fatalf("messages after recovery = %+v", messages)
	}
	exec.recoverDurableRuns(restarted.runCtx)
	if !restarted.WaitForInFlightRuns(2 * time.Second) {
		t.Fatalf("executor did not drain after repeated recovery scan")
	}
	messages, err = sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages after repeated recovery: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages after repeated recovery = %+v, want no duplicate assistant", messages)
	}
	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("list events after repeated recovery: %v", err)
	}
	var completed int
	for _, event := range events {
		if event.EventType == "session.assistant.completed" {
			completed++
		}
	}
	if completed != 1 {
		t.Fatalf("assistant completed events after repeated recovery = %d events=%+v", completed, events)
	}
}

func TestSessionsV3ExecutorCoalescesProviderDeltas(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{deltas: []string{"hel", "lo", " ", "world"}, text: "hello world"}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	exec.deltaFlushMaxBytes = 64
	exec.deltaFlushMaxDelay = time.Hour
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "coalesced-provider-create", "coalesced provider", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "coalesced-provider-message", "coalesce provider deltas")
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)

	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var deltas []sessionruntime.SessionEvent
	for _, event := range events {
		if event.EventType == "session.assistant.delta" {
			deltas = append(deltas, event)
		}
	}
	if len(deltas) != 1 {
		t.Fatalf("assistant delta events = %d, want one coalesced event events=%+v", len(deltas), events)
	}
	var payload struct {
		RunID      string `json:"run_id"`
		DeltaIndex int    `json:"delta_index"`
		Delta      string `json:"delta"`
	}
	if err := json.Unmarshal(deltas[0].Payload, &payload); err != nil {
		t.Fatalf("decode delta payload: %v", err)
	}
	if payload.DeltaIndex != 1 || payload.Delta != "hello world" {
		t.Fatalf("delta payload = %+v", payload)
	}
}

func TestSessionsV3ExecutorFlushesProviderDeltaAtSizeBoundary(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{deltas: []string{"ab", "cd", "ef"}, text: "abcdef"}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	exec.deltaFlushMaxBytes = 4
	exec.deltaFlushMaxDelay = time.Hour
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "boundary-provider-create", "boundary provider", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "boundary-provider-message", "flush at size boundary")
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)

	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var got []string
	for _, event := range events {
		if event.EventType != "session.assistant.delta" {
			continue
		}
		var payload struct {
			Delta string `json:"delta"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("decode delta payload: %v", err)
		}
		got = append(got, payload.Delta)
	}
	if strings.Join(got, "|") != "abcd|ef" {
		t.Fatalf("coalesced deltas = %#v, want [abcd ef]", got)
	}
}

func TestSessionsV3ExecutorRecordsFailurePayload(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{err: fmt.Errorf("provider exploded")}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "failure-provider-create", "failure provider", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "failure-provider-message", "fail provider")
	waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentFailed)

	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var failure *sessionruntime.SessionEvent
	for _, event := range events {
		if event.EventType == "session.run.failed" {
			copy := event
			failure = &copy
		}
	}
	if failure == nil {
		t.Fatalf("missing run failure event: %+v", events)
	}
	var payload struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(failure.Payload, &payload); err != nil {
		t.Fatalf("decode failure payload: %v", err)
	}
	if payload.Status != sessionruntime.RunIntentFailed || !strings.Contains(payload.Error, "provider exploded") {
		t.Fatalf("failure payload = %+v", payload)
	}
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "user" {
		t.Fatalf("messages after failure = %+v, want only committed user message", messages)
	}
}

func TestSessionsV3ExecutorUsesProviderFromCommittedHistory(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{
		text: "provider answer",
		response: provideriface.Response{
			ID:         "provider-response-1",
			Model:      "provider-model-resolved",
			StopReason: "stop",
			Usage:      provideriface.TokenUsage{InputTokens: 3, OutputTokens: 4, TotalTokens: 7},
		},
	}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "provider-create", "provider", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "provider-message", "use real provider")
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)

	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 2 || messages[0].Role != "user" || messages[1].Role != "assistant" || messages[1].Content != "provider answer" {
		t.Fatalf("messages = %+v", messages)
	}
	if messages[1].Metadata["executor_kind"] != "v3_provider" || messages[1].Metadata["provider"] != "test-provider" || messages[1].Metadata["model"] != "provider-model-resolved" || messages[1].Metadata["provider_response_id"] != "provider-response-1" {
		t.Fatalf("assistant metadata = %+v", messages[1].Metadata)
	}
	if runner.callCount != 1 {
		t.Fatalf("provider call count = %d, want 1", runner.callCount)
	}
	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var deltas int
	for _, event := range events {
		if event.EventType == "session.assistant.delta" {
			deltas++
		}
	}
	if deltas != 1 {
		t.Fatalf("assistant delta events = %d, want 1 events=%+v", deltas, events)
	}
	if len(runner.lastRequest.Input) != 1 {
		t.Fatalf("provider input = %+v, want committed user message only", runner.lastRequest.Input)
	}
	if runner.lastRequest.Model != "test-model" || runner.lastRequest.Thinking != "medium" || runner.lastRequest.SessionID != created.ID || runner.lastRequest.ToolChoice != "none" {
		t.Fatalf("provider request = %+v", runner.lastRequest)
	}
	if !strings.Contains(runner.lastRequest.Instructions, "Active agent profile:") || !strings.Contains(runner.lastRequest.Instructions, "- name: swarm") {
		t.Fatalf("provider instructions = %q", runner.lastRequest.Instructions)
	}
}

func TestSessionsV3ExecutorUsesAutoToolChoiceWhenToolsResolved(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{text: "tool-enabled answer"}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	runSvc := runruntime.NewService(sessionSvc, server.model, providers, tool.NewRuntime(1), server.perm.(*permission.Service), server.agents, nil, nil)
	server.runner = runSvc
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "swarm", Mode: agentruntime.ModePrimary, Provider: "test-provider", Model: "test-model", Thinking: "medium", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{"read": {Enabled: pebblestore.BoolPtr(true)}}}, Enabled: pebblestore.BoolPtr(true), Prompt: "Swarm prompt"}); err != nil {
		t.Fatalf("upsert tool-enabled swarm agent: %v", err)
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "provider-tools-create", "provider tools", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "provider-tools-message", "list files")
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)
	if runner.lastRequest.ToolChoice != "auto" || len(runner.lastRequest.Tools) == 0 {
		t.Fatalf("provider request tools=%+v tool_choice=%q, want auto with tools", runner.lastRequest.Tools, runner.lastRequest.ToolChoice)
	}
}

func TestSessionsV3ExecutorExecutesProviderToolCallsAndContinuesToFinalAnswer(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "facts.txt"), []byte("tool-loop-file-content"), 0o644); err != nil {
		t.Fatalf("write workspace fact file: %v", err)
	}
	runner := &sessionsV3RecordingProviderRunner{responses: []provideriface.Response{
		{FunctionCalls: []provideriface.FunctionCall{{CallID: "call-read-facts", Name: "read", Arguments: `{"path":"facts.txt"}`}}},
		{Text: "final answer after durable tool result"},
	}}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	runSvc := runruntime.NewService(sessionSvc, server.model, providers, tool.NewRuntime(1), server.perm.(*permission.Service), server.agents, nil, nil)
	server.runner = runSvc
	server.SetBypassPermissions(true)
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "swarm", Mode: agentruntime.ModePrimary, Provider: "test-provider", Model: "test-model", Thinking: "medium", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{"read": {Enabled: pebblestore.BoolPtr(true)}}}, Enabled: pebblestore.BoolPtr(true), Prompt: "Swarm prompt"}); err != nil {
		t.Fatalf("upsert tool-enabled swarm agent: %v", err)
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "provider-tool-loop-create", "provider tool loop", workspace, pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "provider-tool-loop-message", "read facts.txt before answering")
	intent := waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentCompleted)
	if intent.BlockedReason != "" {
		t.Fatalf("completed run has blocked reason: %+v", intent)
	}
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 3 || messages[0].Role != "user" || messages[1].Role != "tool" || messages[2].Role != "assistant" {
		t.Fatalf("messages after tool loop = %+v, want user/tool/assistant", messages)
	}
	if !strings.Contains(messages[1].Content, "tool-loop-file-content") {
		t.Fatalf("tool message content = %q, want durable tool output", messages[1].Content)
	}
	if messages[2].Content != "final answer after durable tool result" {
		t.Fatalf("assistant final content = %q", messages[2].Content)
	}
	if runner.callCount != 2 {
		t.Fatalf("provider call count = %d, want initial tool request plus continuation", runner.callCount)
	}
	if len(runner.requests) != 2 || len(runner.requests[1].Input) < 2 {
		t.Fatalf("continuation request input = %+v", runner.requests)
	}
}

func TestSessionsV3ExecutorCarriesFullContinuationHistoryAcrossMultipleToolSteps(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "one.txt"), []byte("first durable tool result"), 0o644); err != nil {
		t.Fatalf("write first file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "two.txt"), []byte("second durable tool result"), 0o644); err != nil {
		t.Fatalf("write second file: %v", err)
	}
	runner := &sessionsV3RecordingProviderRunner{responses: []provideriface.Response{
		{FunctionCalls: []provideriface.FunctionCall{{CallID: "call-reused", Name: "read", Arguments: `{"path":"one.txt"}`}}},
		{FunctionCalls: []provideriface.FunctionCall{{CallID: "call-reused", Name: "read", Arguments: `{"path":"two.txt"}`}}},
		{Text: "final answer with both tool results"},
	}}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	runSvc := runruntime.NewService(sessionSvc, server.model, providers, tool.NewRuntime(1), server.perm.(*permission.Service), server.agents, nil, nil)
	server.runner = runSvc
	server.SetBypassPermissions(true)
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "swarm", Mode: agentruntime.ModePrimary, Provider: "test-provider", Model: "test-model", Thinking: "medium", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{"read": {Enabled: pebblestore.BoolPtr(true)}}}, Enabled: pebblestore.BoolPtr(true), Prompt: "Swarm prompt"}); err != nil {
		t.Fatalf("upsert tool-enabled swarm agent: %v", err)
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "provider-multi-tool-create", "provider multi tool", workspace, pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "provider-multi-tool-message", "read both files before answering")
	waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentCompleted)
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 4 || messages[0].Role != "user" || messages[1].Role != "tool" || messages[2].Role != "tool" || messages[3].Role != "assistant" {
		t.Fatalf("messages after multi-step tool loop = %+v, want user/tool/tool/assistant", messages)
	}
	if !strings.Contains(messages[1].Content, "first durable tool result") || !strings.Contains(messages[2].Content, "second durable tool result") {
		t.Fatalf("tool messages did not persist distinct step results: %+v", messages)
	}
	if messages[1].ID == messages[2].ID {
		t.Fatalf("tool messages reused id across steps: %+v", messages)
	}
	if runner.callCount != 3 {
		t.Fatalf("provider call count = %d, want two tool steps plus final", runner.callCount)
	}
	if len(runner.requests) != 3 || len(runner.requests[2].Input) != 3 {
		t.Fatalf("final continuation input = %+v, want full user/tool/tool history", runner.requests)
	}
}

func TestSessionsV3ExecutorPersistsFailureWhenToolLoopExceedsBound(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "loop.txt"), []byte("loop tool result"), 0o644); err != nil {
		t.Fatalf("write loop file: %v", err)
	}
	responses := make([]provideriface.Response, 0, sessionV3ProviderToolLoopMaxSteps)
	for i := 0; i < sessionV3ProviderToolLoopMaxSteps; i++ {
		responses = append(responses, provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "call-reused", Name: "read", Arguments: `{"path":"loop.txt"}`}}})
	}
	runner := &sessionsV3RecordingProviderRunner{responses: responses}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	runSvc := runruntime.NewService(sessionSvc, server.model, providers, tool.NewRuntime(1), server.perm.(*permission.Service), server.agents, nil, nil)
	server.runner = runSvc
	server.SetBypassPermissions(true)
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "swarm", Mode: agentruntime.ModePrimary, Provider: "test-provider", Model: "test-model", Thinking: "medium", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{"read": {Enabled: pebblestore.BoolPtr(true)}}}, Enabled: pebblestore.BoolPtr(true), Prompt: "Swarm prompt"}); err != nil {
		t.Fatalf("upsert tool-enabled swarm agent: %v", err)
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "provider-tool-bound-create", "provider tool bound", workspace, pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "provider-tool-bound-message", "keep reading forever")
	intent := waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentFailed)
	if !strings.Contains(intent.BlockedReason, fmt.Sprintf("tool loop exceeded %d steps", sessionV3ProviderToolLoopMaxSteps)) {
		t.Fatalf("failed intent = %+v", intent)
	}
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != sessionV3ProviderToolLoopMaxSteps+1 {
		t.Fatalf("messages after bounded failure = %d %+v, want user plus one tool per step", len(messages), messages)
	}
	for i, message := range messages[1:] {
		if message.Role != "tool" || !strings.Contains(message.Content, "loop tool result") {
			t.Fatalf("tool message %d = %+v", i, message)
		}
	}
	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 40)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var failure *sessionruntime.SessionEvent
	for _, event := range events {
		if event.EventType == "session.run.failed" {
			copy := event
			failure = &copy
		}
	}
	if failure == nil {
		t.Fatalf("missing durable run failure event after bounded loop: %+v", events)
	}
}

func TestSessionsV3ExecutorContinuesAfterProviderManagedRestartTurn(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "restart.txt"), []byte("restart-turn tool result"), 0o644); err != nil {
		t.Fatalf("write restart file: %v", err)
	}
	runner := &sessionsV3RecordingProviderRunner{}
	runner.handler = func(_ context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
		if runner.callCount == 1 {
			if req.ToolInvoker == nil {
				return provideriface.Response{}, fmt.Errorf("missing provider-managed tool invoker")
			}
			if _, err := req.ToolInvoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{CallID: "provider-managed-read", Name: "read", Arguments: `{"path":"restart.txt"}`}); err != nil {
				return provideriface.Response{}, err
			}
			return provideriface.Response{RestartTurn: true}, nil
		}
		if len(req.Input) != 2 {
			return provideriface.Response{}, fmt.Errorf("restart continuation input length = %d, want user plus tool", len(req.Input))
		}
		return provideriface.Response{Text: "final answer after restart turn"}, nil
	}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	runSvc := runruntime.NewService(sessionSvc, server.model, providers, tool.NewRuntime(1), server.perm.(*permission.Service), server.agents, nil, nil)
	server.runner = runSvc
	server.SetBypassPermissions(true)
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "swarm", Mode: agentruntime.ModePrimary, Provider: "test-provider", Model: "test-model", Thinking: "medium", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{"read": {Enabled: pebblestore.BoolPtr(true)}}}, Enabled: pebblestore.BoolPtr(true), Prompt: "Swarm prompt"}); err != nil {
		t.Fatalf("upsert tool-enabled swarm agent: %v", err)
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "provider-restart-turn-create", "provider restart turn", workspace, pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "provider-restart-turn-message", "read then restart")
	waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentCompleted)
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 3 || messages[0].Role != "user" || messages[1].Role != "tool" || messages[2].Role != "assistant" {
		t.Fatalf("messages after restart turn = %+v, want user/tool/assistant", messages)
	}
	if !strings.Contains(messages[1].Content, "restart-turn tool result") || messages[2].Content != "final answer after restart turn" {
		t.Fatalf("messages after restart turn = %+v", messages)
	}
	if runner.callCount != 2 {
		t.Fatalf("provider call count = %d, want restart plus continuation", runner.callCount)
	}
}

func TestSessionsV3RuntimeInstructionsUseLegacyWorkspaceAndRuleContext(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	primary := t.TempDir()
	worktreeRoot := t.TempDir()
	linked := t.TempDir()
	if err := os.WriteFile(filepath.Join(primary, "AGENTS.md"), []byte("# Primary rule\nRoot agent rule for V3 parity."), 0o644); err != nil {
		t.Fatalf("write primary AGENTS.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(linked, "AGENTS.md"), []byte("# Linked rule\nLinked root instruction."), 0o644); err != nil {
		t.Fatalf("write linked AGENTS.md: %v", err)
	}
	server.discovery = discovery.NewService()
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	now := time.Now().UnixMilli()
	session := pebblestore.SessionSnapshot{
		ID:                      "v3session_instruction_workspace_redline",
		UserID:                  testPrincipal().UserID,
		AccountScopeID:          testPrincipal().AccountScopeID,
		WorkspacePath:           primary,
		WorkspaceName:           "primary",
		TemporaryWorkspaceRoots: []string{linked},
		WorktreeEnabled:         true,
		WorktreeRootPath:        worktreeRoot,
		WorktreeBranch:          "agent/instruction-parity",
		Title:                   "instruction parity",
		Mode:                    sessionruntime.ModeAuto,
		Preference:              pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"},
		Metadata: map[string]any{
			"agent_name":          "swarm",
			"resolved_agent_name": "swarm",
			"agent_mode":          "primary",
			"runtime_mode":        pebblestore.AgentRuntimeModePlanAuto,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       session.ID,
		UserID:          session.UserID,
		AccountScopeID:  session.AccountScopeID,
		ClientRequestID: "instruction-workspace-create",
		IdempotencyKey:  "instruction-workspace-create",
		PayloadHash:     "instruction-workspace-create-hash",
		RequestHash:     "instruction-workspace-create-hash",
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session:         &session,
		NowUnixMs:       now,
	}); err != nil {
		t.Fatalf("create session mutation: %v", err)
	}
	if _, _, err := sessionSvc.GetSession(session.ID); err != nil {
		t.Fatalf("get created session: %v", err)
	}

	resolved, err := exec.resolveSessionV3Runtime(sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: session.ID, RunID: "run-instruction-workspace"})
	if err != nil {
		t.Fatalf("resolve runtime: %v", err)
	}
	for _, want := range []string{
		"Master harness prompt (applies to every agent run):",
		"Workspace scope:",
		"- primary_root: " + primary,
		"- linked_root: " + worktreeRoot,
		"- linked_root: " + linked,
		"Workspace runtime policy:",
		"Allowed workspace roots:",
		"Loaded instruction sources:",
		"Root agent rule for V3 parity.",
		"Linked root instruction.",
	} {
		if !strings.Contains(resolved.Instructions, want) {
			t.Fatalf("resolved instructions missing %q:\n%s", want, resolved.Instructions)
		}
	}
}

func TestSessionsV3ProviderToolPersistenceUsesApplySessionMutationOnly(t *testing.T) {
	body, err := os.ReadFile("../run/provider_tool_invoker.go")
	if err != nil {
		t.Fatalf("read provider_tool_invoker.go: %v", err)
	}
	source := string(body)
	if !strings.Contains(source, "ApplySessionMutation") {
		t.Fatalf("provider tool invoker must expose an ApplySessionMutation-backed V3 persistence path")
	}
	for _, forbidden := range []string{".AppendMessage(", ".UpdateMetadata("} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("provider tool invoker contains legacy durable write %q; V3 tool persistence must use ApplySessionMutation", forbidden)
		}
	}
}

func TestSessionsV3ExecutorFailsClosedWhenProviderReturnsToolCalls(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{
		text:          "needs a tool",
		functionCalls: []provideriface.FunctionCall{{CallID: "call-1", Name: "bash", Arguments: "{}"}},
	}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "provider-tool-call-create", "provider tool call", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "provider-tool-call-message", "do something with a tool")
	intent := waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentFailed)
	if !strings.Contains(intent.BlockedReason, "tool-loop execution is not supported") {
		t.Fatalf("run intent = %+v", intent)
	}
	if runner.lastRequest.ToolChoice != "none" {
		t.Fatalf("provider tool choice = %q, want none for no resolved tools", runner.lastRequest.ToolChoice)
	}
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "user" {
		t.Fatalf("messages after unsupported tool call = %+v, want only committed user message", messages)
	}
}

func TestSessionsV3ExecutorUpdatesDefaultTitleWithMemoryAgentAfterFirstRun(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{responses: []provideriface.Response{{Text: "assistant answer"}, {Text: "Memory Agent Session Title Flow"}}}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "memory", Mode: agentruntime.ModeSubagent, Provider: "test-provider", Model: "title-model", Thinking: "low", Enabled: pebblestore.BoolPtr(true), Prompt: "Memory title prompt"}); err != nil {
		t.Fatalf("upsert memory agent: %v", err)
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "title-update-create", "New Session", pebblestore.ModelPreference{Provider: "test-provider", Model: "chat-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "title-update-message", "please title this first turn")
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)
	waitForSessionsV3Title(t, sessionSvc, created.ID, "Memory Agent Session Title Flow")
	if !server.WaitForInFlightRuns(2 * time.Second) {
		t.Fatalf("server did not drain v3 title generation")
	}

	stored, ok, err := sessionSvc.GetSession(created.ID)
	if err != nil || !ok {
		t.Fatalf("load titled session ok=%v err=%v", ok, err)
	}
	if stored.Title != "Memory Agent Session Title Flow" {
		t.Fatalf("session title = %q", stored.Title)
	}
	if runner.callCount != 2 {
		t.Fatalf("provider call count = %d, want assistant + memory title", runner.callCount)
	}
	if !strings.Contains(runner.requests[1].Instructions, "Memory title prompt") || !strings.Contains(runner.requests[1].Instructions, "You generate deterministic session titles") {
		t.Fatalf("memory title instructions = %q", runner.requests[1].Instructions)
	}
	if runner.requests[1].Model != "title-model" || runner.requests[1].Thinking != "low" || runner.requests[1].ToolChoice != "none" {
		t.Fatalf("memory title request = %+v", runner.requests[1])
	}
	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var titleEvent *sessionruntime.SessionEvent
	for _, event := range events {
		if event.EventType == "session.title.updated" {
			copy := event
			titleEvent = &copy
		}
	}
	if titleEvent == nil {
		t.Fatalf("missing session.title.updated event: %+v", events)
	}
	var payload struct {
		SessionID string                      `json:"session_id"`
		Title     string                      `json:"title"`
		Stage     string                      `json:"stage"`
		Session   pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(titleEvent.Payload, &payload); err != nil {
		t.Fatalf("decode title event payload: %v", err)
	}
	if payload.SessionID != created.ID || payload.Title != "Memory Agent Session Title Flow" || payload.Stage != "final" || payload.Session.Title != "Memory Agent Session Title Flow" {
		t.Fatalf("title event payload = %+v", payload)
	}
	replay, err := sessionSvc.ReplaySessionEvents(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("replay events: %v", err)
	}
	if replay.Session == nil || replay.Session.Title != "Memory Agent Session Title Flow" {
		t.Fatalf("replayed session = %+v", replay.Session)
	}
}

func TestSessionsV3ExecutorDoesNotRetitleExplicitTitle(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{responses: []provideriface.Response{{Text: "assistant answer"}, {Text: "Should Not Apply"}}}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "explicit-title-create", "Explicit Title", pebblestore.ModelPreference{Provider: "test-provider", Model: "chat-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "explicit-title-message", "keep explicit title")
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)
	if !server.WaitForInFlightRuns(2 * time.Second) {
		t.Fatalf("server did not drain")
	}
	stored, ok, err := sessionSvc.GetSession(created.ID)
	if err != nil || !ok {
		t.Fatalf("load session ok=%v err=%v", ok, err)
	}
	if stored.Title != "Explicit Title" {
		t.Fatalf("session title = %q", stored.Title)
	}
	if runner.callCount != 1 {
		t.Fatalf("provider call count = %d, want only assistant", runner.callCount)
	}
}

func createSessionsV3PrimaryTestSessionWithPreference(t *testing.T, server *Server, clientRequestID, title string, pref pebblestore.ModelPreference) pebblestore.SessionSnapshot {
	t.Helper()
	return createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, clientRequestID, title, t.TempDir(), pref)
}

func createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t *testing.T, server *Server, clientRequestID, title, workspacePath string, pref pebblestore.ModelPreference) pebblestore.SessionSnapshot {
	t.Helper()
	payload := map[string]any{
		"client_request_id": clientRequestID,
		"workspace_path":    workspacePath,
		"title":             title,
		"agent_name":        "swarm",
		"preference":        pref,
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

type sessionsV3RecordingProviderRunner struct {
	text          string
	deltas        []string
	err           error
	response      provideriface.Response
	responses     []provideriface.Response
	functionCalls []provideriface.FunctionCall
	handler       func(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error)
	callCount     int
	lastRequest   provideriface.Request
	requests      []provideriface.Request
}

func (r *sessionsV3RecordingProviderRunner) ID() string { return "test-provider" }
func (r *sessionsV3RecordingProviderRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}
func (r *sessionsV3RecordingProviderRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.callCount++
	r.lastRequest = req
	r.requests = append(r.requests, req)
	if r.handler != nil {
		return r.handler(ctx, req, onEvent)
	}
	if r.err != nil {
		return provideriface.Response{}, r.err
	}
	response := r.response
	if len(r.responses) >= r.callCount {
		response = r.responses[r.callCount-1]
	}
	if response.Text == "" {
		response.Text = r.text
	}
	if onEvent != nil {
		deltas := r.deltas
		if len(deltas) == 0 {
			deltas = []string{response.Text}
		}
		for _, delta := range deltas {
			onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: delta})
		}
	}
	if len(r.functionCalls) > 0 {
		response.FunctionCalls = append([]provideriface.FunctionCall(nil), r.functionCalls...)
	}
	return response, nil
}
