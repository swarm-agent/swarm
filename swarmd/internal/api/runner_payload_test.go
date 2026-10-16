package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"swarm/packages/swarmd/internal/sandbox"
	"swarm/packages/swarmd/internal/security"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"sync"
	"testing"
	"time"
)

// Purpose: CreateRunnerLanding must reject requests without a source clone,
// rather than queue work whose payload cannot exist. The real store boundary
// is the narrowest layer proving failed creation leaves nothing claimable.
func TestRunnerPayloadRequiresSource(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var l pebblestore.RunnerLanding
	if err := json.Unmarshal([]byte(`{"id":"missing-source","base_commit":"base","head_commit":"head","checks":["check"],"check_timeout_seconds":60}`), &l); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRunnerLanding("account", l); err == nil {
		t.Fatal("request without source archive accepted")
	}
}

func runnerSource(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	clone := filepath.Join(root, "clone")
	run := func(args ...string) string {
		t.Helper()
		out, err := sandbox.Command(context.Background(), args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", repo)
	if err := os.WriteFile(filepath.Join(repo, "source.txt"), []byte("landing source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("-C", repo, "add", "source.txt")
	run("-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "source")
	run("clone", repo, clone)
	return clone, run("-C", clone, "rev-parse", "HEAD")
}

// Purpose: real Git archive creation, durable claim ownership, HTTP delivery,
// and terminal cleanup must agree. This exercises their integration without
// a live daemon; foreign runners/accounts must never receive source bytes.
func TestRunnerPayloadArchive(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	source, head := runnerSource(t)
	if err := db.CreateRunnerLanding("account", pebblestore.RunnerLanding{ID: "archive", SourceClonePath: source, BaseCommit: head, HeadCommit: head, Checks: []string{"check"}, CheckTimeout: 60}); err != nil {
		t.Fatal(err)
	}
	l, err := db.ClaimRunnerLanding("account", "one", time.Now())
	if err != nil || l == nil {
		t.Fatal(err)
	}
	s := &Server{runnerStore: db}
	for _, tc := range []struct {
		account, runner string
		code            int
	}{{"account", "one", 200}, {"account", "two", 403}, {"other", "one", 403}} {
		r := httptest.NewRequest("GET", "/v3/runners/landings/archive/payload", nil)
		w := httptest.NewRecorder()
		rec := &pebblestore.ScopedTokenRecord{AccountScopeID: tc.account, Name: tc.runner}
		if !gateRunnerKey(w, r, rec) {
			t.Fatal("payload route refused")
		}
		s.handleRunnerWork(w, r, rec)
		if w.Code != tc.code {
			t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
		}
		if tc.code == 200 && (fmt.Sprintf("%x", sha256.Sum256(w.Body.Bytes())) != l.ArchiveSHA256 || int64(w.Body.Len()) != l.ArchiveBytes || l.PayloadFormat != "archive-v1") {
			t.Fatal("archive metadata mismatch")
		}
	}
	f, err := db.OpenRunnerPayload("account", "one", l.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	f.Close()
	if err := db.UpdateRunnerLanding("account", "one", l.ID, l.LeaseID, "result", "", []int{0}, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("payload retained: %v", err)
	}
	w := httptest.NewRecorder()
	s.handleRunnerWork(w, httptest.NewRequest("GET", "/v3/runners/landings/archive/bundle", nil), &pebblestore.ScopedTokenRecord{AccountScopeID: "account", Name: "one"})
	if w.Code != 410 {
		t.Fatal(w.Code)
	}
}

// Purpose: claim must wait for creation, with only one of two waiting runners
// receiving a durable lease. The handler/store boundary proves wakeup and
// exclusivity together; cancellation must release the unsuccessful waiter.
func TestRunnerLongPollClaim(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	source, head := runnerSource(t)
	s := &Server{runnerStore: db}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan *httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	for _, name := range []string{"one", "two"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			w := httptest.NewRecorder()
			s.handleRunnerWork(w, httptest.NewRequest("POST", "/v3/runners/claim", nil).WithContext(ctx), &pebblestore.ScopedTokenRecord{AccountScopeID: "account", Name: name})
			results <- w
		}(name)
	}
	select {
	case <-results:
		t.Fatal("claim returned before work existed")
	case <-time.After(50 * time.Millisecond):
	}
	if err := db.CreateRunnerLanding("account", pebblestore.RunnerLanding{ID: "wait", SourceClonePath: source, BaseCommit: head, HeadCommit: head, Checks: []string{"check"}, CheckTimeout: 60}); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-results:
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"claimed"`) {
			t.Fatal(w.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("claim did not wake")
	}
	cancel()
	select {
	case w := <-results:
		if strings.Contains(w.Body.String(), `"state":"claimed"`) {
			t.Fatal("double claim")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled claim stuck")
	}
}

// Purpose: CreateRunnerLanding must reject secret-bearing env before either
// persistence or archive creation. Account slot metadata is the authority;
// actual secret values are never loaded to validate a runner request.
func TestRunnerSecretEnvironment(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := pebblestore.NewSecretSlotStore(db).UpsertSlot("account", "API_TOKEN", "", []string{"api.example.com"}); err != nil {
		t.Fatal(err)
	}
	source, head := runnerSource(t)
	for i, env := range []map[string]string{{"API_TOKEN": "anything"}, {"SAFE": "prefix swarm-secret://API_TOKEN suffix"}, {"SAFE": "${API_TOKEN}"}} {
		err := db.CreateRunnerLanding("account", pebblestore.RunnerLanding{ID: fmt.Sprintf("secret-%d", i), SourceClonePath: source, BaseCommit: head, HeadCommit: head, Checks: []string{"check"}, CheckTimeout: 60, Env: env})
		if err == nil || !strings.Contains(err.Error(), "secret") {
			t.Fatalf("secret env accepted: %v", err)
		}
	}
	l, err := db.ClaimRunnerLanding("account", "one", time.Now())
	if err != nil || l != nil {
		t.Fatal("rejected request claimable", err)
	}
}

// Purpose: the box summary feeding swarm_status must expose exact runner-state
// and queue counts, without another account's statuses. Exercise the HTTP
// summary rather than only the store aggregation.
func TestRunnerSummaryCounts(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, state := range []string{"idle", "busy", "unhealthy"} {
		if err := db.SetRunnerStatus("account", pebblestore.RunnerStatus{Name: state, State: state, LastSeen: 123}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{runnerStore: db}
	w := httptest.NewRecorder()
	s.handleBoxSummary(w, requestWithTestPrincipalForAccount(httptest.NewRequest("GET", "/v3/box/summary", nil), "owner", "account"))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	runners := body["runners"].(map[string]any)
	if runners["by_state"] == nil || runners["pending"] != float64(0) || runners["claimed"] != float64(0) {
		t.Fatalf("missing counts: %v", runners)
	}
	states := runners["by_state"].(map[string]any)
	for _, state := range []string{"idle", "busy", "unhealthy"} {
		if states[state] != float64(1) {
			t.Fatal(states)
		}
	}
}

// Purpose: successful join, unhealthy report and owner removal must emit the
// monitoring signals and maintain the status inventory. Exercise the handlers
// with real credential/signal stores, not a synthetic signal append alone.
func TestRunnerLifecycleSignals(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	auth := pebblestore.NewClientAuthStore(db)
	signals := pebblestore.NewSignalStore(db)
	s := &Server{runnerStore: db, security: security.NewService(auth, nil), signals: signals}
	token, err := auth.CreateRunnerJoinToken("account", "owner", "runner", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.handleRunners(w, httptest.NewRequest("POST", "/v3/runners/join", strings.NewReader(fmt.Sprintf(`{"token":%q}`, token))))
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	w = httptest.NewRecorder()
	s.handleRunnerWork(w, httptest.NewRequest("POST", "/v3/runners/status", strings.NewReader(`{"state":"unhealthy"}`)), &pebblestore.ScopedTokenRecord{AccountScopeID: "account", Name: "runner"})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = httptest.NewRecorder()
	s.handleRunners(w, requestWithTestPrincipalForAccount(httptest.NewRequest("DELETE", "/v3/runners/runner", nil), "owner", "account"))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	page, err := signals.ListAfter(0, 10, pebblestore.SignalFilter{Account: "account"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Signals) != 3 {
		t.Fatal(page)
	}
	for i, kind := range []string{"runner.joined", "runner.unhealthy", "runner.removed"} {
		if page.Signals[i].Kind != kind || page.Signals[i].Refs["runner"] != "runner" {
			t.Fatal(page)
		}
	}
	summary, err := db.RunnerSummary("account")
	if err != nil || summary.Count != 0 {
		t.Fatal(summary, err)
	}
}
