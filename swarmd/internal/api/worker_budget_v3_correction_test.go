package api

import (
	"context"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type v3BudgetReceiptRunner struct {
	calls   int
	started chan struct{}
	finish  chan struct{}
}

func (r *v3BudgetReceiptRunner) ID() string { return "codex" }
func (r *v3BudgetReceiptRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}
func (r *v3BudgetReceiptRunner) CreateResponseStreaming(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.calls++
	if r.started != nil {
		close(r.started)
	}
	if r.finish != nil {
		<-r.finish
	}
	return provideriface.Response{Usage: provideriface.TokenUsage{Source: "codex_api_usage", APIUsageRawPath: "response.usage", InputTokens: 7, TotalTokens: 7}}, nil
}
