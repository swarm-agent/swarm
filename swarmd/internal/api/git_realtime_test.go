package api

import (
	"sync"

	"swarm/packages/swarmd/internal/gitwatch"
)

type fakeGitRealtimeBackend struct {
	events chan gitwatch.Event
	mu     sync.Mutex
	closed bool
}

func (b *fakeGitRealtimeBackend) Events() <-chan gitwatch.Event { return b.events }
func (b *fakeGitRealtimeBackend) Diagnostics() gitwatch.Diagnostics {
	return gitwatch.Diagnostics{BackendKind: "fake", WatchedDirs: 3}
}
func (b *fakeGitRealtimeBackend) Close() error {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	return nil
}
