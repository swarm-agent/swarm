package runtime

// Purpose: the secret broker is what turns an owner's grant into a sandbox's
// wiring without leaking the value. These tests pin that a granted project gets
// proxy env, a stand-in env var (never the value), and the CA mounts; that the
// gateway registry receives the real value and the sandbox env does not; that a
// project with no grant gets nothing; and that an unchanged grant set keeps the
// same token (so a running sandbox's baked-in proxy token stays valid) while a
// changed set rotates it. Unit level over a real slot store and registry.

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/egress"
	"swarm/packages/swarmd/internal/sandbox"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type mapValueReader map[string][]byte

func (m mapValueReader) GetSecretValueForAccount(account, name string) ([]byte, bool, error) {
	v, ok := m[account+"/"+name]
	return v, ok, nil
}

func newBrokerForTest(t *testing.T) (*secretBroker, *pebblestore.SecretSlotStore, mapValueReader) {
	t.Helper()
	dir := t.TempDir()
	store, err := pebblestore.Open(filepath.Join(dir, "main.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	slots := pebblestore.NewSecretSlotStore(store)
	values := mapValueReader{}
	b := &secretBroker{
		slots:    slots,
		values:   values,
		registry: egress.NewRegistry(),
		gateway:  "http://172.31.251.1:8080",
		caPath:   filepath.Join(dir, "egress-ca.pem"),
		bundle:   filepath.Join(dir, "ca-bundle.pem"),
		byRoot:   map[string]brokerState{},
	}
	return b, slots, values
}

func TestBrokerWiresGrantedProjectWithoutLeakingValue(t *testing.T) {
	b, slots, values := newBrokerForTest(t)
	if _, err := slots.UpsertSlot("acct", "STRIPE_KEY", "", []string{"api.stripe.com"}); err != nil {
		t.Fatal(err)
	}
	values["acct/STRIPE_KEY"] = []byte("sk_live_REAL")
	if _, err := slots.Grant("acct", "STRIPE_KEY", "/projects/app", time.Hour); err != nil {
		t.Fatal(err)
	}

	setup, err := b.PrepareSandbox("/projects/app")
	if err != nil {
		t.Fatal(err)
	}
	env := strings.Join(setup.Env, "\n")
	if strings.Contains(env, "sk_live_REAL") {
		t.Fatalf("real value leaked into sandbox env:\n%s", env)
	}
	if !strings.Contains(env, "STRIPE_KEY=swarm-secret://STRIPE_KEY") {
		t.Fatalf("stand-in not set:\n%s", env)
	}
	if !strings.Contains(env, "HTTPS_PROXY=http://swarm:") || !strings.Contains(env, "@172.31.251.1:8080") {
		t.Fatalf("proxy env not set:\n%s", env)
	}
	if !strings.Contains(env, "NODE_EXTRA_CA_CERTS="+sandboxCADest) || !strings.Contains(env, "SSL_CERT_FILE="+sandboxBundleDest) {
		t.Fatalf("CA trust env not set:\n%s", env)
	}
	dests := map[string]bool{}
	for _, f := range setup.Files {
		dests[f.Dest] = true
	}
	if !dests[sandboxCADest] || !dests[sandboxBundleDest] {
		t.Fatalf("CA mounts missing: %+v", setup.Files)
	}
	// The gateway side must hold the real value for the minted token.
	var token string
	for _, kv := range setup.Env {
		if strings.HasPrefix(kv, "HTTPS_PROXY=") {
			token = strings.TrimSuffix(strings.TrimPrefix(kv, "HTTPS_PROXY=http://swarm:"), "@172.31.251.1:8080")
		}
	}
	sb, ok := b.registry.ResolveSandbox(token)
	if !ok || len(sb.Grants) != 1 || string(sb.Grants[0].Value) != "sk_live_REAL" {
		t.Fatalf("gateway did not receive the real value: %+v ok=%v", sb, ok)
	}
}

func TestBrokerNoGrantNoWiring(t *testing.T) {
	b, _, _ := newBrokerForTest(t)
	setup, err := b.PrepareSandbox("/projects/none")
	if err != nil {
		t.Fatal(err)
	}
	if len(setup.Env) != 0 || len(setup.Files) != 0 || setup.Fingerprint != "" {
		t.Fatalf("ungranted project got wiring: %+v", setup)
	}
}

func TestBrokerTokenStableUntilGrantsChange(t *testing.T) {
	b, slots, values := newBrokerForTest(t)
	slots.UpsertSlot("acct", "K", "", []string{"api.example.com"})
	values["acct/K"] = []byte("v1")
	slots.Grant("acct", "K", "/p", time.Hour)

	first, err := b.PrepareSandbox("/p")
	if err != nil {
		t.Fatal(err)
	}
	again, err := b.PrepareSandbox("/p")
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint != again.Fingerprint || tokenOf(first) != tokenOf(again) {
		t.Fatal("token/fingerprint changed with an unchanged grant set")
	}
	// Add a second allowed host via a new slot+grant: fingerprint and token change.
	slots.UpsertSlot("acct", "K2", "", []string{"api2.example.com"})
	values["acct/K2"] = []byte("v2")
	slots.Grant("acct", "K2", "/p", time.Hour)
	changed, err := b.PrepareSandbox("/p")
	if err != nil {
		t.Fatal(err)
	}
	if changed.Fingerprint == first.Fingerprint || tokenOf(changed) == tokenOf(first) {
		t.Fatal("token/fingerprint did not change when a grant was added")
	}
	// Release drops the token.
	b.ReleaseSandbox("/p")
	if _, ok := b.registry.ResolveSandbox(tokenOf(changed)); ok {
		t.Fatal("token still valid after release")
	}
}

func tokenOf(setup sandbox.SecretSetup) string {
	for _, kv := range setup.Env {
		if strings.HasPrefix(kv, "HTTPS_PROXY=") {
			return strings.TrimSuffix(strings.TrimPrefix(kv, "HTTPS_PROXY=http://swarm:"), "@172.31.251.1:8080")
		}
	}
	return ""
}
