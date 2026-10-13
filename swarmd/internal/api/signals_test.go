package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: /v3/signals is the feed every monitor, forwarder and outside
// reporter (a host security monitor such as Falco, a script) uses, so it must
// hold these rules: reading needs signals:read and writing signals:write (the
// owner may do both); a reader never sees another account's signals; an
// outside reporter may only use kinds under "external." so it cannot imitate
// Swarm's own signals (agent.blocked, token.denied) and cannot forge which key
// reported it; a refused report writes nothing. Owner: handleSignals over the
// real Handler with the real scoped-token authentication, the narrowest layer
// where scopes, principal and store meet.
func TestSignalsAPI(t *testing.T) {
	s, attachToken, sec, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "signals"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	signals := pebblestore.NewSignalStore(store)
	s.SetSignalStore(signals)
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	mint := func(scopes ...string) (string, string) {
		t.Helper()
		token, record, err := sec.CreateScopedToken("t", scopes, actor.AccountScopeID, actor.UserID, time.Hour, "", "")
		if err != nil {
			t.Fatal(err)
		}
		return token, record.ID
	}
	call := func(token, method, path, body string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(method, "http://127.0.0.1:5555"+path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	latest := func() uint64 {
		t.Helper()
		page, err := signals.ListAfter(0, 1, pebblestore.SignalFilter{})
		if err != nil {
			t.Fatal(err)
		}
		return page.LatestSeq
	}

	// The owner reports an outside event and reads it back.
	falco := `{"kind":"external.falco","severity":"critical","source":"falco","summary":"Terminal shell in container","refs":{"rule":"Terminal shell in container"}}`
	if code, out := call(attachToken, http.MethodPost, "/v3/signals", falco); code != http.StatusOK {
		t.Fatalf("owner ingest = %d %v", code, out)
	}
	// Another account's signal is stored but never shown to this account.
	if _, err := signals.Append(pebblestore.Signal{Kind: "agent.blocked", Account: "acct_other", Summary: "not yours"}); err != nil {
		t.Fatal(err)
	}
	code, out := call(attachToken, http.MethodGet, "/v3/signals?after=0", "")
	list, _ := out["signals"].([]any)
	if code != http.StatusOK || len(list) != 1 {
		t.Fatalf("owner list = %d %v", code, out)
	}
	got := list[0].(map[string]any)
	if got["kind"] != "external.falco" || got["source"] != "external:falco" || got["severity"] != "critical" {
		t.Fatalf("stored signal = %v", got)
	}

	// Kinds outside "external." are refused, and nothing is written.
	before := latest()
	for _, body := range []string{
		`{"kind":"agent.blocked","summary":"spoof"}`,
		`{"kind":"token.denied","summary":"spoof"}`,
		`{"kind":"external.x","summary":"bad source","source":"Bad Source"}`,
		`{"kind":"external.x","summary":"forged","refs":{"reported_by_token":"tok_owner"}}`,
	} {
		if code, out := call(attachToken, http.MethodPost, "/v3/signals", body); code != http.StatusBadRequest {
			t.Fatalf("accepted %s: %d %v", body, code, out)
		}
	}
	if latest() != before {
		t.Fatal("a refused report was written")
	}

	// Scopes: a sessions key can neither read nor report.
	sessionsKey, _ := mint("sessions:read", "sessions:write")
	if code, _ := call(sessionsKey, http.MethodGet, "/v3/signals", ""); code != http.StatusForbidden {
		t.Fatalf("sessions key read signals: %d", code)
	}
	if code, _ := call(sessionsKey, http.MethodPost, "/v3/signals", `{"kind":"external.x","summary":"x"}`); code != http.StatusForbidden {
		t.Fatalf("sessions key reported a signal: %d", code)
	}
	// A reporter key reports, and its id is recorded; it cannot read.
	reporter, reporterID := mint(signalsWriteScope)
	code, out = call(reporter, http.MethodPost, "/v3/signals", `{"kind":"external.script","summary":"backup finished","source":"backup"}`)
	if code != http.StatusOK {
		t.Fatalf("reporter ingest = %d %v", code, out)
	}
	if refs, _ := out["signal"].(map[string]any)["refs"].(map[string]any); refs["reported_by_token"] != reporterID {
		t.Fatalf("reporter id not recorded: %v", out["signal"])
	}
	if code, _ := call(reporter, http.MethodGet, "/v3/signals", ""); code != http.StatusForbidden {
		t.Fatalf("write-only key read signals: %d", code)
	}
	// A reader key reads with filters; it cannot report.
	reader, _ := mint(signalsReadScope)
	code, out = call(reader, http.MethodGet, "/v3/signals?kind=external.falco&min_severity=warning", "")
	if list, _ := out["signals"].([]any); code != http.StatusOK || len(list) != 1 {
		t.Fatalf("filtered read = %d %v", code, out)
	}
	if code, _ := call(reader, http.MethodPost, "/v3/signals", `{"kind":"external.x","summary":"x"}`); code != http.StatusForbidden {
		t.Fatalf("read-only key reported a signal: %d", code)
	}
	for _, path := range []string{"/v3/signals?after=-1", "/v3/signals?limit=0", "/v3/signals?min_severity=loud"} {
		if code, _ := call(reader, http.MethodGet, path, ""); code != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400", path, code)
		}
	}
}

// Purpose: a reader can follow the feed in real time without polling: with
// wait, GET /v3/signals blocks while nothing it may see follows the cursor
// and returns, in the usual shape, as soon as a signal it may see is stored.
// Threats: a wait that returns early on another account's signal (an empty
// page that tells the reader to poll again), a wait that never ends after a
// signal it may see, one that ignores the client going away, or a malformed
// or unbounded wait. Owner: listSignals over the real Handler and
// SignalStore.Changed, the narrowest layer where the broadcast and the
// account filter meet.
func TestSignalsAPIWait(t *testing.T) {
	s, attachToken, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "signals"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	signals := pebblestore.NewSignalStore(store)
	s.SetSignalStore(signals)
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	request := func(ctx context.Context, path string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5555"+path, nil).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer "+attachToken)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	for _, bad := range []string{"-1", "soon"} {
		if code, out := request(context.Background(), "/v3/signals?wait="+bad); code != http.StatusBadRequest {
			t.Fatalf("wait=%s = %d %v", bad, code, out)
		}
	}
	first, err := signals.Append(pebblestore.Signal{Kind: "agent.blocked", Account: actor.AccountScopeID, Summary: "first"})
	if err != nil {
		t.Fatal(err)
	}
	cursor := strconv.FormatUint(first.Seq, 10)

	// Without wait, or with something to return, the read answers at once.
	if code, out := request(context.Background(), "/v3/signals?after=0&wait=30"); code != http.StatusOK || len(out["signals"].([]any)) != 1 {
		t.Fatalf("read with signals waiting = %d %v", code, out)
	}

	type result struct {
		code int
		out  map[string]any
	}
	done := make(chan result, 1)
	go func() {
		code, out := request(context.Background(), "/v3/signals?after="+cursor+"&wait=30")
		done <- result{code, out}
	}()
	// Another account's signal is invisible to this reader: it must not end
	// the wait with an empty page.
	if _, err := signals.Append(pebblestore.Signal{Kind: "agent.blocked", Account: "acct_other", Summary: "not yours"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	select {
	case got := <-done:
		t.Fatalf("wait returned before a visible signal: %d %v", got.code, got.out)
	default:
	}
	visible, err := signals.Append(pebblestore.Signal{Kind: "run.failed", Account: actor.AccountScopeID, Summary: "second"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		list, _ := got.out["signals"].([]any)
		if got.code != http.StatusOK || len(list) != 1 || list[0].(map[string]any)["summary"] != "second" || got.out["next_after"] != float64(visible.Seq) || got.out["latest_seq"] != float64(visible.Seq) {
			t.Fatalf("woken read = %d %v", got.code, got.out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("waiting read did not return after a signal was stored")
	}

	// The wait ends when the client goes away.
	ctx, cancel := context.WithCancel(context.Background())
	gone := make(chan struct{})
	go func() {
		request(ctx, "/v3/signals?after="+strconv.FormatUint(visible.Seq, 10)+"&wait=30")
		close(gone)
	}()
	cancel()
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("waiting read ignored request cancellation")
	}

	// A wait that sees nothing ends with the usual, empty page.
	started := time.Now()
	code, out := request(context.Background(), "/v3/signals?after="+strconv.FormatUint(visible.Seq, 10)+"&wait=1")
	if list, _ := out["signals"].([]any); code != http.StatusOK || out["ok"] != true || len(list) != 0 || out["next_after"] != float64(visible.Seq) || time.Since(started) < 900*time.Millisecond {
		t.Fatalf("expired wait = %d %v after %v", code, out, time.Since(started))
	}
}
