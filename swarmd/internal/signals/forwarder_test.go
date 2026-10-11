package signals

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type received struct {
	body      deliveryBody
	signature string
	timestamp int64
	sink      string
	raw       []byte
}

// Purpose: the forwarder is how a machine's signals leave it for an alerting
// service, so it must: sign every delivery so the receiver can reject forged
// or replayed batches; send each sink only its account's signals matching its
// kinds and severity; advance a sink's cursor only after a 2xx, so an outage
// delays signals instead of losing them, and back off after a failure; never
// follow a redirect to another URL; and send a heartbeat when idle so a
// receiver can tell a silent machine from a dead one. Owner: Forwarder over
// real signal and sink stores and a real local HTTP receiver.
func TestForwarderDelivery(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	feed := pebblestore.NewSignalStore(store)
	sinks := pebblestore.NewSignalSinkStore(store)

	var mu sync.Mutex
	var got []received
	status := http.StatusOK
	redirectHit := false
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			mu.Lock()
			redirectHit = true
			mu.Unlock()
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body deliveryBody
		_ = json.Unmarshal(raw, &body)
		ts, _ := strconv.ParseInt(r.Header.Get(HeaderTimestamp), 10, 64)
		mu.Lock()
		got = append(got, received{body: body, signature: r.Header.Get(HeaderSignature), timestamp: ts, sink: r.Header.Get(HeaderSink), raw: raw})
		code := status
		mu.Unlock()
		if code == http.StatusFound {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
			return
		}
		w.WriteHeader(code)
	}))
	defer receiver.Close()

	sink, err := sinks.Create("acct", "alerts", receiver.URL+"/ingest", []string{"agent", "worker.run.failed"}, "warning", 60, 0)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	fwd := NewForwarder(feed, sinks, func() string { return "box-1" })
	fwd.now = func() time.Time { return clock }
	emit := NewEmitter(feed)
	emit.Emit(pebblestore.Signal{Kind: "agent.blocked", Severity: "warning", Account: "acct", Summary: "waiting"})
	emit.Emit(pebblestore.Signal{Kind: "agent.blocked", Severity: "warning", Account: "other", Summary: "not this account"})
	emit.Emit(pebblestore.Signal{Kind: "token.minted", Severity: "info", Account: "acct", Summary: "filtered by kind"})
	emit.Emit(pebblestore.Signal{Kind: "worker.run.failed", Severity: "warning", Account: "acct", Summary: "worker failed"})

	fwd.DeliverOnce(context.Background())
	mu.Lock()
	if len(got) != 1 {
		mu.Unlock()
		t.Fatalf("deliveries = %d, want 1", len(got))
	}
	first := got[0]
	mu.Unlock()
	if first.sink != sink.ID || first.body.Machine != "box-1" || first.body.Heartbeat || len(first.body.Signals) != 2 {
		t.Fatalf("delivery = %+v", first.body)
	}
	if first.body.Signals[0].Summary != "waiting" || first.body.Signals[1].Kind != "worker.run.failed" {
		t.Fatalf("signals = %+v", first.body.Signals)
	}
	if !Verify(sink.Secret, first.timestamp, first.raw, first.signature, time.Now(), time.Minute) {
		t.Fatal("delivery signature does not verify")
	}
	if Verify("wrong-secret", first.timestamp, first.raw, first.signature, time.Now(), time.Minute) || Verify(sink.Secret, first.timestamp, append(first.raw, ' '), first.signature, time.Now(), time.Minute) {
		t.Fatal("signature verified with a wrong secret or altered body")
	}
	if Verify(sink.Secret, first.timestamp, first.raw, first.signature, time.Now().Add(time.Hour), time.Minute) {
		t.Fatal("an old delivery verified (replay)")
	}
	stored, _, _ := sinks.Get("acct", sink.ID)
	if stored.Cursor != 4 || stored.LastError != "" {
		t.Fatalf("cursor after success = %+v", stored)
	}

	// A failed delivery keeps the cursor and backs off.
	mu.Lock()
	status = http.StatusInternalServerError
	mu.Unlock()
	emit.Emit(pebblestore.Signal{Kind: "agent.blocked", Severity: "critical", Account: "acct", Summary: "second"})
	fwd.DeliverOnce(context.Background())
	stored, _, _ = sinks.Get("acct", sink.ID)
	if stored.Cursor != 4 || stored.Failures != 1 || stored.LastError == "" {
		t.Fatalf("after failure = %+v", stored)
	}
	mu.Lock()
	attempts := len(got)
	status = http.StatusOK
	mu.Unlock()
	fwd.DeliverOnce(context.Background())
	mu.Lock()
	if len(got) != attempts {
		mu.Unlock()
		t.Fatal("retried before the backoff elapsed")
	}
	mu.Unlock()
	clock = clock.Add(maxDeliveryBackoff)
	fwd.DeliverOnce(context.Background())
	stored, _, _ = sinks.Get("acct", sink.ID)
	if stored.Cursor != 5 || stored.Failures != 0 {
		t.Fatalf("after retry = %+v", stored)
	}

	// A redirect is a failure and is not followed.
	mu.Lock()
	status = http.StatusFound
	mu.Unlock()
	emit.Emit(pebblestore.Signal{Kind: "agent.blocked", Severity: "warning", Account: "acct", Summary: "third"})
	fwd.DeliverOnce(context.Background())
	stored, _, _ = sinks.Get("acct", sink.ID)
	mu.Lock()
	hit := redirectHit
	status = http.StatusOK
	mu.Unlock()
	if hit || stored.Cursor != 5 || stored.Failures != 1 {
		t.Fatalf("redirect followed=%v sink=%+v", hit, stored)
	}

	// Idle and due: a heartbeat with no signals.
	clock = clock.Add(maxDeliveryBackoff)
	fwd.DeliverOnce(context.Background()) // delivers "third"
	clock = clock.Add(2 * time.Minute)
	fwd.DeliverOnce(context.Background())
	mu.Lock()
	last := got[len(got)-1]
	mu.Unlock()
	if !last.body.Heartbeat || len(last.body.Signals) != 0 {
		t.Fatalf("expected a heartbeat, got %+v", last.body)
	}
}

