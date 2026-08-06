package codex

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

func TestLockCachedWebsocketSessionIsCancellationAware(t *testing.T) {
	session := &cachedWebsocketSession{}
	if err := lockCachedWebsocketSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var phases []provideriface.AttemptActivityPhase
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	ctx = provideriface.WithAttemptActivityReporter(ctx, func(phase provideriface.AttemptActivityPhase) {
		mu.Lock()
		phases = append(phases, phase)
		mu.Unlock()
	})
	if err := lockCachedWebsocketSession(ctx, session); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock error = %v", err)
	}
	unlockCachedWebsocketSession(session)
	if err := lockCachedWebsocketSession(context.Background(), session); err != nil {
		t.Fatalf("lock after cancellation = %v", err)
	}
	unlockCachedWebsocketSession(session)
	mu.Lock()
	defer mu.Unlock()
	if len(phases) != 1 || phases[0] != provideriface.AttemptActivityLockWaiting {
		t.Fatalf("phases = %v", phases)
	}
}
