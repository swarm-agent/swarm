package api

import (
	"context"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type testProviderAdapter struct {
	status provideriface.Status
}

func (a testProviderAdapter) ID() string { return a.status.ID }
func (a testProviderAdapter) Status(context.Context) (provideriface.Status, error) {
	return a.status, nil
}

type testProviderRunner struct {
	id string
}

func (r testProviderRunner) ID() string { return r.id }
func (r testProviderRunner) CreateResponse(context.Context, provideriface.Request) (provideriface.Response, error) {
	return provideriface.Response{}, nil
}
func (r testProviderRunner) CreateResponseStreaming(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
	return provideriface.Response{}, nil
}
