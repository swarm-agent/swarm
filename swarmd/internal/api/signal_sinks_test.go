package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a signal sink sends this machine's signals to a URL, so creating,
// listing and deleting sinks is for the machine owner only: every scoped
// token, including an admin key an AI holds, is refused and nothing is
// created. The signing secret is returned once at creation and never listed,
// and a new sink starts at the feed's current end. Owner: handleSignalSinks
// over the real Handler and scoped-token authentication.
func TestSignalSinksAPI(t *testing.T) {
	s, attachToken, sec, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "signals"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	feed := pebblestore.NewSignalStore(store)
	sinks := pebblestore.NewSignalSinkStore(store)
	s.SetSignalStore(feed)
	s.SetSignalSinkStore(sinks)
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := feed.Append(pebblestore.Signal{Kind: "daemon.started", Summary: "started"}); err != nil {
		t.Fatal(err)
	}
	call := func(token, method, path, body string) (int, map[string]any, string) {
		t.Helper()
		req := httptest.NewRequest(method, "http://127.0.0.1:5555"+path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out, rec.Body.String()
	}
	create := `{"name":"alerts","url":"https://alerts.example.com/in","kinds":["agent"],"min_severity":"warning"}`

	admin, _, err := sec.CreateScopedToken("ai", []string{"admin"}, actor.AccountScopeID, actor.UserID, time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if code, _, _ := call(admin, method, "/v3/signals/sinks", create); code != http.StatusForbidden {
			t.Fatalf("admin key %s sinks = %d, want 403", method, code)
		}
	}
	if list, _ := sinks.List(actor.AccountScopeID); len(list) != 0 {
		t.Fatal("a refused request created a sink")
	}

	code, out, _ := call(attachToken, http.MethodPost, "/v3/signals/sinks", create)
	secret, _ := out["secret"].(string)
	sink, _ := out["sink"].(map[string]any)
	if code != http.StatusOK || !strings.HasPrefix(secret, "sss_") || sink["secret"] != nil || sink["cursor"].(float64) != 1 {
		t.Fatalf("create = %d %v", code, out)
	}
	if code, _, _ := call(attachToken, http.MethodPost, "/v3/signals/sinks", `{"name":"x","url":"http://alerts.example.com"}`); code != http.StatusBadRequest {
		t.Fatalf("plain http sink accepted: %d", code)
	}
	code, out, raw := call(attachToken, http.MethodGet, "/v3/signals/sinks", "")
	if list, _ := out["sinks"].([]any); code != http.StatusOK || len(list) != 1 || strings.Contains(raw, secret) {
		t.Fatalf("list = %d %s", code, raw)
	}
	id, _ := sink["id"].(string)
	if code, _, _ := call(admin, http.MethodDelete, "/v3/signals/sinks/"+id, ""); code != http.StatusForbidden {
		t.Fatalf("admin key deleted a sink: %d", code)
	}
	if code, _, _ := call(attachToken, http.MethodDelete, "/v3/signals/sinks/"+id, ""); code != http.StatusOK {
		t.Fatalf("owner delete = %d", code)
	}
	if code, _, _ := call(attachToken, http.MethodDelete, "/v3/signals/sinks/"+id, ""); code != http.StatusNotFound {
		t.Fatalf("second delete = %d, want 404", code)
	}
}
