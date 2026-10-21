package api

import (
	"context"

	"strings"
	"sync"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type sessionsV3UndeclaredLifecycleRunner struct {
	id string
}

func (r sessionsV3UndeclaredLifecycleRunner) ID() string { return r.id }
func (r sessionsV3UndeclaredLifecycleRunner) CreateResponse(context.Context, provideriface.Request) (provideriface.Response, error) {
	return provideriface.Response{}, nil
}
func (r sessionsV3UndeclaredLifecycleRunner) CreateResponseStreaming(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
	return provideriface.Response{}, nil
}

type sessionsV3RecordingProviderRunner struct {
	mu               sync.Mutex
	id               string
	mediaDeclaration *provideriface.MediaAdapterDeclaration
	text             string
	deltas           []string
	err              error
	response         provideriface.Response
	responses        []provideriface.Response
	functionCalls    []provideriface.FunctionCall
	handler          func(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error)
	callCount        int
	lastRequest      provideriface.Request
	requests         []provideriface.Request
}

func (r *sessionsV3RecordingProviderRunner) ID() string {
	if strings.TrimSpace(r.id) != "" {
		return strings.TrimSpace(r.id)
	}
	return "test-provider"
}
func (r *sessionsV3RecordingProviderRunner) ExecutionEpochLifecycle() provideriface.ExecutionEpochLifecycleCapabilities {
	return provideriface.ExecutionEpochLifecycleCapabilities{ContextMode: provideriface.ExecutionEpochContextResponsesChain, TransportReusable: true}
}

func (r *sessionsV3RecordingProviderRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}
func (r *sessionsV3RecordingProviderRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.mu.Lock()
	r.callCount++
	callCount := r.callCount
	r.lastRequest = req
	r.requests = append(r.requests, req)
	handler := r.handler
	err := r.err
	response := r.response
	if len(r.responses) >= callCount {
		response = r.responses[callCount-1]
	}
	functionCalls := append([]provideriface.FunctionCall(nil), r.functionCalls...)
	deltas := append([]string(nil), r.deltas...)
	text := r.text
	r.mu.Unlock()
	if handler != nil {
		return handler(ctx, req, onEvent)
	}
	if err != nil {
		return provideriface.Response{}, err
	}
	if response.Text == "" {
		response.Text = text
	}
	if len(functionCalls) > 0 {
		response.FunctionCalls = functionCalls
	}
	if response.StopReason == "" && strings.TrimSpace(response.Text) != "" && len(response.FunctionCalls) == 0 && !response.RestartTurn {
		response.StopReason = "stop"
	}
	if onEvent != nil {
		if len(deltas) == 0 {
			deltas = []string{response.Text}
		}
		for _, delta := range deltas {
			onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: delta})
		}
	}
	return response, nil
}
