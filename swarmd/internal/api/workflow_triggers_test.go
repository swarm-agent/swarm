package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/jobautomation"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
	"swarm/packages/swarmd/internal/worktree"
	"testing"
	"time"
)

// Purpose: saveTriggerKey must return a new key only once, rotate explicitly,
// and keep signing bytes sealed and absent from secret slots. Real AuthStore
// persistence is the narrowest boundary that proves those storage postconditions.
func TestWorkflowTriggerKeySealedAndRotated(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "secrets"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	auth := store.NewAuthStore(db)
	s := &Server{secretValues: auth}
	a := jobautomation.Automation{Account: "account", ID: "workflow", On: &jobautomation.Trigger{Event: jobautomation.TriggerEvent{Source: "webhook"}}}
	first, err := s.saveTriggerKey(a, false)
	if err != nil || len(first) != 64 {
		t.Fatal(len(first), err)
	}
	key, err := hex.DecodeString(first)
	if err != nil {
		t.Fatal(err)
	}
	read, ok, err := auth.GetWorkflowTriggerKey(a.Account, a.ID)
	if err != nil || !ok || !bytes.Equal(read, key) {
		t.Fatal(ok, err)
	}
	if err := db.IteratePrefix(store.KeyAuthSecretValuePrefix, 10, func(_ string, value []byte) error {
		if bytes.Contains(value, key) || bytes.Contains(value, []byte(first)) {
			t.Fatal("plaintext key persisted")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	again, err := s.saveTriggerKey(a, false)
	if err != nil || again != "" {
		t.Fatal("key returned twice", err)
	}
	rotated, err := s.saveTriggerKey(a, true)
	if err != nil || rotated == first || len(rotated) != 64 {
		t.Fatal("rotation failed", err)
	}
	other, ok, err := auth.GetWorkflowTriggerKey("other", a.ID)
	if err != nil || ok || len(other) != 0 {
		t.Fatal("cross-account key leak")
	}
	if err := auth.DeleteWorkflowTriggerKey(a.Account, a.ID); err != nil {
		t.Fatal(err)
	}
	_, ok, err = auth.GetWorkflowTriggerKey(a.Account, a.ID)
	if err != nil || ok {
		t.Fatal("key survived deletion")
	}
}

// Purpose: the exact webhook ingress must require an HMAC over raw bytes,
// reject invalid signatures without queue mutations, and deduplicate accepted
// IDs. The HTTP helper over real identity and Pebble is the narrowest boundary.
func TestWorkflowTriggerWebhookSignatureDedupe(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "webhook"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	identities := store.NewIdentityStore(db)
	if _, err := identity.NewService(identities).BootstrapFirstIdentity("test-owner"); err != nil {
		t.Fatal(err)
	}
	s := &Server{secretValues: store.NewAuthStore(db), identitySessions: identity.NewSessionService(identities, store.NewIdentitySessionStore(db))}
	repository := jobautomation.NewStore(db)
	s.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: repository}))
	a, _, err := repository.Put(s.OutboxAccount(), jobautomation.Automation{Name: "hook", Enabled: true, Scope: jobautomation.Scope{Folder: t.TempDir()}, Steps: []jobautomation.Step{{Type: "agent", Worker: "coder", Brief: "review"}}, On: &jobautomation.Trigger{Event: jobautomation.TriggerEvent{Source: "webhook", ID: "id"}, Start: jobautomation.TriggerStart{Worker: "worker-1", Brief: "{{event.id}}"}}})
	if err != nil {
		t.Fatal(err)
	}
	keyHex, err := s.saveTriggerKey(a, false)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := hex.DecodeString(keyHex)
	post := func(body string, valid bool) int {
		req := httptest.NewRequest(http.MethodPost, JobAutomationsPath+"/"+a.ID+"/events", strings.NewReader(body))
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(body))
		sig := mac.Sum(nil)
		if !valid {
			sig[0] ^= 1
		}
		req.Header.Set("X-Swarm-Trigger-Signature", "sha256="+hex.EncodeToString(sig))
		response := httptest.NewRecorder()
		if !s.serveWorkflowEvent(response, req) {
			t.Fatal("route not recognized")
		}
		return response.Code
	}
	if got := post(`{"id":"one"}`, false); got != 401 {
		t.Fatal(got)
	}
	events, _ := repository.TriggerEvents(a)
	if len(events) != 0 {
		t.Fatal("bad signature mutated queue")
	}
	if got := post(`{"items":[{"id":"one"},{"id":"one"}]}`, true); got != 202 {
		t.Fatal(got)
	}
	events, _ = repository.TriggerEvents(a)
	if len(events) != 1 || events[0].State != "queued" {
		t.Fatal(events)
	}
	if got := post(`{"id":"two"}`, true); got != 429 {
		t.Fatal("missing rate limit", got)
	}
	rotated, err := s.saveTriggerKey(a, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := post(`{"id":"two"}`, true); got != 401 {
		t.Fatal("old key survived rotation", got)
	}
	key, _ = hex.DecodeString(rotated)
	triggerRate.Lock()
	delete(triggerRate.at, a.Account+"/"+a.ID)
	triggerRate.Unlock()
	if got := post(`{"id":"one"}`, true); got != 202 {
		t.Fatal(got)
	}
	events, _ = repository.TriggerEvents(a)
	if len(events) != 1 {
		t.Fatal("replayed duplicate")
	}
}

