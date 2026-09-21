package lifecycle

import (
	"context"
	"sync"
)

// contextMutex provides mutual exclusion that supports cancellation-aware
// acquisition via context.Context to prevent deadlocks and unbounded waiting.
type contextMutex struct {
	ch chan struct{}
}

func newContextMutex() *contextMutex {
	m := &contextMutex{ch: make(chan struct{}, 1)}
	m.ch <- struct{}{}
	return m
}

// Lock acquires the mutex or returns ctx.Err() if the context is cancelled
// while waiting.
func (m *contextMutex) Lock(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-m.ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Unlock releases the mutex.
func (m *contextMutex) Unlock() {
	select {
	case m.ch <- struct{}{}:
	default:
		// already unlocked
	}
}

// TryLock attempts to acquire the mutex without waiting.
func (m *contextMutex) TryLock() bool {
	select {
	case <-m.ch:
		return true
	default:
		return false
	}
}
