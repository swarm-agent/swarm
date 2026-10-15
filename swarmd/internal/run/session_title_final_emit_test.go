package run

import (
	"context"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type staticTitleRunner struct {
	text string
}

func (staticTitleRunner) ID() string { return "codex" }

func (r staticTitleRunner) CreateResponse(context.Context, provideriface.Request) (provideriface.Response, error) {
	return provideriface.Response{Text: r.text}, nil
}

func (r staticTitleRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	_ = onEvent
	return r.CreateResponse(ctx, req)
}
