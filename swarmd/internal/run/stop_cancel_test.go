package run

import (
	"context"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type lateStopProviderRunner struct {
	entered chan struct{}
	resume  chan struct{}
}

func (r *lateStopProviderRunner) ID() string { return "fake-stop-late" }

func (r *lateStopProviderRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}

func (r *lateStopProviderRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	close(r.entered)
	<-r.resume
	if onEvent != nil {
		onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "late assistant text"})
	}
	return provideriface.Response{Text: "late assistant text"}, nil
}
