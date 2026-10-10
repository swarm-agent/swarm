package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: GET /v3/box/summary is the one call a monitor makes per machine to
// decide whether to alert, so it must: require signals:read (a sessions key
// is refused); report "degraded" with the reason when agents run unconfined
// (no active sandbox, as in this test process); count and list workers that
// failed today while never exposing a run's error text; and report usage
// against the daily limit. Owner: handleBoxSummary over the real Handler,
// scoped-token auth and session/worker services.
func TestBoxSummary(t *testing.T) {
	s, attachToken, sec, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "signals"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s.SetSignalStore(pebblestore.NewSignalStore(store))
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	worker, err := s.sessions.CreateWorker(context.Background(), actor.AccountScopeID, actor.UserID, pebblestore.CreateWorkerRequest{Name: "Poster", Instructions: "Post"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.sessions.RecordWorkerRun(actor.AccountScopeID, pebblestore.WorkerRunRecord{WorkerID: worker.ID, UserID: actor.UserID, Status: "failed", RequestSource: "schedule", Error: "provider said: private detail"}); err != nil {
		t.Fatal(err)
	}

	get := func(token string) (int, map[string]any, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5555/v3/box/summary", bytes.NewBuffer(nil))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out, rec.Body.String()
	}

	sessionsKey, _, err := sec.CreateScopedToken("app", []string{"sessions:read"}, actor.AccountScopeID, actor.UserID, time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if code, _, _ := get(sessionsKey); code != http.StatusForbidden {
		t.Fatalf("sessions key read the box summary: %d", code)
	}
	monitorKey, _, err := sec.CreateScopedToken("monitor", []string{signalsReadScope}, actor.AccountScopeID, actor.UserID, time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{attachToken, monitorKey} {
		code, out, raw := get(token)
		if code != http.StatusOK {
			t.Fatalf("summary = %d %s", code, raw)
		}
		if out["status"] != BoxStatusDegraded {
			t.Fatalf("status = %v, want degraded without a sandbox: %s", out["status"], raw)
		}
		reasons := strings.Join(toStrings(out["reasons"]), "|")
		if !strings.Contains(reasons, "agent sandbox is off") || !strings.Contains(reasons, "workers failed today") {
			t.Fatalf("reasons = %q", reasons)
		}
		workers := out["workers"].(map[string]any)
		failing, _ := workers["failing"].([]any)
		if workers["today_failed"].(float64) != 1 || len(failing) != 1 || failing[0].(map[string]any)["name"] != "Poster" {
			t.Fatalf("workers = %v", workers)
		}
		if strings.Contains(raw, "private detail") {
			t.Fatal("summary exposed a run's error text")
		}
		if _, ok := out["usage"].(map[string]any)["limit_exceeded"]; !ok {
			t.Fatalf("usage = %v", out["usage"])
		}
	}
}
