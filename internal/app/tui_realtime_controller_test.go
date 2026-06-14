package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"swarm-refactor/swarmtui/internal/client"
)

type tuiRealtimeFakeStreamCall struct {
	Index         int
	Subscriptions []client.V3RealtimeSubscription
}

type tuiRealtimeFakeStreamer struct {
	mu      sync.Mutex
	calls   []tuiRealtimeFakeStreamCall
	started chan tuiRealtimeFakeStreamCall
	ctxs    chan context.Context
	handler func(index int, ctx context.Context, subscriptions []client.V3RealtimeSubscription, onFrame func(client.V3RealtimeFrame)) error
}

func newTUIRealtimeFakeStreamer() *tuiRealtimeFakeStreamer {
	return &tuiRealtimeFakeStreamer{
		started: make(chan tuiRealtimeFakeStreamCall, 32),
		ctxs:    make(chan context.Context, 32),
	}
}

func (f *tuiRealtimeFakeStreamer) StreamSessionsV3Realtime(ctx context.Context, subscriptions []client.V3RealtimeSubscription, onFrame func(client.V3RealtimeFrame)) error {
	f.mu.Lock()
	index := len(f.calls) + 1
	call := tuiRealtimeFakeStreamCall{Index: index, Subscriptions: cloneTUIRealtimeSubscriptions(subscriptions)}
	f.calls = append(f.calls, call)
	handler := f.handler
	f.mu.Unlock()
	select {
	case f.started <- call:
	default:
	}
	select {
	case f.ctxs <- ctx:
	default:
	}
	if handler != nil {
		return handler(index, ctx, subscriptions, onFrame)
	}
	<-ctx.Done()
	return nil
}

