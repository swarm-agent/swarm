package google

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type countingToolInvoker struct{ calls atomic.Int32 }

func (c *countingToolInvoker) ExecuteTool(context.Context, provideriface.ToolInvocation) (provideriface.ToolExecutionResult, error) {
	c.calls.Add(1)
	return provideriface.ToolExecutionResult{}, errors.New("the adapter must not run tools")
}

func TestVertexStreamWithoutFinishReasonIsMarkedIncompleteAndRunsNothing(t *testing.T) {
	// Purpose:
	// - Requirement: a Vertex streamGenerateContent reply that closes cleanly
	//   without a finish reason fails with provideriface.ErrStreamIncomplete
	//   (message unchanged: "vertex stream ended without a finish reason"),
	//   after exactly one HTTP request, and the adapter never calls the
	//   request's ToolInvoker even when the cut-off reply contained a function
	//   call. That is what lets the run replay the request once safely.
	// - Threat/regression: an untagged error kills the Coder job on a single
	//   truncated stream; an adapter that ran a half-streamed tool call, or
	//   replayed the HTTP request itself after forwarding output, would make a
	//   run-level replay execute an edit twice or duplicate output invisibly.
	// - Boundary/authority: generateContentTransport.stream
	//   (generate_content.go), googleStreamAccumulator.finished,
	//   provideriface.ErrStreamIncomplete.
	// - Narrowest test layer: VertexRunner round trip against an httptest
	//   Vertex endpoint; the run-level replay is covered in package run.
	calls := 0
	runner, _, capture, ctx := newVertexTestRunner(t, "global", func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Editing the file."}]}}],"usageMetadata":{"promptTokenCount":3}}`+"\n\n")
		fmt.Fprint(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"edit","args":{"path":"a.txt"}}}]}}]}`+"\n\n")
	})
	invoker := &countingToolInvoker{}
	var forwarded []provideriface.StreamEventType
	request := provideriface.Request{
		Model:       "gemini-3.5-flash-lite",
		Input:       []map[string]any{{"role": "user", "content": "edit a.txt"}},
		Tools:       []provideriface.ToolDefinition{{Type: "function", Name: "edit", Parameters: map[string]any{"type": "object"}}},
		ToolInvoker: invoker,
	}
	_, err := runner.CreateResponseStreaming(ctx, request, func(event provideriface.StreamEvent) {
		forwarded = append(forwarded, event.Type)
	})
	if !errors.Is(err, provideriface.ErrStreamIncomplete) || err.Error() != "vertex stream ended without a finish reason" {
		t.Fatalf("err = %v, want vertex ErrStreamIncomplete with the unchanged message", err)
	}
	if calls != 1 || len(capture.all()) != 1 {
		t.Fatalf("requests = %d, want exactly one (no in-adapter replay after output was forwarded)", calls)
	}
	if invoker.calls.Load() != 0 {
		t.Fatalf("tool invoker calls = %d, want 0", invoker.calls.Load())
	}
	if len(forwarded) == 0 || forwarded[0] != provideriface.StreamEventOutputTextDelta {
		t.Fatalf("forwarded = %v, want the partial text forwarded live before the failure", forwarded)
	}

	// A complete stream is not marked incomplete.
	complete, _, _, completeCtx := newVertexTestRunner(t, "global", vertexReply)
	if resp, err := complete.CreateResponseStreaming(completeCtx, request, nil); err != nil || resp.Text != "hello from vertex" {
		t.Fatalf("complete stream: resp = %q, err = %v", resp.Text, err)
	}
}
