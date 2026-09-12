package api

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/memory"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: handleMemory is the authenticated HTTP boundary. Real temp storage
// proves missing identity, forged account/provenance, stale revision and malformed
// input cannot mutate memory; successful explicit authoring and restore use CAS.
func TestMemoryAPIIsolationAndCAS(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ms := store.NewMemoryStore(db)
	s := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	s.SetMemoryService(memory.NewService(ms, nil))
	call := func(account, method, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/v1/memory", strings.NewReader(body))
		if account != "" {
			r = r.WithContext(identity.ContextWithPrincipal(r.Context(), identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: account, UserID: "user"}))
		}
		w := httptest.NewRecorder()
		s.handleMemory(w, r)
		return w
	}
	if w := call("", "GET", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	d, _ := ms.GetForAccount("a")
	payload := func(rev int64, content string) string {
		b, _ := json.Marshal(map[string]any{"action": "remember", "expected_revision": rev, "reason": "explicit user request", "entry": map[string]any{"id": "rule", "kind": "rule", "content": content, "purpose": "project_context", "subject": "Build"}})
		return string(b)
	}
	w := call("a", "POST", payload(d.Revision, "original"))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	saved, _ := ms.GetForAccount("a")
	if saved.Entries[0].Origin != "user" || saved.Entries[0].Purpose != "project_context" || saved.Entries[0].Subject != "Build" {
		t.Fatal("HTTP metadata lost")
	}
	for _, body := range []string{`{"action":"remember","expected_revision":2,"reason":"forge","entry":{"id":"x","kind":"rule","content":"x","origin":"learned"}}`, `{"action":"remember","expected_revision":2,"reason":"invalid","entry":{"id":"x","kind":"rule","content":"x","purpose":"permission"}}`, payload(d.Revision, "stale"), `{"action":"remember","account_scope_id":"b"}`, `{"action":"remember"} {}`, `{"action":"remember","expected_revision":2,"reason":"forge","entry":{"id":"x","kind":"learned","content":"x","sources":[{"session_id":"foreign"}]}}`} {
		if w := call("a", "POST", body); w.Code < 400 {
			t.Fatal("accepted invalid request", body)
		}
		after, _ := ms.GetForAccount("a")
		if !reflect.DeepEqual(saved, after) {
			t.Fatal("partial mutation")
		}
	}
	b, _ := ms.GetForAccount("b")
	if len(b.Entries) != 0 {
		t.Fatal("cross-account leak")
	}
	if w := call("a", "POST", payload(saved.Revision, "new")); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	current, _ := ms.GetForAccount("a")
	// Edit changes independent purpose/scope but cannot relabel provenance.
	edit, _ := json.Marshal(map[string]any{"action": "edit", "expected_revision": current.Revision, "reason": "edit metadata", "entry": map[string]any{"id": "rule", "content": "edited", "purpose": "recovery", "subject": "Recovery", "origin": "learned", "created_at": 1, "workspace_id": "scope"}})
	if w := call("a", "POST", string(edit)); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	edited, _ := ms.GetForAccount("a")
	if edited.Entries[0].Origin != "user" || edited.Entries[0].CreatedAt != current.Entries[0].CreatedAt || edited.Entries[0].Purpose != "recovery" || edited.Entries[0].WorkspaceID != "scope" {
		t.Fatal("edit lost or forged metadata")
	}
	if w := call("a", "POST", string(edit)); w.Code != 409 {
		t.Fatal("stale edit accepted")
	}
	unchanged, _ := ms.GetForAccount("a")
	if !reflect.DeepEqual(edited, unchanged) {
		t.Fatal("stale edit mutated state")
	}
	current = edited
	raw, _ := json.Marshal(map[string]any{"action": "restore", "expected_revision": current.Revision, "reason": "undo edit", "entry_id": "rule", "restore_revision": saved.Revision})
	if w := call("a", "POST", string(raw)); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	restored, _ := ms.GetForAccount("a")
	if restored.Revision <= current.Revision || restored.Entries[0].Content != "original" {
		t.Fatal("restore did not create revision")
	}
}
