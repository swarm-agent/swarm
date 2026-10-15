package run

import (
	"context"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

// Embedding the existing hermetic runner preserves its capability declarations.
// The callback runs inside the actual provider attempt, not between simulated runs.
type feedbackBoundaryRunner struct {
	*googleOverflowTestRunner
	step func(context.Context, provideriface.Request) (provideriface.Response, error)
}

func (r *feedbackBoundaryRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.step(ctx, req)
}

func (r *feedbackBoundaryRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
	return r.step(ctx, req)
}
