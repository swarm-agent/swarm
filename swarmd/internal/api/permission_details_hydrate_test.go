package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
)

// Requirement: collapsed-card questions use exact durable permission identity
// without resolving model/media runtime or transcript history. Real sync handler,
// permission service and temporary store prove bounded targeted hydration and
// cross-account rejection without mutating pending requests. No provider fixture
// or timing assertion is used: this is an authority/shape regression.
func TestPermissionDetailsHydrateIsTargetedAndLightweight(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	log, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), log)
	perms := permission.NewService(pebblestore.NewPermissionStore(store), log, nil)
	perms.SetSessionResolver(sessions)
	server := NewServer(nil, nil, nil, nil, sessions, nil, nil, nil, nil, perms, nil, log, stream.NewHub(log))
	p := testPrincipal()
	create := func(account string) string {
		session, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{UserID: p.UserID, AccountScopeID: account, Title: "Card", WorkspacePath: t.TempDir(), Preference: &pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "low"}})
		if err != nil {
			t.Fatal(err)
		}
		return session.ID
	}
	id := create(p.AccountScopeID)
	empty := create(p.AccountScopeID)
	foreign := create("foreign-account")
	record, err := perms.CreatePending(permission.CreateInput{SessionID: id, RunID: "run", CallID: "call", ToolName: "ask_user", ToolArguments: `{"questions":[{"id":"q","question":"Choose","options":["A","B"]}]}`, Requirement: "ask", Mode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	read := func(path string, ids []string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"surface": "desktop", "session_ids": ids, "history": map[string]any{"mode": "none"}, "resources": map[string]any{"permission_details": true, "permission_summaries": true, "active_plan": true}})
		req := withTestPrincipal(httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, req)
		return w
	}
	w := read("/v3/sync/hydrate", []string{id, empty})
	if w.Code != 200 {
		t.Fatalf("hydrate %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Views map[string]map[string]json.RawMessage `json:"session_views_by_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	for _, sid := range []string{id, empty} {
		v := response.Views[sid]
		if v["pending_permissions"] == nil || v["agentic_settings"] != nil || v["media_capability"] != nil {
			t.Fatalf("not a permission-only view: %s", w.Body.String())
		}
	}
	var pending []pebblestore.PermissionRecord
	if err := json.Unmarshal(response.Views[id]["pending_permissions"], &pending); err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != record.ID || pending[0].RunID != "run" || pending[0].CallID != "call" || pending[0].ToolArguments != record.ToolArguments {
		t.Fatal("permission identity or question changed")
	}
	if string(response.Views[empty]["pending_permissions"]) != "[]" {
		t.Fatal("known absence must be an explicit empty list")
	}
	denied := read("/v3/sync/hydrate", []string{foreign})
	if denied.Code == 200 {
		var foreignResponse struct {
			Sessions map[string]json.RawMessage `json:"sessions_by_id"`
			Views    map[string]json.RawMessage `json:"session_views_by_id"`
		}
		if err := json.Unmarshal(denied.Body.Bytes(), &foreignResponse); err != nil {
			t.Fatal(err)
		}
		if len(foreignResponse.Sessions) != 0 || len(foreignResponse.Views) != 0 {
			t.Fatal("foreign session resources exposed")
		}
	}
	tooMany := make([]string, 9)
	for i := range tooMany {
		tooMany[i] = create(p.AccountScopeID)
	}
	if got := read("/v3/sync/hydrate", tooMany); got.Code != 400 {
		t.Fatalf("unbounded detail hydrate status=%d", got.Code)
	}
	if got := read("/v3/sync/bootstrap", []string{id}); got.Code != 400 {
		t.Fatalf("untargeted bootstrap details status=%d", got.Code)
	}
	unchanged, err := perms.ListPending(id, 200)
	if err != nil || len(unchanged) != 1 || unchanged[0].ID != record.ID {
		t.Fatal("read mutated pending permission")
	}
}
