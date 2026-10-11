package runtime

import (
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/signals"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the owner tracks where secrets go and when an agent sandbox tries
// to reach somewhere it must not (this server, a private network, cloud
// metadata) through the signal feed. An injected secret must raise
// secret.used naming the secret and website, at most once per window per
// secret and website; a refused destination must raise egress.refused, at most
// once per window per host; untouched pass-through traffic raises nothing; and
// no signal holds the request path. Every use is still logged in full in the
// secret-use log. Owner: useLogger.LogUse, the single point the gateway
// reports through.
func TestGatewayUseSignals(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	signalStore := pebblestore.NewSignalStore(store)
	slots := pebblestore.NewSecretSlotStore(store)
	logger := useLogger{slots: slots, signals: signals.NewEmitter(signalStore)}

	for i := 0; i < 3; i++ {
		logger.LogUse("acct", "STRIPE_KEY", "api.stripe.com", "POST", "/v1/charges/private-path", "injected")
		logger.LogUse("acct", "", "10.0.0.5", "CONNECT", "", "refused:destination is a private address")
		logger.LogUse("acct", "", "example.com", "GET", "/", "passed_no_secret")
	}
	logger.LogUse("acct", "STRIPE_KEY", "files.stripe.com", "GET", "/x", "injected")

	page, err := signalStore.ListAfter(0, 50, pebblestore.SignalFilter{Account: "acct"})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, sig := range page.Signals {
		kinds = append(kinds, sig.Kind+"@"+sig.Refs["host"])
		if strings.Contains(sig.Summary+sig.DedupKey, "private-path") {
			t.Fatalf("signal carries the request path: %+v", sig)
		}
	}
	if got := strings.Join(kinds, ","); got != "secret.used@api.stripe.com,egress.refused@10.0.0.5,secret.used@files.stripe.com" {
		t.Fatalf("signals = %s", got)
	}
	if page.Signals[1].Severity != pebblestore.SignalSeverityWarning || page.Signals[1].Attrs["reason"] != "destination is a private address" {
		t.Fatalf("refusal = %+v", page.Signals[1])
	}
	uses, err := slots.ListUses("acct", 100)
	if err != nil || len(uses) != 10 {
		t.Fatalf("use log has %d entries, want all 10: %v", len(uses), err)
	}
}