func (f *tuiRealtimeFakeStreamer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func TestTUIRealtimeControllerReconcileStartsOneStreamForUnchangedState(t *testing.T) {
	fake := newTUIRealtimeFakeStreamer()
	fake.handler = func(index int, ctx context.Context, subscriptions []client.V3RealtimeSubscription, onFrame func(client.V3RealtimeFrame)) error {
		<-ctx.Done()
		return nil
	}
	controller := newTestTUIRealtimeController(fake, make(chan client.V3RealtimeFrame, 4), make(chan tuiRealtimeStatus, 16))
	subs := []client.V3RealtimeSubscription{{SessionID: "b", EndpointCursor: "cursor-1", LastSeq: 2}, {SessionID: "a", EndpointCursor: "cursor-1", LastSeq: 1}}
	if err := controller.Reconcile(subs, "cursor-1"); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	first := waitTUIRealtimeCall(t, fake)
	if got := sessionIDsForRealtimeSubs(first.Subscriptions); fmt.Sprint(got) != "[a b]" {
		t.Fatalf("subscriptions order = %v, want [a b]", got)
	}
	firstCtx := waitTUIRealtimeContext(t, fake)
	if err := controller.Reconcile([]client.V3RealtimeSubscription{{SessionID: "a", EndpointCursor: "cursor-1", LastSeq: 1}, {SessionID: "b", EndpointCursor: "cursor-1", LastSeq: 2}}, "cursor-1"); err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	assertNoTUIRealtimeCall(t, fake, 25*time.Millisecond)

	if err := controller.Reconcile([]client.V3RealtimeSubscription{{SessionID: "a", EndpointCursor: "cursor-2", LastSeq: 3}}, "cursor-2"); err != nil {
		t.Fatalf("changed Reconcile() error = %v", err)
	}
	waitTUIRealtimeCall(t, fake)
	assertContextCanceled(t, firstCtx)
	controller.Stop()
}

func TestTUIRealtimeControllerStopCancelsOnlyActiveGeneration(t *testing.T) {
	fake := newTUIRealtimeFakeStreamer()
	fake.handler = func(index int, ctx context.Context, subscriptions []client.V3RealtimeSubscription, onFrame func(client.V3RealtimeFrame)) error {
		<-ctx.Done()
		return nil
	}
	controller := newTestTUIRealtimeController(fake, make(chan client.V3RealtimeFrame, 4), make(chan tuiRealtimeStatus, 16))
	if err := controller.Reconcile([]client.V3RealtimeSubscription{{SessionID: "a", EndpointCursor: "cursor-1"}}, "cursor-1"); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	waitTUIRealtimeCall(t, fake)
	firstCtx := waitTUIRealtimeContext(t, fake)
	if err := controller.Reconcile([]client.V3RealtimeSubscription{{SessionID: "b", EndpointCursor: "cursor-2"}}, "cursor-2"); err != nil {
		t.Fatalf("changed Reconcile() error = %v", err)
	}
	waitTUIRealtimeCall(t, fake)
	secondCtx := waitTUIRealtimeContext(t, fake)
	assertContextCanceled(t, firstCtx)
	controller.Stop()
	assertContextCanceled(t, secondCtx)
	controller.Stop()
}

func TestTUIRealtimeControllerTerminalFrameStopsWithoutReconnectLoop(t *testing.T) {
	fake := newTUIRealtimeFakeStreamer()
	fake.handler = func(index int, ctx context.Context, subscriptions []client.V3RealtimeSubscription, onFrame func(client.V3RealtimeFrame)) error {
		onFrame(client.V3RealtimeFrame{Kind: tuiRealtimeKindSlowConsumer, SessionID: "a", Error: "slow"})
		<-ctx.Done()
		return errors.New("read v3 realtime stream: should not retry after terminal frame")
	}
	frames := make(chan client.V3RealtimeFrame, 4)
	statuses := make(chan tuiRealtimeStatus, 16)
	controller := newTestTUIRealtimeController(fake, frames, statuses)
	if err := controller.Reconcile([]client.V3RealtimeSubscription{{SessionID: "a", EndpointCursor: "cursor-1"}}, "cursor-1"); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	waitTUIRealtimeCall(t, fake)
	frame := waitTUIRealtimeFrame(t, frames)
	if frame.Kind != tuiRealtimeKindSlowConsumer {
		t.Fatalf("frame kind = %q", frame.Kind)
	}
	status := waitTUIRealtimeStatusKind(t, statuses, tuiRealtimeStatusTerminal)
	if status.Generation == 0 || status.Reason == "" {
		t.Fatalf("terminal status = %#v", status)
	}
	assertNoTUIRealtimeCall(t, fake, 25*time.Millisecond)
	if got := fake.callCount(); got != 1 {
		t.Fatalf("stream calls = %d, want 1", got)
	}
}

func TestTUIRealtimeControllerRetriesTransientErrorsWithinBudget(t *testing.T) {
	fake := newTUIRealtimeFakeStreamer()
	fake.handler = func(index int, ctx context.Context, subscriptions []client.V3RealtimeSubscription, onFrame func(client.V3RealtimeFrame)) error {
		return errors.New("read v3 realtime stream: temporary websocket failure")
	}
	statuses := make(chan tuiRealtimeStatus, 32)
	controller := newTestTUIRealtimeController(fake, make(chan client.V3RealtimeFrame, 4), statuses)
	controller.retryBudget = 2
	if err := controller.Reconcile([]client.V3RealtimeSubscription{{SessionID: "a", EndpointCursor: "cursor-1"}}, "cursor-1"); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	waitForTUIRealtimeCallCount(t, fake, 3)
	status := waitTUIRealtimeStatusKind(t, statuses, tuiRealtimeStatusFailed)
	if status.Attempt != 2 {
		t.Fatalf("failed attempt = %d, want 2", status.Attempt)
	}
	assertNoTUIRealtimeCall(t, fake, 25*time.Millisecond)
}

func TestTUIRealtimeControllerIgnoresStaleGenerationFramesAndStatuses(t *testing.T) {
	releaseFirst := make(chan struct{})
	fake := newTUIRealtimeFakeStreamer()
	fake.handler = func(index int, ctx context.Context, subscriptions []client.V3RealtimeSubscription, onFrame func(client.V3RealtimeFrame)) error {
		if index == 1 {
			<-releaseFirst
			onFrame(client.V3RealtimeFrame{Kind: tuiRealtimeKindEvent, SessionID: "a"})
			return errors.New("read v3 realtime stream: stale should be ignored")
		}
		<-ctx.Done()
		return nil
	}
	frames := make(chan client.V3RealtimeFrame, 8)
	statuses := make(chan tuiRealtimeStatus, 32)
	controller := newTestTUIRealtimeController(fake, frames, statuses)
	if err := controller.Reconcile([]client.V3RealtimeSubscription{{SessionID: "a", EndpointCursor: "cursor-1"}}, "cursor-1"); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	waitTUIRealtimeCall(t, fake)
	if err := controller.Reconcile([]client.V3RealtimeSubscription{{SessionID: "b", EndpointCursor: "cursor-2"}}, "cursor-2"); err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	waitTUIRealtimeCall(t, fake)
	close(releaseFirst)
	assertNoTUIRealtimeFrame(t, frames, 25*time.Millisecond)
	assertNoTUIRealtimeStatusKind(t, statuses, tuiRealtimeStatusRetrying, 25*time.Millisecond)
	controller.Stop()
}

func TestTUIRealtimeControllerNonblockingSendsDoNotDeadlockShutdown(t *testing.T) {
	fake := newTUIRealtimeFakeStreamer()
	fake.handler = func(index int, ctx context.Context, subscriptions []client.V3RealtimeSubscription, onFrame func(client.V3RealtimeFrame)) error {
		for i := 0; i < 16; i++ {
			onFrame(client.V3RealtimeFrame{Kind: tuiRealtimeKindEvent, SessionID: "a"})
		}
		<-ctx.Done()
		return nil
	}
	frames := make(chan client.V3RealtimeFrame)
	statuses := make(chan tuiRealtimeStatus)
	controller := newTestTUIRealtimeController(fake, frames, statuses)
	if err := controller.Reconcile([]client.V3RealtimeSubscription{{SessionID: "a", EndpointCursor: "cursor-1"}}, "cursor-1"); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	waitTUIRealtimeCall(t, fake)
	done := make(chan struct{})
	go func() {
		controller.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("Stop deadlocked with unbuffered frame/status channels")
	}
}

func newTestTUIRealtimeController(fake *tuiRealtimeFakeStreamer, frames chan client.V3RealtimeFrame, statuses chan tuiRealtimeStatus) *tuiRealtimeController {
	controller := newTUIRealtimeController(fake, frames, statuses)
	controller.retryBase = time.Millisecond
	controller.retryMax = time.Millisecond
	return controller
}

func waitTUIRealtimeCall(t *testing.T, fake *tuiRealtimeFakeStreamer) tuiRealtimeFakeStreamCall {
	t.Helper()
	select {
	case call := <-fake.started:
		return call
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for realtime stream call")
		return tuiRealtimeFakeStreamCall{}
	}
}

func waitForTUIRealtimeCallCount(t *testing.T, fake *tuiRealtimeFakeStreamer, want int) {
	t.Helper()
	deadline := time.After(time.Second)
	for fake.callCount() < want {
		select {
		case <-fake.started:
		case <-deadline:
			t.Fatalf("stream call count = %d, want %d", fake.callCount(), want)
		}
	}
}

func assertNoTUIRealtimeCall(t *testing.T, fake *tuiRealtimeFakeStreamer, wait time.Duration) {
	t.Helper()
	select {
	case call := <-fake.started:
		t.Fatalf("unexpected realtime stream call: %#v", call)
	case <-time.After(wait):
	}
}

func waitTUIRealtimeContext(t *testing.T, fake *tuiRealtimeFakeStreamer) context.Context {
	t.Helper()
	select {
	case ctx := <-fake.ctxs:
		return ctx
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for realtime stream context")
		return context.Background()
	}
}

func assertContextCanceled(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatalf("context was not canceled")
	}
}

