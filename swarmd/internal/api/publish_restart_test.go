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
	call := func() *controlMCPCall {
		r := httptest.NewRequest("POST", "/mcp", nil)
		r = r.WithContext(context.WithValue(r.Context(), productPrincipalRequestContextKey, identity.Principal{Type: "user", UserID: "u", AccountScopeID: "a"}))
		return &controlMCPCall{server: &Server{signals: store.NewSignalStore(db)}, request: r}
	}
	notify := func(context.Context, publish.Entry) error {
		return call().signalPublishDurable(controlMCPJobWorker, "worker", "run", controlMCPJobRefs{}, "job.landed", store.SignalSeverityInfo, "landed", map[string]string{"head": "head"})
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
