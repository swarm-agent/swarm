package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/publish"
	sessionruntime "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: production signalPublishDurable must not append a second feed event
// when Queue.Process loses acknowledgement after acceptance. Reopening Pebble
// proves the receipt and pending effect survive independently of memory.
func TestPublishLandSignalAcceptedRestart(t *testing.T) {
	path := t.TempDir()
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := store.NewSessionStore(db).CreateSessionForAccount(store.SessionSnapshot{ID: "publish-session"}, "u", "a"); err != nil {
		t.Fatal(err)
	}
	call := func() *controlMCPCall {
		r := httptest.NewRequest("POST", "/mcp", nil)
		r = r.WithContext(context.WithValue(r.Context(), productPrincipalRequestContextKey, identity.Principal{Type: "user", UserID: "u", AccountScopeID: "a"}))
		return &controlMCPCall{server: &Server{signals: store.NewSignalStore(db), sessions: sessionruntime.NewService(store.NewSessionStore(db), nil)}, request: r}
	}
	notify := func(context.Context, publish.Entry) error {
		return call().signalPublishDurable(controlMCPJobWorker, "worker", "run", controlMCPJobRefs{sessionID: "publish-session"}, "job.landed", store.SignalSeverityInfo, "landed", map[string]string{"head": "head"})
	}
	q := publish.NewQueue(db)
	if _, err = q.Enqueue(publish.Entry{Account: "a", Request: publish.Request{JobID: "j", Folder: "f"}}); err != nil {
		t.Fatal(err)
	}
	hooks := publish.QueueHooks{Prepare: func(context.Context, *publish.Entry) error { return nil }, Land: func(context.Context, publish.Entry) (publish.LandResult, error) {
		return publish.LandResult{Commit: "head"}, nil
	}, Finished: func(ctx context.Context, e publish.Entry) error {
		if err := notify(ctx, e); err != nil {
			t.Fatal(err)
		}
		return errors.New("crash before ack")
	}}
	if err = q.Process(context.Background(), "a", "f", hooks); err == nil {
		t.Fatal("expected interrupted acknowledgement")
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	q = publish.NewQueue(db)
	hooks.Finished = notify
	hooks.Prepare = func(context.Context, *publish.Entry) error { t.Fatal("reprepared"); return nil }
	if err = q.Process(context.Background(), "a", "f", hooks); err != nil {
		t.Fatal(err)
	}
	page, err := store.NewSignalStore(db).ListAfter(0, 100, store.SignalFilter{Account: "a"})
	if err != nil || len(page.Signals) != 1 {
		t.Fatalf("duplicate completion: %+v %v", page, err)
	}
	pending, err := q.Pending("a")
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending=%v %v", pending, err)
	}
}

