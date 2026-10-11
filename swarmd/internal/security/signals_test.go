package security

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/signals"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: "which key did what" starts with the key lifecycle in the signal
// feed: minting, revoking and deleting a key must each raise one signal naming
// the key (id, name, scopes, issuing key), and presenting a revoked key or a
// key whose issuer was revoked must raise token.denied, at most once per key
// per window so a client retrying a dead key cannot flood the feed. No signal
// may contain the key or its hash. Owner: Service minting, validation and
// revocation over a real token store and signal store.
func TestScopedTokenLifecycleSignals(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	signalStore := pebblestore.NewSignalStore(store)
	svc := NewService(pebblestore.NewClientAuthStore(store), nil)
	svc.SetSignalEmitter(signals.NewEmitter(signalStore))

	rawParent, parent, err := svc.CreateScopedToken("ai", []string{"admin"}, "acct", "user", time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	rawChild, child, err := svc.CreateScopedTokenUnder(&parent, "client", []string{"sessions:read"}, "acct", "user", time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RevokeScopedToken("acct", parent.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := svc.ValidateScopedToken(rawParent); err == nil {
			t.Fatal("revoked key validated")
		}
		if _, err := svc.ValidateScopedToken(rawChild); err == nil {
			t.Fatal("key under a revoked key validated")
		}
	}
	if err := svc.DeleteScopedToken("acct", child.ID); err != nil {
		t.Fatal(err)
	}

	page, err := signalStore.ListAfter(0, 100, pebblestore.SignalFilter{Account: "acct"})
	if err != nil {
		t.Fatal(err)
	}
	count := map[string]int{}
	for _, sig := range page.Signals {
		count[sig.Kind+"/"+sig.Refs["token_id"]]++
		blob := sig.Summary + sig.DedupKey
		for _, v := range sig.Refs {
			blob += v
		}
		for _, v := range sig.Attrs {
			blob += v
		}
		for _, secret := range []string{rawParent, rawChild, parent.TokenHash, child.TokenHash} {
			if secret != "" && strings.Contains(blob, secret) {
				t.Fatalf("signal carries a key or its hash: %+v", sig)
			}
		}
		if sig.Account != "acct" {
			t.Fatalf("signal not scoped to the key's account: %+v", sig)
		}
	}
	want := map[string]int{
		"token.minted/" + parent.ID:  1,
		"token.minted/" + child.ID:   1,
		"token.revoked/" + parent.ID: 1,
		"token.denied/" + parent.ID:  1,
		"token.denied/" + child.ID:   1,
		"token.deleted/" + child.ID:  1,
	}
	for key, n := range want {
		if count[key] != n {
			t.Fatalf("%s = %d, want %d (all: %v)", key, count[key], n, count)
		}
	}
	if len(count) != len(want) {
		t.Fatalf("unexpected signals: %v", count)
	}
	for _, sig := range page.Signals {
		if sig.Kind == "token.denied" && sig.Refs["token_id"] == child.ID && (sig.Refs["parent_token_id"] != parent.ID || sig.Attrs["reason"] != "issuing key no longer valid") {
			t.Fatalf("child denial = %+v", sig)
		}
	}
}
