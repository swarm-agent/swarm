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
	rawToken, record, err := svc.CreateScopedToken("GitHub Actions", []string{"automations:trigger", "sessions:read"}, "acct_main", "user_admin", time.Hour, "", "")
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
	rawExpired, _, err := svc.CreateScopedToken("Expired Token", []string{"admin"}, "acct_main", "user_admin", -time.Second, "", "")
	if err != nil {
		t.Fatalf("create expired token: %v", err)
	}
	_, err = svc.ValidateScopedToken(rawExpired)
	if err == nil {
		t.Fatalf("expected error validating expired token, got nil")
	}
}

// Requirement: a token minted on behalf of another scoped token never outlives
// it. Its lifetime is capped at the parent's, it validates only while the
// parent is valid (not revoked, expired or deleted), and an expired parent
// cannot mint. Threat: a client app key minted by an AI key keeps reading and
// steering sessions after the owner revokes or deletes that AI key, or after
// it expires. Owners: Service.CreateScopedTokenUnder,
// CreateAgentBoundTokenUnder and ValidateScopedToken over a real temporary
// token store, the narrowest layer that holds the parent link.
func TestScopedTokenNeverOutlivesItsParent(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := NewService(pebblestore.NewClientAuthStore(store), nil)

	_, parent, err := svc.CreateScopedToken("ai", []string{"admin"}, "acct_main", "user_admin", time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	rawChild, child, err := svc.CreateScopedTokenUnder(&parent, "ui", []string{"sessions:read"}, "acct_main", "user_admin", 30*24*time.Hour, "", "")
	if err != nil || child.ParentTokenID != parent.ID || child.ExpiresAt > parent.ExpiresAt {
		t.Fatalf("child = %+v, %v; want parent %s and expiry at most %d", child, err, parent.ID, parent.ExpiresAt)
	}
	if _, never, err := svc.CreateScopedTokenUnder(&parent, "ui", []string{"sessions:read"}, "acct_main", "user_admin", 0, "", ""); err != nil || never.ExpiresAt == 0 || never.ExpiresAt > parent.ExpiresAt {
		t.Fatalf("a never-expiring request under an expiring parent = %+v, %v", never, err)
	}
	rawAgent, agentBound, err := svc.CreateAgentBoundTokenUnder(&parent, "chat", "acct_main", "user_admin", 0, "frontdesk", 0, 0)
	if err != nil || agentBound.ParentTokenID != parent.ID || agentBound.ExpiresAt == 0 {
		t.Fatalf("agent-bound child = %+v, %v", agentBound, err)
	}
	if record, err := svc.ValidateScopedToken(rawChild); err != nil || record == nil {
		t.Fatalf("child refused while its parent is valid: %v", err)
	}

	if _, err := svc.RevokeScopedToken("acct_main", parent.ID); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{rawChild, rawAgent} {
		if record, err := svc.ValidateScopedToken(raw); err == nil || record != nil {
			t.Fatalf("child of a revoked parent validated: %+v", record)
		}
	}

	// A deleted parent ends its children too.
	_, gone, err := svc.CreateScopedToken("ai2", []string{"admin"}, "acct_main", "user_admin", time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	rawOrphan, _, err := svc.CreateScopedTokenUnder(&gone, "ui", []string{"sessions:read"}, "acct_main", "user_admin", time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteScopedToken("acct_main", gone.ID); err != nil {
		t.Fatal(err)
	}
	if record, err := svc.ValidateScopedToken(rawOrphan); err == nil || record != nil {
		t.Fatalf("child of a deleted parent validated: %+v", record)
	}

	// An expired parent cannot mint, and its existing children stop validating.
	_, expired, err := svc.CreateScopedToken("old", []string{"admin"}, "acct_main", "user_admin", time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	rawLate, _, err := svc.CreateScopedTokenUnder(&expired, "ui", []string{"sessions:read"}, "acct_main", "user_admin", time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	expired.ExpiresAt = time.Now().Add(-time.Second).UnixMilli()
	if err := pebblestore.NewClientAuthStore(store).PutScopedToken(expired); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateScopedTokenUnder(&expired, "ui", []string{"sessions:read"}, "acct_main", "user_admin", time.Hour, "", ""); err == nil {
		t.Fatal("an expired parent minted a token")
	}
	if record, err := svc.ValidateScopedToken(rawLate); err == nil || record != nil {
		t.Fatalf("child of an expired parent validated: %+v", record)
	}
}

// Purpose: an AI key may mint a client key that itself mints keys, so a
// grandchild must stop validating when any ancestor is revoked, not only its
// immediate parent (otherwise a key outlives the AI key that granted it), and
// a cyclic parent link must fail closed instead of looping. Owner:
// Service.ValidateScopedToken over a real temporary token store, the narrowest
// layer that holds the parent chain.
func TestScopedTokenRevocationCascadesThroughTheChain(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tokens := pebblestore.NewClientAuthStore(store)
	svc := NewService(tokens, nil)

	_, root, err := svc.CreateScopedToken("ai", []string{"admin"}, "acct_main", "user_admin", time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	_, child, err := svc.CreateScopedTokenUnder(&root, "client", []string{"admin"}, "acct_main", "user_admin", time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	rawGrandchild, grandchild, err := svc.CreateScopedTokenUnder(&child, "ui", []string{"sessions:read"}, "acct_main", "user_admin", time.Hour, "", "")
	if err != nil || grandchild.ParentTokenID != child.ID {
		t.Fatalf("grandchild = %+v, %v", grandchild, err)
	}
	if record, err := svc.ValidateScopedToken(rawGrandchild); err != nil || record == nil {
		t.Fatalf("grandchild refused while its chain is valid: %v", err)
	}
	if _, err := svc.RevokeScopedToken("acct_main", root.ID); err != nil {
		t.Fatal(err)
	}
	if record, err := svc.ValidateScopedToken(rawGrandchild); err == nil || record != nil {
		t.Fatalf("grandchild validated after the root key was revoked: %+v", record)
	}

	// A parent link that points back at a descendant must not loop.
	_, a, err := svc.CreateScopedToken("a", []string{"admin"}, "acct_main", "user_admin", time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	rawB, b, err := svc.CreateScopedTokenUnder(&a, "b", []string{"admin"}, "acct_main", "user_admin", time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	a.ParentTokenID = b.ID
	if err := tokens.PutScopedToken(a); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := svc.ValidateScopedToken(rawB)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a token under a cyclic parent chain validated")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("validating a cyclic parent chain did not terminate")
	}
}
