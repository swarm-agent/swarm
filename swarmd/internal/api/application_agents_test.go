package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestApplicationAgentConversationOwnershipAndContext(t *testing.T) {
	// Purpose: the registered app API must bind canonical V3 sessions, preserve
	// retry identity and pinned execution context, and reject unrelated sessions.
	// HTTP plus real store proves this boundary without a provider/network run.
	t.Setenv("SWARM_API_NO_AUTH", "1")
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	request := func(method, path, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, withTestPrincipal(r))
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w
	}
	root := "/v3/application-agents/editor"
	request(http.MethodPut, root, `{"name":"Editor","instructions":"Draft only","context":"Original","expected_revision":0}`, 200)
	workspace := t.TempDir()
	binding := seedSessionsV3PrimaryAuthority(t, server, workspace)
	body := fmt.Sprintf(`{"client_request_id":"app-conversation","workspace_path":%q,"workspace_binding_id":%q,"swarm_id":"host-swarm-id","target_kind":"host","target_relationship":"self","agent_name":"swarm"}`, workspace, binding)
	response := request(http.MethodPost, root+"/conversations?revision=1", body, 200)
	var created struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Session.ID == "" {
		t.Fatal("missing canonical session")
	}
	persisted, found, err := server.sessions.GetSession(created.Session.ID)
	if err != nil || !found {
		t.Fatalf("persisted session: %v %v", found, err)
	}
	created.Session = persisted
	request(http.MethodPost, root+"/conversations?revision=1", body, 200)
	request(http.MethodPost, root+"/conversations/"+created.Session.ID+"/messages", `{"role":"user","content":"Draft","client_request_id":"event-1"}`, 200)
	request(http.MethodPost, root+"/conversations/"+created.Session.ID+"/messages", `{"role":"user","content":"Draft","client_request_id":"event-1"}`, 200)
	messages, err := server.sessions.ListSessionMessages(created.Session.ID, 0, 10)
	if err != nil || len(messages) != 1 || messages[0].Content != "Draft" {
		t.Fatalf("event retry duplicated chat: %+v %v", messages, err)
	}
	request(http.MethodPut, root, `{"name":"Editor","instructions":"New instructions","context":"Updated","expected_revision":1}`, 200)
	request(http.MethodPut, root, `{"name":"Editor","instructions":"Wrong","context":"Stale","expected_revision":0}`, 409)
	request(http.MethodPost, root+"/conversations?revision=2", body, 409)
	request(http.MethodGet, root+"/conversations/"+created.Session.ID, "", 200)
	instruction, err := server.applicationAgentInstructions(created.Session)
	if err != nil || !strings.Contains(instruction, "Draft only") || !strings.Contains(instruction, "Original") || strings.Contains(instruction, "Updated") {
		t.Fatalf("pinned execution instructions: %q %v", instruction, err)
	}
	for _, field := range []string{"account", "user"} {
		foreign := created.Session
		if field == "account" {
			foreign.AccountScopeID = "other"
		} else {
			foreign.UserID = "other"
		}
		if _, err := server.applicationAgentInstructions(foreign); err == nil {
			t.Fatal("cross-principal context resolved")
		}
	}
	other := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "unrelated", "Unrelated", workspace)
	request(http.MethodPost, root+"/conversations/"+other.ID+"/messages", `{"content":"unauthorized","client_request_id":"event-1"}`, 404)
	request(http.MethodGet, root+"/conversations/"+other.ID, "", 404)
	request(http.MethodPut, "/v3/application-agents/other", `{"name":"Other","instructions":"","context":"","expected_revision":0}`, 200)
	request(http.MethodGet, "/v3/application-agents/other/conversations/"+created.Session.ID, "", 404)
	unchanged := request(http.MethodGet, "/v3/sessions/"+other.ID, "", 200)
	if strings.Contains(unchanged.Body.String(), "unauthorized") {
		t.Fatal("rejected message persisted")
	}
	own := request(http.MethodGet, root+"/conversations/"+created.Session.ID, "", 200)
	if strings.Contains(own.Body.String(), "Draft only") {
		t.Fatal("instructions leaked into chat history")
	}
	if err := validateSessionsV3CreateMetadata(map[string]any{applicationAgentBindingKey: applicationAgentBinding{ID: "editor", Revision: 1}}); err == nil {
		t.Fatal("client can forge ownership")
	}
	merged := mergeSessionsV3MetadataUpdate(created.Session.Metadata, map[string]any{applicationAgentBindingKey: nil})
	if _, err := readApplicationAgentBinding(merged); err != nil {
		t.Fatal("metadata update removed ownership", err)
	}
}

