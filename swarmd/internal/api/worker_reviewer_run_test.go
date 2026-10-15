package api

import (
	"context"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type reviewRunProvider struct {
	calls    int
	inFlight func()
}

func (p *reviewRunProvider) ID() string { return "codex" }
func (p *reviewRunProvider) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return p.CreateResponseStreaming(ctx, req, nil)
}
func (p *reviewRunProvider) CreateResponseStreaming(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
	p.calls++
	if p.inFlight != nil {
		p.inFlight()
	}
	return provideriface.Response{StopReason: "end_turn", Text: "VERDICT: OK\nSUMMARY: correct\nFINDINGS: None\nTESTS RUN: fixture"}, nil
}

func (p *reviewRunProvider) ExecutionEpochLifecycle() provideriface.ExecutionEpochLifecycleCapabilities {
	return provideriface.ExecutionEpochLifecycleCapabilities{ContextMode: provideriface.ExecutionEpochContextResponsesChain, TransportReusable: true}
}
