package google

import (
	"encoding/json"
	"reflect"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

// Requirement: a follow-up after a tool result must retain both the result and
// the user's new instruction on the Gemini wire. Regression: merging them into
// one user turn causes Vertex to reject same-session retry/blocked-plan resume.
// Owners: buildGeminiRequest and normalizeGoogleContentsForRequest. Capturing
// both real runner transport variants is the narrowest deterministic boundary;
// live Vertex acceptance is qualified separately on the failed sessions.
func TestVertexContinuationKeepsToolResponseBoundary(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "unary", true: "streaming"}[streaming], func(t *testing.T) {
			request := provideriface.Request{Model: "gemini-3.8-flash", Input: []map[string]any{
				{"role": "user", "content": "Read the fixture."},
				{"type": "function_call", "call_id": "call-fixture", "name": "read", "arguments": "{\"path\":\"fixture.txt\"}"},
				{"type": "function_call_output", "call_id": "call-fixture", "output": "{\"text\":\"retained result\"}"},
				{"role": "user", "content": "Continue with the supplied token."},
			}}
			before, _ := json.Marshal(request.Input)
			runner, _, capture, ctx := newVertexTestRunner(t, "global", vertexReply)
			var err error
			if streaming {
				_, err = runner.CreateResponseStreaming(ctx, request, nil)
			} else {
				_, err = runner.CreateResponse(ctx, request)
			}
			if err != nil {
				t.Fatal(err)
			}
			sent := capture.all()
			if len(sent) != 1 {
				t.Fatalf("requests=%d", len(sent))
			}
			var wire googleRequest
			if err := json.Unmarshal([]byte(sent[0].body), &wire); err != nil {
				t.Fatal(err)
			}
			if len(wire.Contents) != 4 {
				t.Fatalf("tool response and follow-up merged: %+v", wire.Contents)
			}
			response, followup := wire.Contents[2], wire.Contents[3]
			if response.Role != "user" || len(response.Parts) != 1 || response.Parts[0].FunctionResponse == nil || response.Parts[0].FunctionResponse.Name != "read" || response.Parts[0].Text != "" {
				t.Fatalf("result lost or mixed: %+v", response)
			}
			if followup.Role != "user" || len(followup.Parts) != 1 || followup.Parts[0].Text != "Continue with the supplied token." || followup.Parts[0].FunctionResponse != nil {
				t.Fatalf("follow-up changed: %+v", followup)
			}
			after, _ := json.Marshal(request.Input)
			if string(before) != string(after) {
				t.Fatal("durable input mutated")
			}
		})
	}
	// Ordinary adjacent messages still normalize, while a tool-only ending keeps
	// its exact response (no invented continuation or reordering of tool effects).
	response := googleContent{Role: "user", Parts: []googlePart{{FunctionResponse: &googleFunctionResponse{Name: "read", Response: map[string]any{"output": "retained"}}}}}
	normalized := normalizeGoogleContentsForRequest([]googleContent{{Role: "user", Parts: []googlePart{{Text: "a"}}}, {Role: "user", Parts: []googlePart{{Text: "b"}}}, {Role: "model", Parts: []googlePart{{FunctionCall: &googleFunctionCall{Name: "read"}}}}, response})
	if len(normalized) != 3 || len(normalized[0].Parts) != 2 || !reflect.DeepEqual(normalized[2], response) {
		t.Fatalf("ordinary/tool-only normalization changed: %+v", normalized)
	}
}
