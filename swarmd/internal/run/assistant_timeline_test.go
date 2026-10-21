package run

import (
	"context"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type timelineProviderRunner struct {
	calls  int
	inputs [][]map[string]any
}

func (r *timelineProviderRunner) ID() string { return "fake" }

func (r *timelineProviderRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}

func (r *timelineProviderRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.calls++
	r.inputs = append(r.inputs, cloneProviderInput(req.Input))
	if r.calls == 1 {
		if onEvent != nil {
			onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "preface before tool"})
		}
		return provideriface.Response{
			Text: "preface before tool",
			FunctionCalls: []provideriface.FunctionCall{{
				CallID:    "call_1",
				Name:      "bash",
				Arguments: `{"command":"printf tool ok"}`,
			}},
		}, nil
	}
	if onEvent != nil {
		onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "final after tool"})
	}
	return provideriface.Response{Text: "final after tool"}, nil
}

func cloneProviderInput(input []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(input))
	for _, item := range input {
		clone := make(map[string]any, len(item))
		for key, value := range item {
			clone[key] = value
		}
		out = append(out, clone)
	}
	return out
}

type multiStepTimelineProviderRunner struct {
	calls  int
	inputs [][]map[string]any
}

func (r *multiStepTimelineProviderRunner) ID() string { return "fake-multistep" }

func (r *multiStepTimelineProviderRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}

func (r *multiStepTimelineProviderRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.calls++
	r.inputs = append(r.inputs, cloneProviderInput(req.Input))
	switch r.calls {
	case 1:
		if onEvent != nil {
			onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "preface before first tool"})
		}
		return provideriface.Response{
			Text: "preface before first tool",
			FunctionCalls: []provideriface.FunctionCall{{
				CallID:    "call_1",
				Name:      "bash",
				Arguments: `{"command":"printf tool one"}`,
			}},
		}, nil
	case 2:
		if onEvent != nil {
			onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "bridge before second tool"})
		}
		return provideriface.Response{
			Text: "bridge before second tool",
			FunctionCalls: []provideriface.FunctionCall{{
				CallID:    "call_2",
				Name:      "bash",
				Arguments: `{"command":"printf tool two"}`,
			}},
		}, nil
	default:
		if onEvent != nil {
			onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "final after second tool"})
		}
		return provideriface.Response{Text: "final after second tool"}, nil
	}
}
