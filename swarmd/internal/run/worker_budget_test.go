package run

import (
	"context"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type budgetBoundaryRunner struct{ calls int }

func (r *budgetBoundaryRunner) ID() string { return "fixture" }
func (r *budgetBoundaryRunner) CreateResponse(context.Context, provideriface.Request) (provideriface.Response, error) {
	r.calls++
	return provideriface.Response{}, nil
}
func (r *budgetBoundaryRunner) CreateResponseStreaming(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.calls++
	return provideriface.Response{}, nil
}
