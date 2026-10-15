package run

import (
	"context"

	"sync"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type designContractRunner struct {
	mu       sync.Mutex
	requests []provideriface.Request
	call     func(context.Context, provideriface.Request) (provideriface.Response, error)
}

func (*designContractRunner) ID() string { return "test" }
func (r *designContractRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	r.mu.Lock()
	r.requests = append(r.requests, req)
	r.mu.Unlock()
	return r.call(ctx, req)
}
func (r *designContractRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
	return r.CreateResponse(ctx, req)
}
