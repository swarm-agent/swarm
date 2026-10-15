package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/publish"
)

// Purpose: the real landing queue must share restart's admission fence per
// entry, not per FIFO batch. This boundary test holds checks in flight, starts
// drain, and proves subsequent durable work is untouched and resumes afterwards.
func TestLandingDrainFence(t *testing.T) {
	s, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	q, err := s.landingQueue()
	if err != nil {
		t.Fatal(err)
	}
	account := testPrincipal().AccountScopeID
	for _, id := range []string{"first", "second"} {
		_, _, err = q.EnqueueDelivery(publish.Entry{Account: account, Request: publish.Request{JobID: id, Folder: "folder"}}, false)
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := make(chan struct{})
	finish := make(chan struct{})
	done := make(chan error, 1)
	hooks := publish.QueueHooks{Admit: s.admitLanding, Prepare: func(ctx context.Context, e *publish.Entry) error {
		close(started)
		select {
		case <-finish:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, Land: func(context.Context, publish.Entry) (publish.LandResult, error) {
		return publish.LandResult{Commit: "landed"}, nil
	}}
	go func() { done <- q.Process(ctx, account, "folder", hooks) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	m := s.ExecutionCapacity()
	status, err := m.BeginDrain(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if status.Ready || len(status.Blockers) != 1 {
		t.Fatalf("landing missing: %+v", status)
	}
	claim := "01234567890123456789012345678901"
	if _, err = m.ClaimRestart(status.Generation, claim); err == nil {
		t.Fatal("restart allowed during checks")
	}
	close(finish)
	for !m.DrainStatus().Ready {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		default:
		}
	}
	if _, err = m.ClaimRestart(status.Generation, claim); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("held admission: %v", err)
	}
	pending, err := q.Pending(account)
	if err != nil || len(pending) != 1 || pending[0].State != "queued" || pending[0].Request.JobID != "second" {
		t.Fatalf("pending %+v: %v", pending, err)
	}
	m.ReleaseRestart(status.Generation, claim)
	hooks.Prepare = func(context.Context, *publish.Entry) error { return nil }
	resume, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err = q.Process(resume, account, "folder", hooks); err != nil {
		t.Fatal(err)
	}
	pending, err = q.Pending(account)
	if err != nil || len(pending) != 0 {
		t.Fatalf("not resumed: %+v %v", pending, err)
	}
}
