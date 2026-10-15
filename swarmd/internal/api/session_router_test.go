package api

import (
	"context"
	"errors"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type sessionRouterRecordingRunner struct {
	id             string
	response       provideriface.Response
	err            error
	createCalls    int
	streamingCalls int
	allowStreaming bool
	requests       []provideriface.Request
	contexts       []context.Context
}

func (r *sessionRouterRecordingRunner) ID() string { return r.id }
func (r *sessionRouterRecordingRunner) CreateResponse(ctx context.Context, request provideriface.Request) (provideriface.Response, error) {
	r.createCalls++
	r.requests = append(r.requests, request)
	r.contexts = append(r.contexts, ctx)
	return r.response, r.err
}
func (r *sessionRouterRecordingRunner) CreateResponseStreaming(ctx context.Context, request provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.streamingCalls++
	if r.allowStreaming {
		r.requests = append(r.requests, request)
		r.contexts = append(r.contexts, ctx)
		return r.response, r.err
	}
	return provideriface.Response{}, errors.New("streaming must not be used by Router")
}
