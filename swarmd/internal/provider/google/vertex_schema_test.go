package google

import (
	"encoding/json"
	"fmt"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/tool"
)

func TestVertexPreservesCanonicalToolJSONSchemasOnWire(t *testing.T) {
	// Purpose:
	// - Requirement: Vertex receives the actual runtime tool schemas unchanged,
	//   including ask-user alternatives, numeric enums and additionalProperties.
	// - Threat/regression: protobuf Schema rejects anyOf with siblings before
	//   inference; dropping constraints would misrepresent the runtime contract.
	// - Authority: VertexRunner.prepare and buildGeminiRequest select the wire field.
	// - Narrowest layer: captured unary/streaming HTTP requests with real definitions;
	//   this proves serialization, not live provider acceptance (tested separately).
	definitions := (&tool.Runtime{}).Definitions()
	request := provideriface.Request{Model: "gemini-test", Input: []map[string]any{{"role": "user", "content": "Inspect the fixture."}}}
	for _, d := range definitions {
		request.Tools = append(request.Tools, provideriface.ToolDefinition{Name: d.Name, Description: d.Description, Parameters: d.Parameters})
	}
	before, err := json.Marshal(request.Tools)
	if err != nil {
		t.Fatal(err)
	}
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("streaming=%v", streaming), func(t *testing.T) {
			runner, _, capture, ctx := newVertexTestRunner(t, "global", vertexReply)
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
			var wire struct {
				Tools []struct {
					Functions []map[string]json.RawMessage `json:"functionDeclarations"`
				} `json:"tools"`
			}
			if err := json.Unmarshal([]byte(sent[0].body), &wire); err != nil {
				t.Fatal(err)
			}
			if len(wire.Tools) != 1 || len(wire.Tools[0].Functions) != len(request.Tools) {
				t.Fatal("tool declarations missing")
			}
			for i, f := range wire.Tools[0].Functions {
				if _, ok := f["parameters"]; ok {
					t.Fatalf("%s used mutually exclusive legacy parameters", request.Tools[i].Name)
				}
				want, _ := json.Marshal(request.Tools[i].Parameters)
				if string(f["parametersJsonSchema"]) != string(want) {
					t.Fatalf("%s schema changed on wire", request.Tools[i].Name)
				}
			}
		})
	}
	after, _ := json.Marshal(request.Tools)
	if string(before) != string(after) {
		t.Fatal("canonical tool definitions mutated")
	}
	// Gemini API keeps its existing adapter surface; the Vertex repair must not
	// silently change a separate provider's request format.
	legacy, err := buildGoogleRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range legacy.Tools[0].FunctionDeclarations {
		if f.ParametersJSONSchema != nil || f.Parameters == nil {
			t.Fatal("Google API schema surface changed")
		}
	}
}
