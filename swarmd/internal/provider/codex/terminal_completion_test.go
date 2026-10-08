package codex

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// Purpose: valid Responses completion evidence must survive SSE decoding,
// parseResponse and FromResponse, while output/EOF alone cannot authorize success.
// This adapter-layer test is the narrowest boundary covering the real translation
// consumed by V3 TerminalClassifier, without credentials or a provider connection.
func TestCodexTerminalCompletionTranslation(t *testing.T) {
	const delta = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Final answer\"}\n\n"
	for _, tt := range []struct {
		name   string
		stream string
		want   string
	}{
		{"completed status", delta + "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n", "completed"},
		{"terminal event without status at EOF", delta + "event: response.completed\ndata: {\"response\":{\"id\":\"response-test\"}}", "completed"},
		{"done sentinel after terminal", delta + "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\ndata: [DONE]\n\n", "completed"},
		{"explicit incomplete preserved", delta + "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n", "incomplete: max_output_tokens"},
		{"explicit cancellation preserved", delta + "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"cancelled\"}}\n\n", "cancelled"},
		{"explicit failure preserved", delta + "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"failed\"}}\n\n", "failed"},
		{"error overrides completion", delta + "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"error\":{\"message\":\"provider failure\"}}}\n\n", "provider failure"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var visible strings.Builder
			decoded, err := parseEventStreamReader(strings.NewReader(tt.stream), func(event StreamEvent) {
				if event.Type == StreamEventOutputTextDelta {
					visible.WriteString(event.Delta)
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			response := FromResponse(parseResponse(decoded))
			if response.StopReason != tt.want || response.Text != "Final answer" || visible.String() != "Final answer" {
				t.Fatalf("stop=%q text=%q visible=%q, want stop=%q with output preserved", response.StopReason, response.Text, visible.String(), tt.want)
			}
		})
	}
}

// Purpose: parseEventStreamReader must reject truncated, failed and cancelled
// streams even after visible output or completed tool construction. The parser
// boundary proves no executable response escapes on these failure paths.
func TestCodexTerminalCompletionRejectsMissingTerminal(t *testing.T) {
	const delta = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Partial answer\"}\n\n"
	for _, tail := range []string{
		"",
		"data: [DONE]\n\n",
		"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\"}}\n\n",
		"data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\"}}\n\n",
		"data: {\"type\":\"response.cancelled\",\"response\":{\"status\":\"cancelled\"}}\n\n",
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"call_id\":\"call-test\",\"name\":\"read\",\"arguments\":\"{}\"}}\n\n",
	} {
		decoded, err := parseEventStreamReader(strings.NewReader(delta+tail), nil)
		if err == nil || decoded != nil || !strings.Contains(err.Error(), "before response.completed") {
			t.Fatalf("tail %q: decoded=%v err=%v, want rejection without response", tail, decoded, err)
		}
	}
	decoded, err := parseEventStreamReader(io.MultiReader(strings.NewReader(delta), terminalErrorReader{}), nil)
	if !errors.Is(err, context.Canceled) || decoded != nil {
		t.Fatalf("cancelled reader: decoded=%v err=%v", decoded, err)
	}
}

type terminalErrorReader struct{}

func (terminalErrorReader) Read([]byte) (int, error) { return 0, context.Canceled }

// Purpose: preserving a completion stop reason must not remove or duplicate a
// function call. Exercise stream item merging and FromResponse, the adapter
// boundary supplying V3's HasFunctionCalls continuation decision.
func TestCodexTerminalCompletionPreservesToolContinuation(t *testing.T) {
	stream := "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"id\":\"item-test\",\"call_id\":\"call-test\",\"name\":\"read\",\"arguments\":\"{}\"}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"
	decoded, err := parseEventStreamReader(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatal(err)
	}
	response := FromResponse(parseResponse(decoded))
	if response.StopReason != "completed" || len(response.FunctionCalls) != 1 {
		t.Fatalf("response = %+v", response)
	}
	call := response.FunctionCalls[0]
	if call.CallID != "call-test" || call.Name != "read" || call.Arguments != "{}" {
		t.Fatalf("call = %+v", call)
	}
}

// Purpose: parseResponse also serves non-streaming Responses callers. Only an
// explicit terminal status may supply the missing stop reason; text and item
// status are not response completion evidence. Test the shared parser directly.
func TestCodexTerminalCompletionResponseStatus(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  map[string]any
		want string
	}{
		{"completed response", map[string]any{"status": "completed"}, "completed"},
		{"nested completed response", map[string]any{"response": map[string]any{"status": "completed"}}, "completed"},
		{"text only", map[string]any{"output_text": "answer"}, ""},
		{"explicit stop reason", map[string]any{"status": "completed", "stop_reason": "max_tokens"}, "max_tokens"},
		{"in progress", map[string]any{"status": "in_progress", "output_text": "answer"}, "in_progress"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := FromResponse(parseResponse(tt.raw)).StopReason; got != tt.want {
				t.Fatalf("stop reason = %q, want %q", got, tt.want)
			}
		})
	}
}
