package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a gateway token sets a session's end_user and labels once, when it
// creates the session: they are stored in protected metadata and returned on
// create and on reads; a retry of the same create with a different end user is
// a conflict, the metadata route refuses to change them (and keeps them when
// other keys change), and nobody but a gateway token for that agent may set
// them (owner, operator token, another agent's token), with malformed values
// refused. Nothing is stored by a refused create. Threat: a visitor or the
// model re-pointing a conversation at another customer's data or budget.
// Boundary: createSessionsV3Primary, sessionsV3EndUserBinding,
// handleSessionV3PrimaryMetadata, isProtectedSessionsV3MetadataKey.
// Narrowest layer: the create, read and metadata handlers over a real store.
func TestSessionsV3EndUserIsSetOnceByTheGateway(t *testing.T) {
	server, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := filepath.Join(t.TempDir(), "scratch")
	bindingID := seedSessionsV3PrimaryAuthority(t, server, workspace)
	principal := testPrincipal()
	account := principal.AccountScopeID
	if _, err := server.agents.PutCustomToolForAccount(account, pebblestore.AgentCustomToolDefinition{Name: "lookup_order", Kind: pebblestore.AgentCustomToolKindClient, Description: "x", InputSchema: lookupOrderSchema}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := server.agents.UpsertForAccount(account, agentruntime.UpsertInput{Name: "frontdesk", Mode: agentruntime.ModeSubagent, Enabled: pebblestore.BoolPtr(true), Prompt: "x",
		ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"lookup_order": {Enabled: pebblestore.BoolPtr(true)}}}}); err != nil {
		t.Fatal(err)
	}
	gateway := &pebblestore.ScopedTokenRecord{ID: "tok_gw", AgentName: "frontdesk", AccountScopeID: account, UserID: principal.UserID, Scopes: []string{"sessions:read", "sessions:write"}}
	operator := &pebblestore.ScopedTokenRecord{ID: "tok_op", AgentName: "frontdesk", AgentOperator: true, AccountScopeID: account, UserID: principal.UserID, Scopes: []string{"sessions:read", "sessions:write"}}
	other := &pebblestore.ScopedTokenRecord{ID: "tok_other", AgentName: "backdesk", AccountScopeID: account, UserID: principal.UserID, Scopes: []string{"sessions:read", "sessions:write"}}
	create := func(token *pebblestore.ScopedTokenRecord, requestID string, extra map[string]any) (int, map[string]any) {
		t.Helper()
		payload := map[string]any{"client_request_id": requestID, "workspace_path": workspace, "workspace_name": "scratch", "swarm_id": "host-swarm-id",
			"workspace_binding_id": bindingID, "target_kind": "host", "target_relationship": "self", "title": "Visitor", "mode": "auto", "agent_name": "frontdesk"}
		for key, value := range extra {
			payload[key] = value
		}
		raw, _ := json.Marshal(payload)
		r := withTestPrincipal(httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewReader(raw)))
		if token != nil {
			r = requestWithScopedToken(r, token)
		}
		w := httptest.NewRecorder()
		server.handleSessionsV3PrimaryCreate(w, r, principal)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	code, created := create(gateway, "visit-1", map[string]any{"end_user": "Cust_42", "labels": map[string]string{"plan": "pro", "site": "example.com"}})
	if code != http.StatusOK || created["end_user"] != "Cust_42" {
		t.Fatalf("gateway create: %d %v", code, created)
	}
	sessionID, _ := created["session_id"].(string)
	stored := func() pebblestore.SessionEndUser {
		t.Helper()
		snapshot, ok, err := sessions.GetSession(sessionID)
		if err != nil || !ok {
			t.Fatalf("session: %v %v", ok, err)
		}
		binding, bound, err := pebblestore.SessionEndUserFromMetadata(snapshot.Metadata)
		if err != nil || !bound {
			t.Fatalf("binding: %v %v", bound, err)
		}
		return binding
	}
	if got := stored(); got.EndUser != "Cust_42" || got.AgentName != "frontdesk" || got.Labels["plan"] != "pro" || got.Labels["site"] != "example.com" {
		t.Fatalf("stored binding %+v", got)
	}
	read := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(read, requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v3/sessions/"+sessionID, nil)), gateway))
	var detail map[string]any
	_ = json.Unmarshal(read.Body.Bytes(), &detail)
	if read.Code != http.StatusOK || detail["end_user"] != "Cust_42" {
		t.Fatalf("read: %d end_user=%v", read.Code, detail["end_user"])
	}

	// The same create retried with the same end user replays; with another it conflicts.
	if code, replay := create(gateway, "visit-1", map[string]any{"end_user": "Cust_42", "labels": map[string]string{"plan": "pro", "site": "example.com"}}); code != http.StatusOK || replay["session_id"] != sessionID {
		t.Fatalf("replay: %d %v", code, replay)
	}
	if code, _ := create(gateway, "visit-1", map[string]any{"end_user": "Cust_43"}); code != http.StatusConflict {
		t.Fatalf("retry with another end user: %d, want 409", code)
	}
	metadata := func(body string) int {
		w := httptest.NewRecorder()
		server.handleSessionV3PrimaryMetadata(w, withTestPrincipal(httptest.NewRequest(http.MethodPost, "/v3/sessions/"+sessionID+"/metadata", strings.NewReader(body))), principal, sessionID)
		return w.Code
	}
	if code := metadata(`{"client_request_id":"meta-1","metadata":{"swarm_v3_end_user":{"agent_name":"frontdesk","end_user":"Cust_43"}}}`); code != http.StatusBadRequest {
		t.Fatalf("metadata rewrite of the end user: %d, want 400", code)
	}
	if code := metadata(`{"client_request_id":"meta-2","metadata":{"note":"x"}}`); code != http.StatusOK {
		t.Fatalf("unrelated metadata change: %d", code)
	}
	if got := stored(); got.EndUser != "Cust_42" || got.Labels["plan"] != "pro" {
		t.Fatalf("the end user changed after creation: %+v", got)
	}

	for _, tc := range []struct {
		name  string
		token *pebblestore.ScopedTokenRecord
		extra map[string]any
	}{
		{"owner", nil, map[string]any{"end_user": "Cust_42"}},
		{"operator token", operator, map[string]any{"end_user": "Cust_42"}},
		{"another agent's token", other, map[string]any{"end_user": "Cust_42"}},
		{"owner metadata", nil, map[string]any{"metadata": map[string]any{pebblestore.SessionEndUserMetadataKey: map[string]any{"agent_name": "frontdesk", "end_user": "Cust_42"}}}},
		{"space", gateway, map[string]any{"end_user": "a b"}},
		{"slash", gateway, map[string]any{"end_user": "a/b"}},
		{"too long", gateway, map[string]any{"end_user": strings.Repeat("x", 129)}},
		{"label key", gateway, map[string]any{"end_user": "ok", "labels": map[string]string{"Bad Key": "x"}}},
		{"label value", gateway, map[string]any{"end_user": "ok", "labels": map[string]string{"k": strings.Repeat("v", 257)}}},
		{"label control", gateway, map[string]any{"end_user": "ok", "labels": map[string]string{"k": "a\nb"}}},
		{"too many labels", gateway, map[string]any{"end_user": "ok", "labels": manyLabels(17)}},
	} {
		code, _ := create(tc.token, "refused-"+strings.ReplaceAll(tc.name, " ", "-"), tc.extra)
		if code != http.StatusBadRequest {
			t.Fatalf("%s: %d, want 400", tc.name, code)
		}
		if _, found, _ := sessions.GetSession(stableSessionsV3PrimarySessionID(principal, "refused-"+strings.ReplaceAll(tc.name, " ", "-"))); found {
			t.Fatalf("%s: a refused create stored a session", tc.name)
		}
	}
	// A session created without one has none, and reads say nothing about it.
	if code, plain := create(gateway, "visit-plain", nil); code != http.StatusOK || plain["end_user"] != nil || plain["labels"] != nil {
		t.Fatalf("create without end_user: %d %v", code, plain)
	}
}

func manyLabels(n int) map[string]string {
	labels := map[string]string{}
	for i := 0; i < n; i++ {
		labels["k"+strings.Repeat("x", i)] = "v"
	}
	return labels
}