// Purpose: Queue.Process send-back recovery uses controlMCPMessageJob and the
// actual V3 message acceptance route, not a fake dedup map. An accepted message
// followed by lost acknowledgement must replay its durable run/message after
// closing and reopening Pebble, without preparing or scheduling a second run.
func TestPublishLandDispatchAcceptedRestart(t *testing.T) {
	s, db, h := setupWorkerAPITestServerWithCleanup(t, false)
	defer func() { _ = db.Close() }()
	workspaceID := setupWorkerAPIExecution(t, s, db)
	w, r := settledWorkerJobForMCP(t, s, h, workspaceID, "land-restart")
	jobID := controlMCPJobID(controlMCPJobWorker, w.ID, r.ID)
	q := publish.NewQueue(db)
	if _, err := q.Enqueue(publish.Entry{Account: "acct-test", Request: publish.Request{JobID: jobID, Folder: "f"}}); err != nil {
		t.Fatal(err)
	}
	dispatch := func(_ context.Context, e publish.Entry) error {
		_, err := messageJobForMCP(h, e.Request.JobID, "Landing rejected this job: "+e.Reason, e.FollowUpID)
		return err
	}
	hooks := publish.QueueHooks{Prepare: func(context.Context, *publish.Entry) error { return &publish.SendBackError{Reason: "fix checks"} }, SendBack: func(ctx context.Context, e publish.Entry) error {
		if err := dispatch(ctx, e); err != nil {
			t.Fatal(err)
		}
		return errors.New("crash before ack")
	}}
	if err := q.Process(context.Background(), "acct-test", "f", hooks); err == nil {
		t.Fatal("expected interrupted acknowledgement")
	}
	before, err := s.sessions.ListSessionMessages(r.SessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	active, ok, err := s.sessions.GetSessionActiveRunIntent(r.SessionID)
	if err != nil || !ok {
		t.Fatalf("missing acceptance: %v", err)
	}
	path := db.Path()
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s = &Server{sessions: sessionruntime.NewService(store.NewSessionStore(db), nil)}
	h = s.apiMux()
	q = publish.NewQueue(db)
	hooks.SendBack = dispatch
	hooks.Prepare = func(context.Context, *publish.Entry) error { t.Fatal("reprepared outstanding send-back"); return nil }
	if err = q.Process(context.Background(), "acct-test", "f", hooks); err != nil {
		t.Fatal(err)
	}
	after, err := s.sessions.ListSessionMessages(r.SessionID, 0, 100)
	if err != nil || len(after) != len(before) {
		t.Fatalf("duplicate message: %d -> %d %v", len(before), len(after), err)
	}
	replay, ok, err := s.sessions.GetSessionActiveRunIntent(r.SessionID)
	if err != nil || !ok || replay.RunID != active.RunID {
		t.Fatalf("duplicate run: %+v %v", replay, err)
	}
	e, _, err := q.Get("acct-test", jobID)
	if err != nil || e.SendBackPending || e.SendBacks != 1 || e.State != "sent_back" {
		t.Fatalf("transition not recovered: %+v %v", e, err)
	}
}

// Purpose: terminal receipts must distinguish new transitions after explicit
// retries while deduplicating acceptance replay. Queue.Process journals identity;
// signalPublishDurable owns signal and activity publication. Real Pebble reopen
// proves both preserve each empty-head transition while deduplicating retries.
func TestPublishLandTerminalTransitionIdentity(t *testing.T) {
	path := t.TempDir()
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := store.NewSessionStore(db).CreateSessionForAccount(store.SessionSnapshot{ID: "publish-session"}, "u", "a"); err != nil {
		t.Fatal(err)
	}
	q := publish.NewQueue(db)
	entry := publish.Entry{Account: "a", Request: publish.Request{JobID: "j", Folder: "f"}}
	states := []string{"sent_back", "landing_stopped", "sent_back", "landing_stopped", "land_failed", "land_failed"}
	ids := map[string]bool{}
	for i, state := range states {
		if _, err = q.Enqueue(entry); err != nil {
			t.Fatal(err)
		}
		notify := func(_ context.Context, e publish.Entry) error {
			r := httptest.NewRequest("POST", "/mcp", nil)
			r = r.WithContext(context.WithValue(r.Context(), productPrincipalRequestContextKey, identity.Principal{Type: "user", UserID: "u", AccountScopeID: "a"}))
			c := &controlMCPCall{server: &Server{signals: store.NewSignalStore(db), sessions: sessionruntime.NewService(store.NewSessionStore(db), nil)}, request: r}
			return c.signalPublishDurable(controlMCPJobWorker, "worker", "run", controlMCPJobRefs{sessionID: "publish-session"}, "job."+e.State, store.SignalSeverityInfo, "terminal", map[string]string{"notification_id": e.NotificationID})
		}
		hooks := publish.QueueHooks{Prepare: func(context.Context, *publish.Entry) error {
			if state == "land_failed" {
				return errors.New("infrastructure failure")
			}
			return &publish.SendBackError{Reason: "checks failed"}
		}, SendBack: func(context.Context, publish.Entry) error { return nil }, Finished: func(ctx context.Context, e publish.Entry) error {
			if err := notify(ctx, e); err != nil {
				t.Fatal(err)
			}
			return errors.New("lost acknowledgement")
		}}
		if err = q.Process(context.Background(), "a", "f", hooks); err == nil {
			t.Fatal("expected lost acknowledgement")
		}
		saved, _, err := q.Get("a", "j")
		if err != nil || saved.State != state || saved.NotificationID == "" || ids[saved.NotificationID] || saved.TransitionSequence != uint64(i+1) {
			t.Fatalf("invalid transition: %+v %v", saved, err)
		}
		ids[saved.NotificationID] = true
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
		db, err = store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		q = publish.NewQueue(db)
		hooks.Finished = notify
		hooks.Prepare = func(context.Context, *publish.Entry) error { t.Fatal("reprepared pending transition"); return nil }
		if err = q.Process(context.Background(), "a", "f", hooks); err != nil {
			t.Fatal(err)
		}
		page, err := store.NewSignalStore(db).ListAfter(0, 100, store.SignalFilter{Account: "a"})
		if err != nil || len(page.Signals) != i+1 {
			t.Fatalf("transition %d: signals=%d err=%v", i, len(page.Signals), err)
		}
		service := sessionruntime.NewService(store.NewSessionStore(db), nil)
		rows, err := service.ListRealtimeOutboxAfter(0, 100)
		if err != nil {
			t.Fatal(err)
		}
		var receipts []activityEvent
		for _, row := range rows {
			if row.Event.EventType == "session.delivery.updated" {
				event, ok := activityMetadata(row)
				if !ok {
					t.Fatal("activity receipt not projected")
				}
				receipts = append(receipts, event)
			}
		}
		if len(receipts) != i+1 {
			t.Fatalf("transition %d: activity receipts=%d", i, len(receipts))
		}
		for j, receipt := range receipts {
			if receipt.State != "job."+states[j] || receipt.SessionID != "publish-session" || receipt.JobID != "worker/worker/run" {
				t.Fatalf("activity receipt %d: %+v", j, receipt)
			}
		}
		for j, sig := range page.Signals {
			if sig.Kind != "job."+states[j] {
				t.Fatalf("signal %d: %+v", j, sig)
			}
		}
	}
}
