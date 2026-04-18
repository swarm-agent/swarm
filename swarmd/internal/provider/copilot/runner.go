package copilot

import (
	"context"
	"errors"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type Runner struct {
	manager *Manager
}

func NewRunner(authStore *pebblestore.AuthStore) *Runner {
	return NewRunnerWithManager(NewManager(authStore))
}

func NewRunnerWithManager(manager *Manager) *Runner {
	return &Runner{manager: manager}
}

func (r *Runner) ID() string {
	return "copilot"
}

func (r *Runner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	if r == nil || r.manager == nil {
		return provideriface.Response{}, errors.New("copilot runner manager is not configured")
	}
	return r.manager.RunTurn(ctx, req, nil)
}

func (r *Runner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	if r == nil || r.manager == nil {
		return provideriface.Response{}, errors.New("copilot runner manager is not configured")
	}
	return r.manager.RunTurn(ctx, req, onEvent)
}