func waitTUIRealtimeFrame(t *testing.T, frames <-chan client.V3RealtimeFrame) client.V3RealtimeFrame {
	t.Helper()
	select {
	case frame := <-frames:
		return frame
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for realtime frame")
		return client.V3RealtimeFrame{}
	}
}

func assertNoTUIRealtimeFrame(t *testing.T, frames <-chan client.V3RealtimeFrame, wait time.Duration) {
	t.Helper()
	select {
	case frame := <-frames:
		t.Fatalf("unexpected realtime frame: %#v", frame)
	case <-time.After(wait):
	}
}

func waitTUIRealtimeStatusKind(t *testing.T, statuses <-chan tuiRealtimeStatus, kind tuiRealtimeStatusKind) tuiRealtimeStatus {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case status := <-statuses:
			if status.Kind == kind {
				return status
			}
		case <-deadline:
			t.Fatalf("timed out waiting for realtime status %q", kind)
			return tuiRealtimeStatus{}
		}
	}
}

func assertNoTUIRealtimeStatusKind(t *testing.T, statuses <-chan tuiRealtimeStatus, kind tuiRealtimeStatusKind, wait time.Duration) {
	t.Helper()
	deadline := time.After(wait)
	for {
		select {
		case status := <-statuses:
			if status.Kind == kind {
				t.Fatalf("unexpected realtime status %q: %#v", kind, status)
			}
		case <-deadline:
			return
		}
	}
}

func sessionIDsForRealtimeSubs(subs []client.V3RealtimeSubscription) []string {
	ids := make([]string, 0, len(subs))
	for _, sub := range subs {
		ids = append(ids, sub.SessionID)
	}
	return ids
}
