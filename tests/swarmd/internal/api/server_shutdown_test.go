package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
)

type shutdownTestRunner struct {
	runCalls  atomic.Int32
	runTurnFn func(ctx context.Context, sessionID string, options runruntime.RunOptions) (runruntime.RunResult, error)
}

func newShutdownTestRunner() *shutdownTestRunner {
	return &shutdownTestRunner{}
}

func (r *shutdownTestRunner) RunTurn(ctx context.Context, sessionID string, options runruntime.RunOptions) (runruntime.RunResult, error) {
	if r.runTurnFn != nil {
		return r.runTurnFn(ctx, sessionID, options)
	}
	r.runCalls.Add(1)
	return runruntime.RunResult{
		SessionID: sessionID,
	}, nil
}

func TestServerReadyReturnsServiceUnavailableDuringShutdown(t *testing.T) {
	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	server.BeginShutdown()

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestServerRunRejectsNewTurnsDuringShutdown(t *testing.T) {
	runner := newShutdownTestRunner()
	server := NewServer(nil, nil, nil, runner, &sessionruntime.Service{}, nil, nil, nil, nil, nil, nil, nil)
	server.BeginShutdown()

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/session-1/run", bytes.NewBufferString(`{"prompt":"hi"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("run status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if got := runner.runCalls.Load(); got != 0 {
		t.Fatalf("run calls = %d, want 0", got)
	}
}

func TestServerActiveRunCountTracksSynchronousRuns(t *testing.T) {
	runner := newShutdownTestRunner()
	server := NewServer(nil, nil, nil, runner, &sessionruntime.Service{}, nil, nil, nil, nil, nil, nil, nil)

	block := make(chan struct{})
	started := make(chan struct{}, 1)
	runner.runTurnFn = func(ctx context.Context, sessionID string, options runruntime.RunOptions) (runruntime.RunResult, error) {
		runner.runCalls.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		<-block
		return runruntime.RunResult{SessionID: sessionID}, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/session-1/run", bytes.NewBufferString(`{"prompt":"hi"}`))
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.Handler().ServeHTTP(rec, req)
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("sync run did not start")
	}

	if got := server.ActiveRunCount(); got != 1 {
		t.Fatalf("active runs = %d, want 1", got)
	}

	close(block)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("sync run did not complete")
	}

	if got := server.ActiveRunCount(); got != 0 {
		t.Fatalf("active runs after completion = %d, want 0", got)
	}
}

func TestSystemShutdownEndpointMarksServerAndInvokesHandler(t *testing.T) {
	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	reasons := make(chan string, 1)
	server.SetShutdownHandler(func(reason string) {
		reasons <- reason
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/system/shutdown", bytes.NewBufferString(`{"reason":"manual-check"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("shutdown status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	select {
	case got := <-reasons:
		if got != "manual-check" {
			t.Fatalf("shutdown reason = %q, want manual-check", got)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("shutdown handler not called")
	}

	readyReq := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	readyRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(readyRec, readyReq)
	if readyRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready status after shutdown = %d, want %d", readyRec.Code, http.StatusServiceUnavailable)
	}
}

func TestSystemShutdownEndpointMethodNotAllowed(t *testing.T) {
	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/system/shutdown", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("shutdown GET status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}
