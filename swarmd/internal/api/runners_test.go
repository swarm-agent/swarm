package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"swarm/packages/swarmd/internal/security"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"sync"
	"testing"
	"time"
)

// Purpose: runner administration must fail closed without an owner principal.
// Server.apiMux is the narrowest layer proving registration and authorization;
// an unregistered route (404) does not satisfy the runner API contract.
func TestRunnerOwnerEndpointRequiresPrincipal(t *testing.T) {
	s := &Server{}
	for _, path := range []string{"/v3/runners", "/v3/runners/join-tokens"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		s.apiMux().ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: got %d, want 401", path, w.Code)
		}
	}
}

// Purpose: the credential store must atomically spend join tokens and reject
// expiry, while the API gate refuses all unrelated methods/routes. This uses
// real temporary Pebble storage, the narrowest persistence boundary involved.
func TestRunnerCredentials(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	auth := pebblestore.NewClientAuthStore(db)
	now := time.Unix(1000, 0)
	join, err := auth.CreateRunnerJoinToken("account", "owner", "runner-one", now)
	if err != nil {
		t.Fatal(err)
	}
	token, rec, err := auth.ConsumeRunnerJoinToken(join, now)
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || rec.Kind != "runner" || len(rec.Scopes) != 2 {
		t.Fatal("invalid credential")
	}
	if _, _, err = auth.ConsumeRunnerJoinToken(join, now); err == nil {
		t.Fatal("join reused")
	}
	expired, err := auth.CreateRunnerJoinToken("account", "owner", "runner-two", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = auth.ConsumeRunnerJoinToken(expired, now.Add(15*time.Minute)); err == nil {
		t.Fatal("expired token accepted")
	}
	for _, path := range []string{"/mcp", "/v3/sessions", "/v3/runners", "/v3/runners/join-tokens", "/v3/runners/claim/extra", "/v1/auth/tokens"} {
		w := httptest.NewRecorder()
		if gateRunnerKey(w, httptest.NewRequest("POST", path, nil), &rec) || w.Code != 403 {
			t.Fatalf("allowed %s", path)
		}
	}
	w := httptest.NewRecorder()
	if !gateRunnerKey(w, httptest.NewRequest("POST", "/v3/runners/claim", nil), &rec) {
		t.Fatal("claim refused")
	}
}

// Purpose: ClaimRunnerLanding and UpdateRunnerLanding own durable exclusive
// leases. Exercise contention, stale reporters, heartbeat, two expiries and
// bounded output against real storage without daemon/provider dependencies.
func TestRunnerLeases(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Unix(1000, 0)
	source, head := runnerSource(t)
	err = db.CreateRunnerLanding("account", pebblestore.RunnerLanding{ID: "landing", SourceClonePath: source, BaseCommit: head, HeadCommit: head, Checks: []string{"check"}, CheckTimeout: 60})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan *pebblestore.RunnerLanding, 2)
	for _, name := range []string{"one", "two"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			l, e := db.ClaimRunnerLanding("account", name, now)
			if e != nil {
				t.Error(e)
			}
			results <- l
		}(name)
	}
	wg.Wait()
	close(results)
	var claimed *pebblestore.RunnerLanding
	count := 0
	for l := range results {
		if l != nil {
			claimed = l
			count++
		}
	}
	if count != 1 {
		t.Fatalf("claims=%d", count)
	}
	if err = db.UpdateRunnerLanding("account", "intruder", claimed.ID, claimed.LeaseID, "heartbeat", "", nil, 0, now); err == nil {
		t.Fatal("foreign heartbeat accepted")
	}
	for i := 0; i < 34; i++ {
		if err = db.UpdateRunnerLanding("account", claimed.Runner, claimed.ID, claimed.LeaseID, "output", strings.Repeat("x", 64<<10), nil, 0, now); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.UpdateRunnerLanding("account", claimed.Runner, claimed.ID, claimed.LeaseID, "heartbeat", "", nil, 0, now.Add(50*time.Second)); err != nil {
		t.Fatal(err)
	}
	if l, e := db.ClaimRunnerLanding("account", "third", now.Add(61*time.Second)); e != nil || l != nil {
		t.Fatal("heartbeat did not extend lease", e)
	}
	second, err := db.ClaimRunnerLanding("account", "third", now.Add(111*time.Second))
	if err != nil || second == nil {
		t.Fatal("not requeued", err)
	}
	if len(second.Output) != 2<<20 || len(second.Attempts) != 2 {
		t.Fatal("output cap or attempt history incorrect")
	}
	if err = db.UpdateRunnerLanding("account", claimed.Runner, claimed.ID, claimed.LeaseID, "result", "", []int{0}, 1, now.Add(112*time.Second)); err == nil {
		t.Fatal("stale result accepted")
	}
	if l, e := db.ClaimRunnerLanding("account", "fourth", now.Add(172*time.Second)); e != nil || l != nil {
		t.Fatal("third attempt admitted", e)
	}
}

// Purpose: withAuth must deny a real runner bearer before exempt-route and
// owner fallback handling. Assert the downstream handler is never invoked;
// checking only the route helper would not establish middleware enforcement.
func TestRunnerAuthenticationBoundary(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	auth := pebblestore.NewClientAuthStore(db)
	join, err := auth.CreateRunnerJoinToken("account", "owner", "runner", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	token, rec, err := auth.ConsumeRunnerJoinToken(join, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{security: security.NewService(auth, nil)}
	called := false
	handler := s.withAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(204) }))
	for _, path := range []string{"/healthz", "/v3/runners/join", "/v3/sessions", "/mcp", "/v3/runners"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != 403 || called {
			t.Fatalf("escaped boundary %s: %d", path, w.Code)
		}
	}
	if _, err = auth.RevokeScopedToken(rec.AccountScopeID, rec.ID); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/v3/runners/claim", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 401 || called {
		t.Fatal("revoked credential escaped boundary")
	}
}