func TestApplicationAgentScopesRejectWithoutMutation(t *testing.T) {
	// Purpose: handleApplicationAgents must enforce read/write scopes before
	// persistence; direct handler requests isolate this authorization boundary.
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPost} {
		r := withTestPrincipal(httptest.NewRequest(method, "/v3/application-agents/denied", strings.NewReader(`{"name":"Denied","expected_revision":0}`)))
		r = requestWithScopedToken(r, &pebblestore.ScopedTokenRecord{})
		w := httptest.NewRecorder()
		server.handleApplicationAgents(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s: %d", method, w.Code)
		}
	}
	p := testPrincipal()
	if _, found, err := server.sessions.Store().GetApplicationAgent(p.AccountScopeID, p.UserID, "denied", 0); err != nil || found {
		t.Fatalf("unauthorized mutation: %v %v", found, err)
	}
}

// Purpose: app resource links must reject missing/foreign resources before writes;
// discovery must survive reload without browser-owned identity lists. Real HTTP
// handlers and Pebble are the narrowest boundary; no provider execution occurs.
func TestApplicationAgentDiscoveryAndResourceGuards(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	p := testPrincipal()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := withTestPrincipal(httptest.NewRequest(method, path, strings.NewReader(body)))
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		return w
	}
	for _, extra := range []string{`,"project_id":"missing"`, `,"worker_ids":["missing"]`} {
		w := request("PUT", "/v3/application-agents/rejected", `{"name":"Rejected","instructions":"","context":"","expected_revision":0`+extra+`}`)
		if w.Code != 404 {
			t.Fatalf("invalid link: %d %s", w.Code, w.Body.String())
		}
		if _, found, _ := server.sessions.Store().GetApplicationAgent(p.AccountScopeID, p.UserID, "rejected", 0); found {
			t.Fatal("invalid link persisted")
		}
	}
	if err := server.sessions.Store().PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: "linked", Name: "Linked", AccountID: p.AccountScopeID}); err != nil {
		t.Fatal(err)
	}
	w := request("PUT", "/v3/application-agents/editor", `{"name":"Editor","instructions":"Draft","context":"Style","expected_revision":0,"project_id":"linked"}`)
	if w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{"/v3/application-agents?limit=1", "/v3/application-agents/editor/tasks", "/v3/application-agents/editor/conversations"} {
		w := request("GET", path, "")
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if w := request("GET", "/v3/application-agents/editor/workers/unlinked", ""); w.Code != 404 {
		t.Fatal("unlinked worker exposed")
	}
	if w := request("PUT", "/v3/application-agents/editor", `{"name":"Changed","expected_revision":1} {}`); w.Code != 400 {
		t.Fatal("trailing JSON accepted")
	}
	record, _, _ := server.sessions.Store().GetApplicationAgent(p.AccountScopeID, p.UserID, "editor", 0)
	if record.Revision != 1 || record.ProjectID != "linked" {
		t.Fatal("rejected request mutated agent")
	}
	if _, err := server.sessions.Store().PutApplicationAgent("foreign", p.UserID, pebblestore.ApplicationAgent{ID: "secret", Name: "Foreign"}, 0); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(request("GET", "/v3/application-agents", "").Body.String(), "Foreign") {
		t.Fatal("cross-account discovery")
	}
}

