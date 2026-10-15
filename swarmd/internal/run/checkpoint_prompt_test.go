package run

import (
	"context"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type checkpointPromptCaptureRunner struct {
	id       string
	requests []provideriface.Request
}

func (r *checkpointPromptCaptureRunner) ID() string { return r.id }

func (r *checkpointPromptCaptureRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	r.requests = append(r.requests, req)
	return provideriface.Response{Text: "checkpoint response"}, nil
}

func (r *checkpointPromptCaptureRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	return r.CreateResponse(ctx, req)
}

type checkpointPromptAdapter struct{ id string }

func (a checkpointPromptAdapter) ID() string { return a.id }
func (a checkpointPromptAdapter) Status(context.Context) (provideriface.Status, error) {
	return provideriface.Status{ID: a.id, Ready: true, DefaultModel: "test-model", DefaultThinking: "off"}, nil
}
