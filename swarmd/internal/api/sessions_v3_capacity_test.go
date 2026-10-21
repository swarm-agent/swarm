package api

import (
	"context"

	"sync"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

// blockingCapacityTestRunner implements provideriface.Runner and
// provideriface.ExecutionEpochLifecycleRunner. It blocks execution until
// unblocked, allowing deterministic capacity slot assertions.
type blockingCapacityTestRunner struct {
	mu        sync.Mutex
	id        string
	unblockCh chan struct{}
	enteredCh chan struct{}
}

func (r *blockingCapacityTestRunner) ID() string {
	if r.id != "" {
		return r.id
	}
	return "codex"
}

func (r *blockingCapacityTestRunner) ExecutionEpochLifecycle() provideriface.ExecutionEpochLifecycleCapabilities {
	return provideriface.ExecutionEpochLifecycleCapabilities{
		ContextMode:       provideriface.ExecutionEpochContextResponsesChain,
		TransportReusable: true,
	}
}

func (r *blockingCapacityTestRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}

func (r *blockingCapacityTestRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	select {
	case r.enteredCh <- struct{}{}:
	default:
	}
	select {
	case <-r.unblockCh:
		return provideriface.Response{
			ID:    "resp-" + req.Model,
			Model: req.Model,
			Text:  "capacity test done",
		}, nil
	case <-ctx.Done():
		return provideriface.Response{}, ctx.Err()
	}
}

func (r *blockingCapacityTestRunner) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	select {
	case <-r.unblockCh:
	default:
		close(r.unblockCh)
	}
}
