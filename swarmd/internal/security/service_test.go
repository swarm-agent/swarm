package security

import (
	"path/filepath"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSecurityServiceScopedTokenLifecycle(t *testing.T) {
	root := t.TempDir()
	store, err := pebblestore.Open(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	authStore := pebblestore.NewClientAuthStore(store)
	svc := NewService(authStore, nil)

	// Create scoped token
	rawToken, record, err := svc.CreateScopedToken("GitHub Actions", []string{"automations:trigger", "sessions:read"}, "acct_main", "user_admin", time.Hour)
	if err != nil {
		t.Fatalf("CreateScopedToken failed: %v", err)
	}
	if rawToken == "" || record.ID == "" {
		t.Fatalf("expected non-empty token and ID, got token=%q id=%q", rawToken, record.ID)
	}
	if record.Name != "GitHub Actions" {
		t.Errorf("expected name %q, got %q", "GitHub Actions", record.Name)
	}

	// Validate valid token
	validated, err := svc.ValidateScopedToken(rawToken)
	if err != nil || validated == nil {
		t.Fatalf("ValidateScopedToken failed: err=%v validated=%#v", err, validated)
	}
	if validated.ID != record.ID {
		t.Errorf("expected ID %q, got %q", record.ID, validated.ID)
	}
	if !validated.HasScope("automations:trigger") {
		t.Errorf("expected token to have scope automations:trigger")
	}
	if validated.HasScope("sessions:write") {
		t.Errorf("token should not have scope sessions:write")
	}

	// Validate unknown token returns nil, nil
	unknown, err := svc.ValidateScopedToken("swk_unknown1234567890abcdef")
	if err != nil || unknown != nil {
		t.Errorf("expected nil for unknown token, got val=%#v err=%v", unknown, err)
	}

	// Validate empty token returns nil, nil
	empty, err := svc.ValidateScopedToken("")
	if err != nil || empty != nil {
		t.Errorf("expected nil for empty token, got val=%#v err=%v", empty, err)
	}

	// List tokens
	tokens, err := svc.ListScopedTokens("acct_main")
	if err != nil {
		t.Fatalf("ListScopedTokens failed: %v", err)
	}
	if len(tokens) != 1 || tokens[0].ID != record.ID {
		t.Fatalf("unexpected token list: %#v", tokens)
	}

	// Revoke token
	revoked, err := svc.RevokeScopedToken("acct_main", record.ID)
	if err != nil {
		t.Fatalf("RevokeScopedToken failed: %v", err)
	}
	if !revoked.Revoked {
		t.Errorf("expected revoked=true")
	}

	// Validate revoked token returns error
	_, err = svc.ValidateScopedToken(rawToken)
	if err == nil {
		t.Fatalf("expected error validating revoked token, got nil")
	}

	// Create expired token
	rawExpired, _, err := svc.CreateScopedToken("Expired Token", []string{"admin"}, "acct_main", "user_admin", -time.Second)
	if err != nil {
		t.Fatalf("create expired token: %v", err)
	}
	_, err = svc.ValidateScopedToken(rawExpired)
	if err == nil {
		t.Fatalf("expected error validating expired token, got nil")
	}
}
