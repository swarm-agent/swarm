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
