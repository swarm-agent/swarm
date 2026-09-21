package pebblestore

import (
	"path/filepath"
	"testing"
)

func TestClientAuthStoreMigratesAttachTokenToSecretStore(t *testing.T) {
	root := t.TempDir()
	mainStore, err := Open(filepath.Join(root, "main"))
	if err != nil {
		t.Fatal(err)
	}
	defer mainStore.Close()
	secretStore, err := Open(filepath.Join(root, "secret"))
	if err != nil {
		t.Fatal(err)
	}
	defer secretStore.Close()

	legacy := AttachAuthRecord{Token: "legacy-token", CreatedAt: 1, UpdatedAt: 2}
	if err := mainStore.PutJSON(KeyAuthAttachDefault, legacy); err != nil {
		t.Fatal(err)
	}
	store := NewClientAuthStoreWithSecretStore(mainStore, secretStore)
	got, ok, err := store.GetAttachAuth()
	if err != nil {
		t.Fatalf("GetAttachAuth: %v", err)
	}
	if !ok || got.Token != legacy.Token {
		t.Fatalf("record = %#v, ok=%v", got, ok)
	}
	if _, ok, err := mainStore.GetBytes(KeyAuthAttachDefault); err != nil || ok {
		t.Fatalf("legacy record remains: ok=%v err=%v", ok, err)
	}
	var migrated AttachAuthRecord
	if ok, err := secretStore.GetJSON(KeyAuthAttachDefault, &migrated); err != nil || !ok || migrated.Token != legacy.Token {
		t.Fatalf("migrated record = %#v, ok=%v err=%v", migrated, ok, err)
	}
}

func TestScopedTokenStoreCRUD(t *testing.T) {
	root := t.TempDir()
	store, err := Open(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	authStore := NewClientAuthStore(store)

	record := ScopedTokenRecord{
		ID:             "tok_test_123",
		Name:           "CI Deploy Token",
		TokenHash:      "hash_abcdef123456",
		TokenHint:      "swk_1234...cdef",
		Scopes:         []string{"automations:trigger", "sessions:read"},
		AccountScopeID: "acct_test",
		UserID:         "user_test",
		CreatedAt:      1000,
		ExpiresAt:      2000,
		Revoked:        false,
	}

	if err := authStore.PutScopedToken(record); err != nil {
		t.Fatalf("PutScopedToken failed: %v", err)
	}

	// Retrieve by ID
	got, ok, err := authStore.GetScopedToken("acct_test", "tok_test_123")
	if err != nil || !ok {
		t.Fatalf("GetScopedToken failed: ok=%v err=%v", ok, err)
	}
	if got.Name != "CI Deploy Token" || len(got.Scopes) != 2 {
		t.Fatalf("unexpected record: %#v", got)
	}

	// Retrieve by Hash
	gotHash, ok, err := authStore.GetScopedTokenByHash("hash_abcdef123456")
	if err != nil || !ok {
		t.Fatalf("GetScopedTokenByHash failed: ok=%v err=%v", ok, err)
	}
	if gotHash.ID != "tok_test_123" {
		t.Fatalf("unexpected record by hash: %#v", gotHash)
	}

	// List
	list, err := authStore.ListScopedTokens("acct_test")
	if err != nil {
		t.Fatalf("ListScopedTokens failed: %v", err)
	}
	if len(list) != 1 || list[0].ID != "tok_test_123" {
		t.Fatalf("unexpected list: %#v", list)
	}

	// Revoke
	revoked, err := authStore.RevokeScopedToken("acct_test", "tok_test_123")
	if err != nil {
		t.Fatalf("RevokeScopedToken failed: %v", err)
	}
	if !revoked.Revoked {
		t.Fatalf("expected Revoked=true, got %v", revoked.Revoked)
	}

	// Verify revoked state in hash lookup
	gotHashRevoked, ok, err := authStore.GetScopedTokenByHash("hash_abcdef123456")
	if err != nil || !ok || !gotHashRevoked.Revoked {
		t.Fatalf("expected revoked in hash lookup, got: ok=%v %#v", ok, gotHashRevoked)
	}

	// Delete
	if err := authStore.DeleteScopedToken("acct_test", "tok_test_123"); err != nil {
		t.Fatalf("DeleteScopedToken failed: %v", err)
	}
	_, ok, err = authStore.GetScopedToken("acct_test", "tok_test_123")
	if err != nil || ok {
		t.Fatalf("expected deleted, got ok=%v", ok)
	}
	_, ok, err = authStore.GetScopedTokenByHash("hash_abcdef123456")
	if err != nil || ok {
		t.Fatalf("expected deleted by hash, got ok=%v", ok)
	}
}

func TestScopedTokenHasScope(t *testing.T) {
	tok := &ScopedTokenRecord{
		Scopes: []string{"automations:trigger", "sessions:read", "workspaces:*"},
	}

	// Exact matches
	if !tok.HasScope("automations:trigger") {
		t.Errorf("expected automations:trigger to match")
	}
	if !tok.HasScope("sessions:read") {
		t.Errorf("expected sessions:read to match")
	}

	// Workers alias for automations
	if !tok.HasScope("workers:trigger") {
		t.Errorf("expected workers:trigger to match automations:trigger")
	}

	// Wildcard
	if !tok.HasScope("workspaces:read") || !tok.HasScope("workspaces:write") {
		t.Errorf("expected workspaces:* to match workspaces:read and workspaces:write")
	}

	// Missing scope
	if tok.HasScope("sessions:write") {
		t.Errorf("did not expect sessions:write to match")
	}
	if tok.HasScope("admin") {
		t.Errorf("did not expect admin to match")
	}

	// Admin token
	adminTok := &ScopedTokenRecord{
		Scopes: []string{"admin"},
	}
	if !adminTok.HasScope("anything:really") {
		t.Errorf("expected admin token to match any scope")
	}

	// Revoked token
	revokedTok := &ScopedTokenRecord{
		Scopes:  []string{"*"},
		Revoked: true,
	}
	if revokedTok.HasScope("anything") {
		t.Errorf("revoked token must not match any scope")
	}
}
