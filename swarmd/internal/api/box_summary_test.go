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

	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: GET /v3/box/summary is the one call a monitor makes per machine to
// decide whether to alert, so it must: require signals:read (a sessions key
// is refused); report "degraded" with the reason when agents run unconfined
// (no active sandbox, as in this test process); count and list workers that
// failed today while never exposing a run's error text; count sessions
// whose agent waits on a person (the real permission service, which keys
// pending approvals by the session owner, so the summary must ask as the
// caller); and report usage
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
	// One run still going today, and one that finished yesterday (not today's).
	if _, err := s.sessions.RecordWorkerRun(actor.AccountScopeID, pebblestore.WorkerRunRecord{WorkerID: worker.ID, UserID: actor.UserID, Status: "running", RequestSource: "direct"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.sessions.RecordWorkerRun(actor.AccountScopeID, pebblestore.WorkerRunRecord{WorkerID: worker.ID, UserID: actor.UserID, Status: "succeeded", RequestSource: "schedule", CreatedAt: time.Now().Add(-48 * time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	s.perm = permission.NewService(pebblestore.NewPermissionStore(store), nil, nil)
	s.perm.(*permission.Service).SetSessionResolver(s.sessions)
	snapshot := pebblestore.SessionSnapshot{ID: "blocked-session", UserID: actor.UserID, AccountScopeID: actor.AccountScopeID, Title: "blocked", Mode: "auto"}
	if _, err := s.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationCreateSession, SessionID: snapshot.ID, UserID: actor.UserID, AccountScopeID: actor.AccountScopeID, Session: &snapshot, IdempotencyKey: snapshot.ID, PayloadHash: snapshot.ID, NowUnixMs: 1000}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.perm.(*permission.Service).CreatePending(permission.CreateInput{SessionID: snapshot.ID, RunID: "r", CallID: "c1", ToolName: "bash", ToolArguments: `{"command":"secret-arg"}`, Requirement: "approval", Mode: "auto"}); err != nil {
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
		if !strings.Contains(reasons, "agent sandbox is off") || !strings.Contains(reasons, "workers failed today") || !strings.Contains(reasons, "agents are waiting on a person") {
			t.Fatalf("reasons = %q", reasons)
		}
		attention := out["attention"].(map[string]any)
		blocked, _ := attention["sessions"].([]any)
		if attention["blocked_sessions"].(float64) != 1 || len(blocked) != 1 || blocked[0].(map[string]any)["session_id"] != "blocked-session" {
			t.Fatalf("attention = %v", attention)
		}
		if strings.Contains(raw, "secret-arg") {
			t.Fatal("summary exposed a tool call's arguments")
		}
		workers := out["workers"].(map[string]any)
		failing, _ := workers["failing"].([]any)
		if workers["today_failed"].(float64) != 1 || len(failing) != 1 || failing[0].(map[string]any)["name"] != "Poster" {
			t.Fatalf("workers = %v", workers)
		}
		if strings.Contains(raw, "private detail") {
			t.Fatal("summary exposed a run's error text")
		}
		runs := out["runs"].(map[string]any)
		if runs["total"].(float64) != 2 || runs["failed"].(float64) != 1 || runs["unfinished"].(float64) != 1 || runs["running_now"].(float64) != 1 || len(runs["failed_runs"].([]any)) != 1 {
			t.Fatalf("runs = %v", runs)
		}
		if failedRun := runs["failed_runs"].([]any)[0].(map[string]any); failedRun["worker_name"] != "Poster" || failedRun["run_id"] == "" {
			t.Fatalf("failed run = %v", failedRun)
		}
		if automations := out["automations"].(map[string]any); automations["schedules"].(float64) != 0 || automations["active"].(float64) != 0 {
			t.Fatalf("automations = %v", automations)
		}
		if machine := out["machine"].(map[string]any); machine["version"] == nil {
			t.Fatalf("machine = %v", machine)
		}
		if _, ok := out["usage"].(map[string]any)["limit_exceeded"]; !ok {
			t.Fatalf("usage = %v", out["usage"])
		}
	}
}

// GET /v3/runs lists every worker's runs newest first, filters by status, and
// like the summary never carries a run's error text.
func TestRunsList(t *testing.T) {
	s, _, sec, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for i, name := range []string{"Poster", "Curator"} {
		worker, err := s.sessions.CreateWorker(context.Background(), actor.AccountScopeID, actor.UserID, pebblestore.CreateWorkerRequest{Name: name, Instructions: "Work"})
		if err != nil {
			t.Fatal(err)
		}
		for j, status := range []string{"succeeded", "failed"} {
			run, err := s.sessions.RecordWorkerRun(actor.AccountScopeID, pebblestore.WorkerRunRecord{WorkerID: worker.ID, UserID: actor.UserID, Status: status, RequestSource: "schedule", Error: "private detail", CreatedAt: int64(1000 + 10*i + j)})
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, run.ID)
		}
	}
	key, _, err := sec.CreateScopedToken("monitor", []string{signalsReadScope}, actor.AccountScopeID, actor.UserID, time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	get := func(query string) (int, map[string]any, string) {
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5555/v3/runs"+query, nil)
		req.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out, rec.Body.String()
	}
	code, out, raw := get("?limit=3")
	if code != http.StatusOK || strings.Contains(raw, "private detail") {
		t.Fatalf("runs = %d %s", code, raw)
	}
	page := out["runs"].([]any)
	if len(page) != 3 || page[0].(map[string]any)["run_id"] != ids[3] || page[2].(map[string]any)["run_id"] != ids[1] || out["next_cursor"] == "" {
		t.Fatalf("first page is not the 3 newest: %s", raw)
	}
	code, out, raw = get("?limit=3&cursor=" + out["next_cursor"].(string))
	if code != http.StatusOK || len(out["runs"].([]any)) != 1 || out["runs"].([]any)[0].(map[string]any)["run_id"] != ids[0] || out["next_cursor"] != "" {
		t.Fatalf("second page = %s", raw)
	}
	if _, out, raw = get("?status=failed"); len(out["runs"].([]any)) != 2 {
		t.Fatalf("failed filter = %s", raw)
	}
	if code, _, _ := get("?status=broken"); code != http.StatusBadRequest {
		t.Fatalf("unknown status = %d", code)
	}
}
