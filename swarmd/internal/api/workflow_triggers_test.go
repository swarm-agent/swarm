package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/jobautomation"
	store "swarm/packages/swarmd/internal/store/pebble"
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