// Purpose: the app facade must not proxy deployment/token/admin operations or let
// a linked worker bypass canonical token/revision checks. Direct HTTP and a real
// worker store prove rejection leaves the worker unchanged without execution.
func TestApplicationAgentWorkerForwardingGuards(t *testing.T) {
	s, db, _ := setupWorkerAPITestServer(t)
	p := testPrincipal()
	p.AccountScopeID = "acct-test"
	p.UserID = "user-test"
	ws := pebblestore.NewWorkerStore(db)
	worker, err := ws.CreateWorker(p.AccountScopeID, p.UserID, pebblestore.CreateWorkerRequest{Name: "Linked", Instructions: "Draft", IdempotencyKey: "app-worker"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.sessions.Store().PutApplicationAgent(p.AccountScopeID, p.UserID, pebblestore.ApplicationAgent{ID: "editor", Name: "Editor", WorkerIDs: []string{worker.ID}}, 0); err != nil {
		t.Fatal(err)
	}
	root := "/v3/application-agents/editor/workers/" + worker.ID
	for _, tc := range []struct {
		path   string
		token  *pebblestore.ScopedTokenRecord
		status int
	}{
		{root + "/activate", nil, 404},
		{root + "/token", nil, 404},
		{root + "/automations/a/enable", nil, 404},
		{root + "/automations", &pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:write"}}, 403},
		{root + "/automations", &pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:write", "automations:write"}, WorkerID: "foreign"}, 403},
		{root + "/automations", nil, 400},
	} {
		r := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{"automation":{"name":"New"}}`)), p.UserID, p.AccountScopeID)
		if tc.token != nil {
			r = requestWithScopedToken(r, tc.token)
		}
		w := httptest.NewRecorder()
		s.handleApplicationAgents(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: got %d want %d: %s", tc.path, w.Code, tc.status, w.Body.String())
		}
	}
	after, found, err := s.sessions.GetWorker(p.AccountScopeID, worker.ID)
	if err != nil || !found || after.Revision != worker.Revision || len(after.Automations) != 0 || after.LifecycleState != worker.LifecycleState {
		t.Fatalf("rejected forwarding changed worker: %+v %v", after, err)
	}
}

// Purpose: exact route/method dispatch prevents future worker admin routes from
// becoming app authority accidentally. This pure allowlist test complements HTTP.
func TestApplicationWorkerRouteAllowlist(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		allowed      bool
	}{
		{"GET", "runs", true}, {"POST", "automations", true},
		{"PUT", "automations/a", true}, {"DELETE", "automations/a", true},
		{"POST", "automations/a/trigger", true}, {"POST", "trigger", true},
		{"GET", "automations", false}, {"POST", "deploy", false},
		{"POST", "automations/a/enable", false}, {"POST", "automations/../trigger", false},
		{"POST", "automations/a/trigger/extra", false},
	} {
		if got := applicationWorkerRouteAllowed(tc.method, strings.Split(tc.path, "/")); got != tc.allowed {
			t.Fatalf("%s %s: %v", tc.method, tc.path, got)
		}
	}
}

// Purpose: registered app configuration must commit a canonical worker revision
// without activation, emit an actual content-free websocket invalidation, and
// reject stale/foreign writes without advancing durable state or the outbox.
// Real HTTP handlers, Pebble and loopback websocket are the narrowest end-to-end
// boundary; no provider, scheduler or deployed worker is involved.
func TestApplicationAgentWorkerConfigurationRealtime(t *testing.T) {
	t.Setenv("SWARM_API_NO_AUTH", "1")
	s, _, db := newWorkspaceOverviewTopologyTestServer(t)
	s.ConfigureAutomationRealtime(db)
	p := testPrincipal()
	ids := pebblestore.NewIdentityStore(db)
	if _, err := ids.PutUser(pebblestore.UserRecord{ID: p.UserID, Username: p.UserID}); err != nil {
		t.Fatal(err)
	}
	if _, err := ids.PutAccountScope(pebblestore.AccountScopeRecord{ID: p.AccountScopeID, Type: pebblestore.AccountScopeTypePersonal, CreatedByUserID: p.UserID}); err != nil {
		t.Fatal(err)
	}
	if _, err := ids.PutAccountUser(pebblestore.AccountUserRecord{ID: "app-member", AccountScopeID: p.AccountScopeID, UserID: p.UserID, Status: pebblestore.AccountUserStatusActive}); err != nil {
		t.Fatal(err)
	}
	ws := pebblestore.NewWorkerStore(db)
	worker, err := ws.CreateWorker(p.AccountScopeID, p.UserID, pebblestore.CreateWorkerRequest{Name: "Linked", Instructions: "Draft", IdempotencyKey: "app-config"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.sessions.Store().PutApplicationAgent(p.AccountScopeID, p.UserID, pebblestore.ApplicationAgent{ID: "editor", Name: "Editor", WorkerIDs: []string{worker.ID}}, 0); err != nil {
		t.Fatal(err)
	}
	initial, err := s.sessions.CurrentRealtimeOutboxRevision()
	if err != nil {
		t.Fatal(err)
	}
	host := newV3RealtimeHTTPTestServer(t, s)
	conn := dialV3RealtimeStream(t, host.URL)
	defer conn.Close()
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: signedV3RealtimeCursorForTest(t, s, initial), Worksets: []V3RealtimeWorksetSubscriptionRequest{v3RealtimeGlobalWorksetRequestForTest()}})
	path := "/v3/application-agents/editor/workers/" + worker.ID + "/automations"
	body := fmt.Sprintf(`{"expected_worker_revision":%d,"automation":{"name":"Draft job","activation_mode":"manual","enabled":true,"plan_document":{"title":"Draft","info":{"goal":"Prepare draft"},"checkpoints":[{"id":"cp-1","title":"Draft","status":"pending","order":1,"tasks":["Prepare draft"],"acceptance_criteria":["Draft exists"]}]}}}`, worker.Revision)
	request := func(user, account string) *httptest.ResponseRecorder {
		r := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)), user, account)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	if w := request(p.UserID, p.AccountScopeID); w.Code != http.StatusCreated {
		t.Fatalf("configuration: %d %s", w.Code, w.Body.String())
	}
	after, found, err := ws.GetWorker(p.AccountScopeID, worker.ID)
	if err != nil || !found || after.Revision != worker.Revision+1 || len(after.Automations) != 0 || after.LifecycleState != worker.LifecycleState || after.PendingReview == nil || len(after.PendingReview.Automations) != 1 || after.PendingReview.Automations[0].Name != "Draft job" || after.PendingReview.LifecycleState != pebblestore.WorkerLifecycleStatePending {
		t.Fatalf("configuration bypassed pending gate: %+v %v", after, err)
	}
	head, err := s.sessions.CurrentRealtimeOutboxRevision()
	if err != nil || head <= initial {
		t.Fatalf("no durable event: %d %v", head, err)
	}
	seen := false
	for i := 0; i < 12; i++ {
		frame := readV3RealtimeFrame(t, conn)
		if frame.Kind == V3RealtimeKindWorkerChanged {
			if frame.EndpointCursor == "" || frame.Session != nil || frame.Event != nil {
				t.Fatalf("invalid resource frame: %+v", frame)
			}
			seen = true
			break
		}
	}
	if !seen {
		t.Fatal("worker resource event missing")
	}
	if w := request(p.UserID, p.AccountScopeID); w.Code != http.StatusConflict {
		t.Fatalf("stale write: %d %s", w.Code, w.Body.String())
	}
	for _, principal := range [][2]string{{"foreign-user", p.AccountScopeID}, {p.UserID, "foreign-account"}} {
		if w := request(principal[0], principal[1]); w.Code != http.StatusNotFound {
			t.Fatalf("foreign app: %d %s", w.Code, w.Body.String())
		}
	}
	unchanged, _, err := ws.GetWorker(p.AccountScopeID, worker.ID)
	finalHead, headErr := s.sessions.CurrentRealtimeOutboxRevision()
	beforeJSON, _ := json.Marshal(after)
	afterJSON, _ := json.Marshal(unchanged)
	if err != nil || headErr != nil || finalHead != head || !bytes.Equal(beforeJSON, afterJSON) {
		t.Fatalf("rejection changed worker/outbox: %v %v %d != %d", err, headErr, finalHead, head)
	}
}
