package api

import (
	"context"

	"errors"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type routingLockCheckingRunner struct {
	*sessionRouterRecordingRunner
	server *Server
}

func (r *routingLockCheckingRunner) CreateResponse(ctx context.Context, request provideriface.Request) (provideriface.Response, error) {
	if !r.server.projectTaskCreateMu.TryLock() {
		return provideriface.Response{}, errors.New("provider invoked under broad task creation lock")
	}
	r.server.projectTaskCreateMu.Unlock()
	return r.sessionRouterRecordingRunner.CreateResponse(ctx, request)
}
