package run

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type watchdogTestRunner struct {
	mu       sync.Mutex
	attempts int
	run      func(context.Context, int, func(provideriface.StreamEvent)) (provideriface.Response, error)
}

func (r *watchdogTestRunner) ID() string { return "watchdog-test" }

func (r *watchdogTestRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}

func (r *watchdogTestRunner) CreateResponseStreaming(ctx context.Context, _ provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.mu.Lock()
	r.attempts++
	attempt := r.attempts
	r.mu.Unlock()
	return r.run(ctx, attempt, onEvent)
}

func TestRunProviderAttemptRetriesSilentProviderOnce(t *testing.T) {
	runner := &watchdogTestRunner{run: func(ctx context.Context, attempt int, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
		if attempt == 1 {
			<-ctx.Done()
			return provideriface.Response{}, ctx.Err()
		}
		return provideriface.Response{Text: "recovered"}, nil
	}}
	response, err := runProviderAttempt(context.Background(), runner, provideriface.Request{}, 10*time.Millisecond, nil)
	if err != nil || response.Text != "recovered" || runner.attempts != 2 {
		t.Fatalf("response=%+v attempts=%d err=%v", response, runner.attempts, err)
	}
}

func TestRunProviderAttemptActivityExtendsDeadline(t *testing.T) {
	runner := &watchdogTestRunner{run: func(ctx context.Context, _ int, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
		for i := 0; i < 3; i++ {
			time.Sleep(6 * time.Millisecond)
			provideriface.ReportAttemptActivity(ctx, provideriface.AttemptActivityResponseRead)
		}
		return provideriface.Response{Text: "active"}, nil
	}}
	response, err := runProviderAttempt(context.Background(), runner, provideriface.Request{}, 10*time.Millisecond, nil)
	if err != nil || response.Text != "active" || runner.attempts != 1 {
		t.Fatalf("response=%+v attempts=%d err=%v", response, runner.attempts, err)
	}
}

func TestRunProviderAttemptFencesLateOutput(t *testing.T) {
	late := make(chan struct{})
	var delivered []string
	runner := &watchdogTestRunner{run: func(ctx context.Context, attempt int, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
		if attempt == 1 {
			<-ctx.Done()
			if onEvent != nil {
				onEvent(provideriface.StreamEvent{Delta: "late"})
			}
			close(late)
			return provideriface.Response{}, ctx.Err()
		}
		if onEvent != nil {
			onEvent(provideriface.StreamEvent{Delta: "current"})
		}
		return provideriface.Response{Text: "current"}, nil
	}}
	_, err := runProviderAttempt(context.Background(), runner, provideriface.Request{}, 10*time.Millisecond, func(event provideriface.StreamEvent) {
		delivered = append(delivered, event.Delta)
	})
	<-late
	if err != nil || len(delivered) != 1 || delivered[0] != "current" {
		t.Fatalf("delivered=%v err=%v", delivered, err)
	}
}

func TestRunProviderAttemptStopsAfterBoundedRetry(t *testing.T) {
	runner := &watchdogTestRunner{run: func(ctx context.Context, _ int, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
		<-ctx.Done()
		return provideriface.Response{}, ctx.Err()
	}}
	_, err := runProviderAttempt(context.Background(), runner, provideriface.Request{}, 5*time.Millisecond, nil)
	if !errors.Is(err, errProviderAttemptActivityTimeout) || runner.attempts != providerAttemptRetryLimit+1 {
		t.Fatalf("attempts=%d err=%v", runner.attempts, err)
	}
}