// Purpose: a sink URL is where machine signals go, so only https (or http to
// this machine's loopback) is accepted, never embedded credentials; a sink's
// secret is never listed. Owner: SignalSinkStore.
func TestSignalSinkValidation(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://alerts.example.com/in":        true,
		"http://127.0.0.1:8787/in":             true,
		"http://localhost:8787/in":             true,
		"http://alerts.example.com/in":         false,
		"https://user:pass@alerts.example.com": false,
		"ftp://alerts.example.com":             false,
		"alerts.example.com/in":                false,
	} {
		if _, err := pebblestore.ValidateSignalSinkURL(raw); (err == nil) != ok {
			t.Fatalf("%s accepted=%v, want %v", raw, err == nil, ok)
		}
	}
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sinks := pebblestore.NewSignalSinkStore(store)
	sink, err := sinks.Create("acct", "x", "https://alerts.example.com", nil, "", 0, 7)
	if err != nil || sink.Secret == "" || sink.Cursor != 7 || sink.HeartbeatSeconds != pebblestore.DefaultSinkHeartbeatSeconds {
		t.Fatalf("create = %+v, %v", sink, err)
	}
	if sink.Public().Secret != "" {
		t.Fatal("Public kept the secret")
	}
	if _, err := sinks.Create("acct", "x", "https://alerts.example.com", []string{"Bad Kind"}, "", 0, 0); err == nil {
		t.Fatal("accepted a malformed kind")
	}
	if _, err := sinks.Create("acct", "x", "https://alerts.example.com", nil, "", 5, 0); err == nil {
		t.Fatal("accepted a 5s heartbeat")
	}
}

// Purpose: receivers in any language verify deliveries, so the signature must
// match a fixed vector computed independently (openssl: HMAC-SHA256 of
// "1700000000000.{\"a\":1}" with key "sss_test"); the SDK's
// verifySignalDelivery test pins the same vector. Owner: Sign.
func TestSignMatchesReferenceVector(t *testing.T) {
	if got := Sign("sss_test", 1700000000000, []byte(`{"a":1}`)); got != "v1=48eb242e09848b0b11d3819e976edb5a2055d3a890167a5417a9cc697ae6bc6a" {
		t.Fatalf("Sign = %s", got)
	}
}
