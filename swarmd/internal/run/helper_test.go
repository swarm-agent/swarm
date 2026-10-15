package run

import (
	"context"

	"fmt"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"

	"sync"
)

// helperBarrierRunner adds a concurrency barrier to the handoff recording
// provider. No response can finish until all three requests are in flight.
type helperBarrierRunner struct {
	taskDesignerRecordingRunner
	mu      sync.Mutex
	arrived int
	ready   chan struct{}
}

func (r *helperBarrierRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}
func (r *helperBarrierRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, event func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.mu.Lock()
	r.arrived++
	n := r.arrived
	r.requests = append(r.requests, req)
	if n == 3 {
		close(r.ready)
	}
	r.mu.Unlock()
	select {
	case <-ctx.Done():
		return provideriface.Response{}, ctx.Err()
	case <-r.ready:
	}
	text := fmt.Sprintf("helper-result-%d", n)
	if event != nil {
		event(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: text})
	}
	return provideriface.Response{Text: text}, nil
}
