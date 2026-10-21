package run

import (
	"context"

	"sync"

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
