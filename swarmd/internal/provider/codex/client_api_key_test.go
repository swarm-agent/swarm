package codex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestBuildRequestPayloadRemovesFunctionCallMetadata(t *testing.T) {
	originalMetadata := map[string]any{"phase": "commentary"}
	input := []map[string]any{
		{
			"type":      "function_call",
			"call_id":   "call_1",
			"name":      "read",
			"arguments": "{}",
			"metadata":  originalMetadata,
		},
		{
			"type":    "function_call_output",
			"call_id": "call_1",
			"output":  "ok",
		},
	}

	payload, err := buildRequestPayload(Request{
		Model: "gpt-5.3-codex",
		Input: input,
	})
	if err != nil {
		t.Fatalf("buildRequestPayload error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	decodedInput := asSlice(decoded["input"])
	if len(decodedInput) != 2 {
		t.Fatalf("input length = %d, want 2", len(decodedInput))
	}
	callInput, ok := decodedInput[0].(map[string]any)
	if !ok {
		t.Fatalf("input[0] = %#v, want object", decodedInput[0])
	}
	if _, ok := callInput["metadata"]; ok {
		t.Fatalf("input[0].metadata = %#v, want omitted", callInput["metadata"])
	}
	outputInput, ok := decodedInput[1].(map[string]any)
	if !ok {
		t.Fatalf("input[1] = %#v, want object", decodedInput[1])
	}
	if got := strings.TrimSpace(asString(outputInput["type"])); got != "function_call_output" {
		t.Fatalf("input[1].type = %q, want function_call_output", got)
	}
	if input[0]["metadata"] == nil {
		t.Fatal("source function_call metadata was mutated")
	}
}

func TestSendRoutesAPIKeyAuthToOpenAIResponsesHTTP(t *testing.T) {
	var gotAuth string
	var gotAccountID string
	var gotBeta string
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccountID = r.Header.Get(chatGPTAccountIDHeader)
		gotBeta = r.Header.Get(openAIBetaHeader)
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n"))
	}))
	defer server.Close()

	client := NewClient(nil)
	client.responsesAPIURL = server.URL + "/v1/responses"

	var deltas []string
	decoded, status, err := client.send(context.Background(), pebblestore.CodexAuthRecord{
		Type:   pebblestore.CodexAuthTypeAPI,
		APIKey: "sk-test",
	}, []byte(`{"model":"gpt-5","stream":true,"input":[]}`), func(event StreamEvent) {
		if event.Type == StreamEventOutputTextDelta {
			deltas = append(deltas, event.Delta)
		}
	})
	if err != nil {
		t.Fatalf("send API key error: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("Authorization = %q, want Bearer sk-test", gotAuth)
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("path = %q, want /v1/responses", gotPath)
	}
	if gotAccountID != "" {
		t.Fatalf("%s = %q, want empty for API key auth", chatGPTAccountIDHeader, gotAccountID)
	}
	if gotBeta != "" {
		t.Fatalf("%s = %q, want empty for API key auth", openAIBetaHeader, gotBeta)
	}
	if decoded[codexTransportMetadataKey] != codexTransportResponsesHTTP {
		t.Fatalf("transport metadata = %v, want %s", decoded[codexTransportMetadataKey], codexTransportResponsesHTTP)
	}
	if decoded[codexConnectedViaWSMetadataKey] != false {
		t.Fatalf("websocket metadata = %v, want false", decoded[codexConnectedViaWSMetadataKey])
	}
	if strings.Join(deltas, "") != "ok" {
		t.Fatalf("stream deltas = %q, want ok", strings.Join(deltas, ""))
	}
}
