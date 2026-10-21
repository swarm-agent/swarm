package run

import (
	"context"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type taskBoundaryProviderRunner struct{ calls int }

func (r *taskBoundaryProviderRunner) ID() string { return "boundary-fake" }
func (r *taskBoundaryProviderRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}
func (r *taskBoundaryProviderRunner) CreateResponseStreaming(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.calls++
	return provideriface.Response{
		Text:          "using tool",
		FunctionCalls: []provideriface.FunctionCall{{CallID: "call-1", Name: "bash", Arguments: `{"command":"printf tool ok"}`}},
	}, nil
}

type ordinaryContinuationProviderRunner struct{ calls int }

func (r *ordinaryContinuationProviderRunner) ID() string { return "boundary-fake" }
func (r *ordinaryContinuationProviderRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}
func (r *ordinaryContinuationProviderRunner) CreateResponseStreaming(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.calls++
	if r.calls == 1 {
		return provideriface.Response{Text: "using tool", FunctionCalls: []provideriface.FunctionCall{{CallID: "call-ordinary", Name: "bash", Arguments: `{"command":"printf tool ok"}`}}}, nil
	}
	if r.calls > 2 {
		return provideriface.Response{}, context.DeadlineExceeded
	}
	return provideriface.Response{Text: "done"}, nil
}
