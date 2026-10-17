package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/publish"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: consume/retry must persist a short per-signal backoff rather than
// leaving newly settled jobs idle for 90 seconds. The real durable consumer is
// the narrowest boundary proving the persisted deadline and retry count.
func TestAutoPublishBackoff(t *testing.T) {
	var budget time.Duration
	for attempt := 1; attempt < autoPublishMaxRetries; attempt++ {
		budget += autoPublishRetryDelay(attempt)
	}
	if budget < 29*time.Minute || budget > 31*time.Minute {
		t.Fatalf("retry budget = %v", budget)
	}
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	s.SetPublishConfig("configured")
	db := s.sessions.Store().Underlying()
	feed := store.NewSignalStore(db)
	p := s.NewAutoPublisher(feed, db)
	sig := store.Signal{Seq: 2, Account: s.OutboxAccount()}
	if err := db.PutJSON(autoPublishCursorKey, uint64(2)); err != nil {
		t.Fatal(err)
	}
	pending := []autoPublishPending{{Signal: sig}}
	for i, want := range []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, time.Minute, time.Minute} {
		pending[0].NextAttempt = time.Time{}
		if err := db.PutJSON(autoPublishPendingKey, pending); err != nil {
			t.Fatal(err)
		}
		before := time.Now()
		err := p.consume(context.Background(), func(_ context.Context, sig store.Signal) error { return p.retry(sig, errAutoPublishWait) })
		after := time.Now()
		if !errors.Is(err, errAutoPublishWait) {
			t.Fatalf("retry: %v", err)
		}
		if _, err := db.GetJSON(autoPublishPendingKey, &pending); err != nil {
			t.Fatal(err)
		}
		if len(pending) != 1 || pending[0].Attempts != i+1 {
			t.Fatalf("pending: %+v", pending)
		}
		// A reconstructed publisher must schedule from the durable deadline,
		// not wait for another feed append or a fixed 30-second interval.
		restarted := s.NewAutoPublisher(feed, db)
		delay, delayErr := restarted.nextRetryDelay()
		if delayErr != nil || delay <= 0 || delay > min(want, 30*time.Second) {
			t.Fatalf("restart delay = %v, err = %v", delay, delayErr)
		}
		if want >= 32*time.Second && delay != 30*time.Second {
			t.Fatalf("upper bound delay = %v", delay)
		}
		if pending[0].NextAttempt.Before(before.Add(want)) || pending[0].NextAttempt.After(after.Add(want)) {
			t.Fatalf("attempt %d delay = %v; want %v", i+1, pending[0].NextAttempt.Sub(before), want)
		}
	}
}

// Purpose: enqueueLand must wake the channel consumed by Run immediately after
// durable enqueue, even if later receipt recording fails. A real queue isolates
// this boundary without running Git or providers; rejected enqueues must not wake.
func TestAutoPublishWake(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	c, err := s.pipelineCall(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wake := s.landingWakeChannel()
	other := (&Server{}).landingWakeChannel()
	req := publish.Request{JobID: "invalid-job-id", Folder: t.TempDir(), Branch: "branch", Head: "head"}
	// Invalid job syntax fails after queue admission, before receipt recording.
	for i := 0; i < 2; i++ {
		if _, err := c.enqueueLand(publish.Rule{}, req, false); err == nil {
			t.Fatal("expected receipt failure")
		}
	}
	q, err := s.landingQueue()
	if err != nil {
		t.Fatal(err)
	}
	entry, _, err := q.Get(s.OutboxAccount(), req.JobID)
	if err != nil || entry.State != "queued" {
		t.Fatalf("enqueue not durable: %+v %v", entry, err)
	}
	select {
	case <-wake:
	default:
		t.Fatal("enqueue did not wake landing channel")
	}
	select {
	case <-wake:
		t.Fatal("wake notifications did not coalesce")
	default:
	}
	select {
	case <-other:
		t.Fatal("wake leaked across servers")
	default:
	}
	req.JobID = ""
	if _, err := c.enqueueLand(publish.Rule{}, req, false); err == nil {
		t.Fatal("invalid enqueue succeeded")
	}
	select {
	case <-wake:
		t.Fatal("rejected enqueue woke landing")
	default:
	}
}