// Purpose: workflowPullSecret must enforce both exact host limits and an
// unexpired folder grant before unsealing. Real secret stores are the narrowest
// layer proving that an otherwise valid secret cannot leak to another host.
func TestWorkflowTriggerSecretHostAndGrant(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "secret-grants"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	auth := store.NewAuthStore(db)
	slots := store.NewSecretSlotStore(db)
	if _, err := slots.UpsertSlot("account", "FEED_TOKEN", "", []string{"example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := auth.PutSecretValueForAccount("account", "FEED_TOKEN", []byte("test-only-value")); err != nil {
		t.Fatal(err)
	}
	if _, err := slots.MarkValue("account", "FEED_TOKEN", true); err != nil {
		t.Fatal(err)
	}
	s := &Server{secretSlots: slots, secretValues: auth}
	a := jobautomation.Automation{Account: "account", Scope: jobautomation.Scope{Folder: t.TempDir()}, On: &jobautomation.Trigger{Event: jobautomation.TriggerEvent{Secret: "FEED_TOKEN", URL: "https://example.com/feed"}}}
	if _, err := s.workflowPullSecret(a); err == nil {
		t.Fatal("accepted missing grant")
	}
	if _, err := slots.Grant("account", "FEED_TOKEN", a.Scope.Folder, time.Hour); err != nil {
		t.Fatal(err)
	}
	if value, err := s.workflowPullSecret(a); err != nil || value != "test-only-value" {
		t.Fatal("valid grant rejected", err)
	}
	a.On.Event.URL = "https://other.example.com/feed"
	if _, err := s.workflowPullSecret(a); err == nil {
		t.Fatal("accepted wrong host")
	}
	a.On.Event.URL = "https://example.com/feed"
	a.Account = "other"
	if _, err := s.workflowPullSecret(a); err == nil {
		t.Fatal("accepted cross-account secret")
	}
}

// Purpose: the save-time role guard must refuse reviewer starts with the
// specified ValidationError; the pure guard is the narrowest policy layer.
func TestWorkflowTriggerReviewerRefused(t *testing.T) {
	err := validateTriggerWorkerRole(store.WorkerRoleReviewer)
	if !jobautomation.IsValidationError(err) || err.Error() != "a trigger starts the work; review it with a reviewer step" {
		t.Fatal(err)
	}
	if err := validateTriggerWorkerRole(store.WorkerRoleCoder); err != nil {
		t.Fatal(err)
	}
}

// Purpose: the production trigger Start callback must dispatch independently of
// later step worker availability. Real worker admission is the narrowest proof;
// execution stops at session mutation, without contacting a provider. A missing
// start worker must still refuse admission without creating another run.
func TestWorkflowTriggerDispatchIgnoresStepWorker(t *testing.T) {
	f := newReopenFixture(t)
	s, p := f.server, testPrincipal()
	db := s.sessions.Store().Underlying()
	s.secretValues = store.NewAuthStore(db)
	catalog := store.NewModelCatalogStore(db)
	for _, model := range []string{"action", "plan", "compact", "finder", "coder", "designer", "router"} {
		if err := catalog.SetRecord(store.ModelCatalogRecord{Provider: "codex", Model: model, ThinkingOptions: []string{"high"}}); err != nil {
			t.Fatal(err)
		}
	}
	ws := workspace.NewService(store.NewWorkspaceStore(db))
	s.workspace = ws
	runtime := s.runner.(*runruntime.Service)
	runtime.SetWorkspaceService(ws)
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "main")
	outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "start")
	added, err := ws.AddForPrincipal(p, folder, "trigger", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.sessions.Store().PutProject(p.AccountScopeID, &store.ProjectRecord{ID: "proj-trigger", Name: "Trigger", Workspaces: []store.ProjectWorkspaceRef{{WorkspaceID: added.WorkspaceID, Path: added.WorkspacePath, Role: "primary_code"}}}); err != nil {
		t.Fatal(err)
	}
	workers := s.sessions.Store().WorkerStore()
	worker, _, err := workers.GetWorker(p.AccountScopeID, f.workerID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workers.ActivateWorker(p.AccountScopeID, p.UserID, worker.ID, worker.Revision, nil); err != nil {
		t.Fatal(err)
	}
	execution, err := runruntime.NewWorkerExecutionService(runtime, s.sessions.Store(), &worktree.Service{}, func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
		return sessionruntime.SessionMutationResult{}, errors.New("test stops at admission")
	}, func(identity.Principal, store.V3SessionRunIntent) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	runtime.SetWorkerExecutionService(execution)
	a := jobautomation.Automation{ID: "workflow-trigger", Account: p.AccountScopeID, TriggerOwner: p.UserID, Scope: jobautomation.Scope{Folder: added.WorkspacePath}, Steps: []jobautomation.Step{{Type: "agent", Worker: "missing-step-worker", Brief: "later"}}, On: &jobautomation.Trigger{Event: jobautomation.TriggerEvent{Source: "webhook", ID: "id"}, Start: jobautomation.TriggerStart{Worker: worker.ID, Brief: "start"}}}
	runner := s.workflowTriggerRunner(jobautomation.NewStore(db))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, startErr := runner.Start(ctx, a, "start work", "trigger-dispatch-test")
	runs, _, err := workers.ListWorkerRuns(p.AccountScopeID, worker.ID, 10, "")
	if err != nil || len(runs) != 1 {
		t.Fatalf("dispatch did not admit job: runs=%+v err=%v start=%v", runs, err, startErr)
	}
	if runs[0].RequestSource != "workflow_trigger" || runs[0].WorkspaceID != added.WorkspaceID || runs[0].Input["prompt"] != "start work" {
		t.Fatalf("wrong admission: %+v", runs[0])
	}
	a.On.Start.Worker = "missing-start-worker"
	if _, err := runner.Start(ctx, a, "refused", "trigger-refused-test"); !jobautomation.IsValidationError(err) {
		t.Fatalf("missing start worker: %v", err)
	}
	runs, _, err = workers.ListWorkerRuns(p.AccountScopeID, worker.ID, 10, "")
	if err != nil || len(runs) != 1 {
		t.Fatalf("refusal changed runs: %+v %v", runs, err)
	}
}
