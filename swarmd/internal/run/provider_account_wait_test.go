package run

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/provider/claudecode"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type accountWaitRunner struct {
	calls atomic.Int32
	until time.Time
}

func (r *accountWaitRunner) ID() string { return "claudecode" }
func (r *accountWaitRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}
func (r *accountWaitRunner) CreateResponseStreaming(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
	if r.calls.Add(1) == 1 {
		return provideriface.Response{}, &claudecode.AccountLimitWait{Until: r.until}
	}
	return provideriface.Response{Text: "continued"}, nil
}

// Purpose: runProviderAttempt waits for an explicit all-accounts reset outside
// its short activity watchdog, preserving the same request/run. Cancel must end
// the wait without dispatching again. A deterministic runner checks this narrow
// orchestration timer boundary; this is not live provider qualification.
func TestProviderAccountWaitResumesAndCancels(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r := &accountWaitRunner{until: time.Now().Add(20 * time.Millisecond)}
	var events int
	response, err := runProviderAttempt(ctx, r, provideriface.Request{SessionID: "test-session"}, 5*time.Millisecond, func(event provideriface.StreamEvent) {
		if event.Type == provideriface.StreamEventAccountWaiting {
			events++
		}
	})
	if err != nil || response.Text != "continued" || r.calls.Load() != 2 || events != 1 {
		t.Fatalf("response=%+v err=%v calls=%d events=%d", response, err, r.calls.Load(), events)
	}
	r = &accountWaitRunner{until: time.Now().Add(time.Hour)}
	cancelled, stop := context.WithCancel(ctx)
	_, err = runProviderAttempt(cancelled, r, provideriface.Request{}, time.Second, func(event provideriface.StreamEvent) {
		if event.Type == provideriface.StreamEventAccountWaiting {
			stop()
		}
	})
	if !errors.Is(err, context.Canceled) || r.calls.Load() != 1 {
		t.Fatalf("cancel err=%v calls=%d", err, r.calls.Load())
	}
}
