package session

import (
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: CLI continuation updates must use ApplySessionMutation, preserve
// unrelated session state and reject cross-owner writes without any projection
// or event mutation. Temporary Pebble plus Service is the narrowest durable
// boundary; process restart/resume is exercised by the provider package.
func TestClaudeCodeSessionMutationOwnerAndPreservation(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := NewService(pebblestore.NewSessionStore(store), nil)
	p := identity.Principal{AccountScopeID: "account", UserID: "user"}
	_, err = service.ApplySessionMutation(SessionMutationInput{Kind: SessionMutationCreateSession, SessionID: "fixture", AccountScopeID: p.AccountScopeID, UserID: p.UserID, ClientRequestID: "create", PayloadHash: "create", Session: &pebblestore.SessionSnapshot{ID: "fixture", AccountScopeID: p.AccountScopeID, UserID: p.UserID, Title: "keep-title", ClaudeCodeSessionID: "forged-at-create", Metadata: map[string]any{"keep": "value"}}})
	if err != nil {
		t.Fatal(err)
	}
	if id, err := service.ClaudeCodeSession(p, "fixture"); err != nil || id != "" {
		t.Fatalf("snapshot forged continuation: %q %v", id, err)
	}
	if err = service.RecordClaudeCodeSession(p, "fixture", "cli-session"); err != nil {
		t.Fatal(err)
	}
	before, _, err := service.GetSessionProjection("fixture")
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range []identity.Principal{{AccountScopeID: "other", UserID: p.UserID}, {AccountScopeID: p.AccountScopeID, UserID: "other"}} {
		if err = service.RecordClaudeCodeSession(other, "fixture", "forged"); err == nil {
			t.Fatal("cross-owner continuation accepted")
		}
		if _, err = service.ClaudeCodeSession(other, "fixture"); err == nil {
			t.Fatal("cross-owner continuation disclosed")
		}
	}
	after, _, err := service.GetSessionProjection("fixture")
	if err != nil {
		t.Fatal(err)
	}
	if before.LastEventSeq != after.LastEventSeq {
		t.Fatal("rejected update appended an event")
	}
	row, _, err := service.GetSession("fixture")
	if err != nil {
		t.Fatal(err)
	}
	if row.ClaudeCodeSessionID != "cli-session" || row.Title != "keep-title" || row.Metadata["keep"] != "value" {
		t.Fatalf("session state changed: %+v", row)
	}
	// An ordinary stale snapshot cannot erase or forge provider-owned state.
	row.ClaudeCodeSessionID = "forged"
	_, err = service.ApplySessionMutation(SessionMutationInput{Kind: SessionMutationUpdateMetadata, SessionID: "fixture", AccountScopeID: p.AccountScopeID, UserID: p.UserID, ClientRequestID: "ordinary", PayloadHash: "ordinary", Session: &row})
	if err != nil {
		t.Fatal(err)
	}
	id, err := service.ClaudeCodeSession(p, "fixture")
	if err != nil || id != "cli-session" {
		t.Fatalf("provider state overwritten: %q %v", id, err)
	}
}
