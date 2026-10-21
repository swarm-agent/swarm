package run

import (
	"context"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type capacityBoundaryRunner struct {
	entered chan struct{}
	release chan struct{}
}

func (*capacityBoundaryRunner) ID() string { return "capacity-fake" }
func (r *capacityBoundaryRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}
func (r *capacityBoundaryRunner) CreateResponseStreaming(ctx context.Context, _ provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
	select {
	case r.entered <- struct{}{}:
	case <-ctx.Done():
		return provideriface.Response{}, ctx.Err()
	}
	select {
	case <-r.release:
		return provideriface.Response{Text: "done"}, nil
	case <-ctx.Done():
		return provideriface.Response{}, ctx.Err()
	}
}
