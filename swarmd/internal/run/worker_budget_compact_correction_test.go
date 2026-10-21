package run

import (
	"context"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type compactBudgetReceiptRunner struct {
	calls   int
	started chan struct{}
	finish  chan struct{}
	failure error
}

func (r *compactBudgetReceiptRunner) ID() string { return "fixture" }
func (r *compactBudgetReceiptRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}
func (r *compactBudgetReceiptRunner) CreateResponseStreaming(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.calls++
	if r.started != nil {
		close(r.started)
	}
	if r.finish != nil {
		<-r.finish
	}
	return provideriface.Response{Usage: provideriface.TokenUsage{InputTokens: 7, TotalTokens: 7}}, r.failure
}
