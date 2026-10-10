package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
