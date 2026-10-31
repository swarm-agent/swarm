package security

import (
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: session MCP uses existing internal scoped credentials, with exactly
// one owner/session binding, no ordinary API scope and no restart/idle expiry.
// The real credential store/service is the narrowest authentication authority;
// revocation must invalidate the key rather than minting a silent replacement.
func TestSessionMCPInternalCredentialBinding(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := NewService(pebblestore.NewClientAuthStore(store), nil)
	for _, parts := range [][3]string{{"", "user", "session"}, {"account", "", "session"}, {"account", "user", ""}} {
		if _, err = service.CreateSessionToken(parts[0], parts[1], parts[2]); err == nil {
			t.Fatal("incomplete binding accepted")
		}
	}
	raw, err := service.CreateSessionToken("account", "user", "session")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := service.ValidateScopedToken(raw)
	if err != nil || rec == nil {
		t.Fatalf("credential: %v", err)
	}
	if rec.AccountScopeID != "account" || rec.UserID != "user" || rec.SessionID != "session" || KeyKind(*rec) != KeyKindInternal || !rec.HasScope("session:mcp") || rec.HasScope("sessions:write") || rec.HasScope("admin") {
		t.Fatalf("wrong binding/scopes: %+v", rec)
	}
	if lapse, _ := KeyLapse(*rec); lapse != 0 {
		t.Fatal("standing internal credential expires from disuse")
	}
	if _, err = service.RevokeScopedToken("account", rec.ID); err != nil {
		t.Fatal(err)
	}
	if rec, err = service.ValidateScopedToken(raw); err == nil && rec != nil {
		t.Fatal("revoked session credential accepted")
	}
}
