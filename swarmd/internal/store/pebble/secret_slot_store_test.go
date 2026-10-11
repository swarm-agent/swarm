package pebblestore

import (
	"path/filepath"
	"testing"
	"time"
)

// Purpose: secret slots are the owner-controlled half of Phase 2. These tests
// pin the rules the gateway and API depend on: values are sealed and only the
// owner's account can read them back; slot names and allowed hosts are
// validated (no wildcards, IPs or ports); a grant is scoped to one exact
// workspace and expires; and a sandbox sees only live grants for its own
// workspace. Unit level over a real pebble store, since the boundary is the
// store contract.
func newTestStores(t *testing.T) (*Store, *AuthStore, *SecretSlotStore) {
	t.Helper()
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "main.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	secretStore, err := Open(filepath.Join(dir, "secrets.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close(); _ = secretStore.Close() })
	return store, NewAuthStoreWithSecretStore(store, secretStore), NewSecretSlotStore(store)
}

func TestSecretValueSealedAndAccountScoped(t *testing.T) {
	_, auth, _ := newTestStores(t)
	if err := auth.PutSecretValueForAccount("acct-a", "STRIPE_KEY", []byte("sk_live_REAL")); err != nil {
		t.Fatal(err)
	}
	value, ok, err := auth.GetSecretValueForAccount("acct-a", "STRIPE_KEY")
	if err != nil || !ok || string(value) != "sk_live_REAL" {
		t.Fatalf("owner cannot read its value: %q %v %v", value, ok, err)
	}
	// A different account has no such slot.
	if _, ok, _ := auth.GetSecretValueForAccount("acct-b", "STRIPE_KEY"); ok {
		t.Fatal("another account read the value")
	}
	// Stored ciphertext must not contain the plaintext.
	raw, ok, err := auth.secretStore.GetBytes(authSecretValueKey("acct-a", "STRIPE_KEY"))
	if err != nil || !ok {
		t.Fatal("value not stored")
	}
	if containsBytes(raw, []byte("sk_live_REAL")) {
		t.Fatalf("value stored in the clear: %s", raw)
	}
}

func containsBytes(haystack, needle []byte) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && (indexBytes(haystack, needle) >= 0)
}

func indexBytes(h, n []byte) int {
outer:
	for i := 0; i+len(n) <= len(h); i++ {
		for j := range n {
			if h[i+j] != n[j] {
				continue outer
			}
		}
		return i
	}
	return -1
}

func TestSecretSlotValidation(t *testing.T) {
	_, _, slots := newTestStores(t)
	for _, name := range []string{"bad-name", "lower", "1LEADING", "A"} {
		if _, err := slots.UpsertSlot("a", name, "", []string{"x.com"}); err == nil {
			t.Fatalf("name %q accepted", name)
		}
	}
	for _, hosts := range [][]string{
		{"*.example.com"}, {"10.0.0.1"}, {"example.com:443"}, {"not a host"}, {},
	} {
		if _, err := slots.UpsertSlot("a", "GOOD_NAME", "", hosts); err == nil {
			t.Fatalf("hosts %v accepted", hosts)
		}
	}
	slot, err := slots.UpsertSlot("a", "API_KEY", "desc", []string{"API.Example.com", "api.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if len(slot.Hosts) != 1 || slot.Hosts[0] != "api.example.com" {
		t.Fatalf("hosts not normalized/deduped: %v", slot.Hosts)
	}
}

func TestSecretGrantScopedAndExpiring(t *testing.T) {
	_, _, slots := newTestStores(t)
	fixed := time.Unix(1_700_000_000, 0)
	slots.now = func() time.Time { return fixed }
	if _, err := slots.UpsertSlot("acct", "API_KEY", "", []string{"api.example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := slots.Grant("acct", "API_KEY", "/projects/app", time.Hour); err != nil {
		t.Fatal(err)
	}
	// A grant for a missing slot is refused.
	if _, err := slots.Grant("acct", "NOPE", "/projects/app", time.Hour); err == nil {
		t.Fatal("grant for unknown slot accepted")
	}
	// Live grant is visible to its exact workspace, with the value absent.
	active, err := slots.ActiveGrantsForWorkspace("/projects/app")
	if err != nil || len(active) != 1 || active[0].Slot.Name != "API_KEY" || active[0].Account != "acct" {
		t.Fatalf("active grant not found for workspace: %+v %v", active, err)
	}
	// Not visible to a different workspace.
	if other, _ := slots.ActiveGrantsForWorkspace("/projects/other"); len(other) != 0 {
		t.Fatalf("grant leaked to another workspace: %+v", other)
	}
	// After expiry, nothing is live.
	slots.now = func() time.Time { return fixed.Add(2 * time.Hour) }
	if expired, _ := slots.ActiveGrantsForWorkspace("/projects/app"); len(expired) != 0 {
		t.Fatalf("expired grant still live: %+v", expired)
	}
}
