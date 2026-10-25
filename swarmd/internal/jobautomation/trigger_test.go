package jobautomation

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
	"time"
)

// Purpose: Normalize and Matches must reject invalid ingress definitions and
// prevent a trigger workflow from processing unrelated jobs. These pure domain
// boundaries are the narrowest layer for schema and matching regressions.
func TestTriggerValidationAndMatching(t *testing.T) {
	a := testAutomation(t.TempDir())
	a.ID = "workflow"
	a.On = &Trigger{Event: TriggerEvent{Source: "pull", URL: "https://example.com/feed", EverySeconds: 30, ID: "id"}, Start: TriggerStart{Worker: "worker-1", Brief: "revise {{event.note}}"}}
	if err := a.Normalize(); err != nil {
		t.Fatal(err)
	}
	result := &Result{Folder: a.Scope.Folder, Status: StatusSucceeded}
	if a.Matches(result) {
		t.Fatal("matched unrelated job")
	}
	result.TriggerID = a.ID
	if !a.Matches(result) {
		t.Fatal("did not match own job")
	}
	for _, url := range []string{"http://example.com", "https://user:password@example.com", "https://example.com/#fragment", "https:///feed"} {
		a.On.Event.URL = url
		if !IsValidationError(a.Normalize()) {
			t.Fatal("accepted bad URL", url)
		}
	}
	a.On.Event.URL = "https://example.com"
	a.On.Event.EverySeconds = 29
	if !IsValidationError(a.Normalize()) {
		t.Fatal("accepted short interval")
	}
	a.On.Event.EverySeconds = 30
	a.Scope.Worker = "other"
	if !IsValidationError(a.Normalize()) {
		t.Fatal("accepted mismatched worker")
	}
	rendered := RenderEvent("{{event.note}} {{event.object}} {{event.absent}}", map[string]any{"note": "{{event.absent}}", "object": map[string]any{"a": 1}})
	if rendered != "{{event.absent}} {\"a\":1} " {
		t.Fatal(rendered)
	}
}

// Purpose: TriggerRunner and Store.Receive must baseline, durably deduplicate,
// and serialize starts across restart. A TLS httptest feed and actual Pebble
// exercise persistence/network boundaries without providers or benchmark claims.
func TestTriggerPollBaselineQueueRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewStore(db)
	body := `[{"id":"old"}]`
	feed := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
	defer feed.Close()
	a := testAutomation(t.TempDir())
	a.On = &Trigger{Event: TriggerEvent{Source: "pull", URL: feed.URL, EverySeconds: 30, ID: "id"}, Start: TriggerStart{Worker: "worker-1", Brief: "{{event.id}}"}}
	a, _, err = s.Put("account", a)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.now = func() time.Time { return now }
	starts := []string{}
	running := false
	runner := TriggerRunner{Store: s, Account: func() string { return "account" }, Client: feed.Client(), Start: func(_ context.Context, _ Automation, brief, key string) (string, error) {
		starts = append(starts, brief)
		running = true
		return "worker/w/" + brief, nil
	}, Running: func(context.Context, Automation, string) (bool, error) { return running, nil }}
	runner.Tick(context.Background())
	if len(starts) != 0 {
		t.Fatal("baseline fired")
	}
	body = `[{"id":"old"},{"id":"one"},{"id":"two"}]`
	now = now.Add(time.Minute)
	runner.Tick(context.Background())
	runner.Tick(context.Background())
	if len(starts) != 1 || starts[0] != "one" {
		t.Fatal(starts)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runner.Store = NewStore(db)
	runner.Store.now = func() time.Time { return now }
	runner.Tick(context.Background())
	if len(starts) != 1 {
		t.Fatal("replayed across restart")
	}
	running = false
	runner.Tick(context.Background())
	if len(starts) != 2 || starts[1] != "two" {
		t.Fatal(starts)
	}
	running = false
	now = now.Add(time.Minute)
	runner.Tick(context.Background())
	if len(starts) != 2 {
		t.Fatal("replayed feed")
	}
	events, err := runner.Store.TriggerEvents(a)
	if err != nil || len(events) != 2 || events[0].Item != nil {
		t.Fatal(events, err)
	}
	if err := runner.Store.Receive(a, []byte(`[{"id":"three"},{}]`)); err == nil {
		t.Fatal("accepted malformed batch")
	}
	events, _ = runner.Store.TriggerEvents(a)
	if len(events) != 2 {
		t.Fatal("partial batch written")
	}
}

// Purpose: the durable trigger event must preserve advanceQueued's failure reason
// exactly once. Receive and Tick with a rejected dispatch exercise the persistence
// boundary without a provider and assert no job receipt is recorded on failure.
func TestTriggerStartFailureReason(t *testing.T) {
	_, s := openTestStore(t)
	a := testAutomation(t.TempDir())
	a.On = &Trigger{Event: TriggerEvent{Source: "webhook", ID: "id"}, Start: TriggerStart{Worker: "worker-1", Brief: "{{event.id}}"}}
	a, _, err := s.Put(testAccount, a)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Receive(a, []byte(`[{"id":"one"}]`)); err != nil {
		t.Fatal(err)
	}
	runner := TriggerRunner{Store: s, Account: func() string { return testAccount }, Start: func(context.Context, Automation, string, string) (string, error) {
		return "", errors.New("dispatch refused")
	}, Running: func(context.Context, Automation, string) (bool, error) { return false, nil }}
	runner.Tick(context.Background())
	events, err := s.TriggerEvents(a)
	if err != nil || len(events) != 1 {
		t.Fatal(events, err)
	}
	if events[0].Error != "worker start failed: dispatch refused" || events[0].State != "failed" || events[0].JobID != "" {
		t.Fatal(events[0])
	}
}
