package api

import (
	"net/http"
	"net/url"
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a client can see and curate the Companion's context bank through
// one bounded, scoped route that never touches account memory. Requirement:
// GET /v3/companion/context lists with a cursor and searches with verified
// source excerpts marked untrusted; POST records only decision/work items
// whose source belongs to this user (idempotently, revision-guarded) and
// refuses a workspace outside the Companion's current scope; DELETE needs the
// current revision; deleting the Companion purges its context bank. Threat: a
// client recording another user's message as evidence, binding context to a
// workspace it cannot access, a lost update, or deleted Companion data
// lingering in the bank. Boundary: handleCompanionContext over
// SessionStore.Put/Retrieve/List/DeleteCompanionContext and
// handleCompanionDelete's purge. Narrowest layer: the HTTP handlers over a
// real store.
func TestCompanionContextRouteIsScopedVerifiedAndPurgedWithTheCompanion(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := filepath.Join(t.TempDir(), "scratch")
	seedSessionsV3PrimaryAuthority(t, server, workspace)
	code, body := companionTestCall(t, server, http.MethodPost, CompanionPath, map[string]any{"workspace_path": workspace})
	if code != http.StatusOK {
		t.Fatalf("ensure: %d %v", code, body)
	}
	companion := companionTestView(t, body)
	if companion.Routes.Context != CompanionContextPath {
		t.Fatalf("view does not name the context route: %+v", companion.Routes)
	}
	store := server.sessions.Store()
	principal := testPrincipal()
	appended, err := store.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: companion.SessionID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Kind: pebblestore.V3SessionMutationAppendMessage, ClientRequestID: "m1", PayloadHash: "m1", Message: &pebblestore.MessageSnapshot{Role: "user", Content: "We decided to keep the vendor printer driver"}})
	if err != nil || appended.Message == nil {
		t.Fatalf("append: %v", err)
	}
	memoryBefore, err := pebblestore.NewMemoryStore(store.Underlying()).GetForAccount(principal.AccountScopeID)
	if err != nil {
		t.Fatal(err)
	}
	source := map[string]any{"kind": "message", "session_id": companion.SessionID, "message_seq": appended.Message.GlobalSeq}
	decision := map[string]any{"id": "printer-driver", "kind": "decision", "title": "Keep the vendor printer driver", "source": source}

	code, body = companionTestCall(t, server, http.MethodPost, CompanionContextPath, decision)
	if code != http.StatusOK || body["changed"] != true {
		t.Fatalf("record decision: %d %v", code, body)
	}
	if code, body = companionTestCall(t, server, http.MethodPost, CompanionContextPath, decision); code != http.StatusOK || body["changed"] != false {
		t.Fatalf("idempotent repeat: %d %v", code, body)
	}
	edited := map[string]any{"id": "printer-driver", "kind": "decision", "title": "Edited", "source": source}
	if code, _ = companionTestCall(t, server, http.MethodPost, CompanionContextPath, edited); code != http.StatusConflict {
		t.Fatalf("stale-revision edit: %d", code)
	}

	refusals := []struct {
		name string
		req  map[string]any
		want int
	}{
		{"topic kind", map[string]any{"id": "t1", "kind": "topic", "title": "x", "source": source}, http.StatusBadRequest},
		{"foreign session", map[string]any{"id": "f1", "kind": "decision", "title": "x", "source": map[string]any{"kind": "message", "session_id": "someone-else", "message_seq": 1}}, http.StatusNotFound},
		{"missing message", map[string]any{"id": "f2", "kind": "decision", "title": "x", "source": map[string]any{"kind": "message", "session_id": companion.SessionID, "message_seq": 9999}}, http.StatusNotFound},
		{"workspace outside scope", map[string]any{"id": "w1", "kind": "decision", "title": "x", "workspace_path": filepath.Join(t.TempDir(), "elsewhere"), "source": source}, http.StatusForbidden},
	}
	for _, refusal := range refusals {
		if code, body := companionTestCall(t, server, http.MethodPost, CompanionContextPath, refusal.req); code != refusal.want {
			t.Fatalf("%s: %d %v, want %d", refusal.name, code, body, refusal.want)
		}
	}

	code, body = companionTestCall(t, server, http.MethodGet, CompanionContextPath+"?q="+url.QueryEscape("printer driver"), nil)
	results, _ := body["results"].([]any)
	if code != http.StatusOK || len(results) != 1 || body["content_trust"] != "untrusted" {
		t.Fatalf("search: %d %v", code, body)
	}
	first, _ := results[0].(map[string]any)
	if first["source_text"] != "We decided to keep the vendor printer driver" {
		t.Fatalf("search lacks source evidence: %v", first)
	}
	code, body = companionTestCall(t, server, http.MethodGet, CompanionContextPath+"?limit=1", nil)
	records, _ := body["records"].([]any)
	if code != http.StatusOK || len(records) != 1 {
		t.Fatalf("list: %d %v", code, body)
	}
	if code, _ = companionTestCall(t, server, http.MethodGet, CompanionContextPath+"?q=printer&cursor=abc", nil); code != http.StatusBadRequest {
		t.Fatalf("cursor with search: %d", code)
	}

	if code, _ = companionTestCall(t, server, http.MethodDelete, CompanionContextPath, map[string]any{"id": "printer-driver", "expected_revision": 7}); code != http.StatusConflict {
		t.Fatalf("stale delete: %d", code)
	}
	if code, _ = companionTestCall(t, server, http.MethodDelete, CompanionContextPath, map[string]any{"id": "printer-driver", "expected_revision": 1}); code != http.StatusOK {
		t.Fatalf("delete: %d", code)
	}
	if code, body = companionTestCall(t, server, http.MethodPost, CompanionContextPath, decision); code != http.StatusOK {
		t.Fatalf("re-record: %d %v", code, body)
	}

	// Deleting the Companion purges its context bank; account memory is untouched.
	if code, body = companionTestCall(t, server, http.MethodDelete, CompanionPath, map[string]any{"session_id": companion.SessionID, "confirm": true}); code != http.StatusOK {
		t.Fatalf("companion delete: %d %v", code, body)
	}
	if records, _, _, err := store.ListCompanionContext(principal.AccountScopeID, principal.UserID, "", 10); err != nil || len(records) != 0 {
		t.Fatalf("context bank survived Companion delete: %d %v", len(records), err)
	}
	memoryAfter, err := pebblestore.NewMemoryStore(store.Underlying()).GetForAccount(principal.AccountScopeID)
	if err != nil || memoryAfter.Revision != memoryBefore.Revision {
		t.Fatalf("account memory changed: %d -> %d (%v)", memoryBefore.Revision, memoryAfter.Revision, err)
	}
}
