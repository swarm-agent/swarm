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

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
)

func TestSessionsV3PrimaryHandlersDoNotUseRuntimeDispatchOrRoutes(t *testing.T) {
	body, err := os.ReadFile("sessions_v3_primary.go")
	if err != nil {
		t.Fatalf("read sessions_v3_primary.go: %v", err)
	}
	for _, required := range []string{"ApplySessionMutation", "SessionMutationCreateSession", "SessionMutationAppendMessage", "RunIntentDispatchBlocked", "ListSessionEvents", "ListSessionMessages"} {
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
	if payload.RunIntent == nil || payload.RunIntent.Status != sessionruntime.RunIntentDispatchBlocked || !strings.Contains(payload.RunIntent.BlockedReason, "invalid dispatch authority") {
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
	if len(events) != 2 || len(second.Events) != 2 {
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
	return server, sessionSvc, store.Close
}
