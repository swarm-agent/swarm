package codex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

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

func TestBuildRequestPayloadSanitizesNativeResponseReplayItems(t *testing.T) {
	input := []map[string]any{
		{
			"role": "user",
			"content": []any{
				map[string]any{"type": "input_text", "text": "hello"},
			},
		},
		{
			"type":              "reasoning",
			"id":                "rs_1",
			"output_index":      float64(0),
			"content":           []any{},
			"summary":           []any{},
			"encrypted_content": "encrypted",
		},
		{
			"type":         "message",
			"id":           "msg_1",
			"output_index": float64(1),
			"phase":        "final_answer",
			"role":         "assistant",
			"status":       "completed",
			"content": []any{
				map[string]any{
					"type":        "output_text",
					"text":        "hi",
					"annotations": []any{},
					"logprobs":    []any{},
				},
			},
		},
	}

	payload, err := buildRequestPayload(Request{
		Model: "gpt-5.5",
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
	if len(decodedInput) != 3 {
		t.Fatalf("input length = %d, want 3", len(decodedInput))
	}
	reasoning, ok := decodedInput[1].(map[string]any)
	if !ok {
		t.Fatalf("input[1] = %#v, want object", decodedInput[1])
	}
	for _, key := range []string{"output_index"} {
		if _, ok := reasoning[key]; ok {
			t.Fatalf("reasoning.%s = %#v, want omitted in payload %s", key, reasoning[key], string(payload))
		}
	}
	contentRaw, hasContent := reasoning["content"]
	if !hasContent || contentRaw != nil {
		t.Fatalf("reasoning.content = %#v, want explicit null in payload %s", contentRaw, string(payload))
	}
	summaryRaw, hasSummary := reasoning["summary"]
	if !hasSummary {
		t.Fatalf("reasoning.summary missing, want empty array in payload %s", string(payload))
	}
	if summary := asSlice(summaryRaw); len(summary) != 0 {
		t.Fatalf("reasoning.summary = %#v, want empty array in payload %s", summaryRaw, string(payload))
	}
	if reasoning["encrypted_content"] != "encrypted" {
		t.Fatalf("reasoning encrypted_content = %#v, want encrypted", reasoning["encrypted_content"])
	}
	message, ok := decodedInput[2].(map[string]any)
	if !ok {
		t.Fatalf("input[2] = %#v, want object", decodedInput[2])
	}
	for _, key := range []string{"output_index", "phase"} {
		if _, ok := message[key]; ok {
			t.Fatalf("message.%s = %#v, want omitted in payload %s", key, message[key], string(payload))
		}
	}
	content := asSlice(message["content"])
	if len(content) != 1 {
		t.Fatalf("message content = %#v, want one item", message["content"])
	}
	contentItem, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("message content[0] = %#v, want object", content[0])
	}
	for _, key := range []string{"annotations", "logprobs"} {
		if _, ok := contentItem[key]; ok {
			t.Fatalf("message content[0].%s = %#v, want omitted in payload %s", key, contentItem[key], string(payload))
		}
	}
	if contentItem["text"] != "hi" {
		t.Fatalf("message content[0].text = %#v, want hi", contentItem["text"])
	}
}

func TestBuildRequestPayloadNormalizesReasoningReplaySummaryToArray(t *testing.T) {
	input := []map[string]any{
		{
			"type":              "reasoning",
			"id":                "rs_missing_summary",
			"encrypted_content": "encrypted-1",
		},
		{
			"type":              "reasoning",
			"id":                "rs_null_summary",
			"summary":           nil,
			"encrypted_content": "encrypted-2",
		},
		{
			"type":              "reasoning",
			"id":                "rs_bad_summary",
			"summary":           "not-an-array",
			"encrypted_content": "encrypted-3",
		},
		{
			"type": "reasoning",
			"id":   "rs_summary_text",
			"summary": []any{
				map[string]any{"type": "summary_text", "text": "kept"},
			},
			"encrypted_content": "encrypted-4",
		},
	}

	payload, err := buildRequestPayload(Request{
		Model: "gpt-5.5",
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
	if len(decodedInput) != 4 {
		t.Fatalf("input length = %d, want 4: %s", len(decodedInput), string(payload))
	}
	for index, item := range decodedInput {
		reasoning, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("input[%d] = %#v, want object", index, item)
		}
		summaryRaw, hasSummary := reasoning["summary"]
		if !hasSummary {
			t.Fatalf("input[%d].summary missing in payload %s", index, string(payload))
		}
		summary, ok := summaryRaw.([]any)
		if !ok {
			t.Fatalf("input[%d].summary = %#v, want array in payload %s", index, summaryRaw, string(payload))
		}
		if index < 3 && len(summary) != 0 {
			t.Fatalf("input[%d].summary = %#v, want empty array", index, summary)
		}
		if index == 3 && len(summary) != 1 {
			t.Fatalf("input[%d].summary = %#v, want preserved summary_text item", index, summary)
		}
		contentRaw, hasContent := reasoning["content"]
		if !hasContent || contentRaw != nil {
			t.Fatalf("input[%d].content = %#v, want explicit null in payload %s", index, contentRaw, string(payload))
		}
	}
}

func TestBuildCodexWebsocketPayloadPreservesReasoningSummaryArray(t *testing.T) {
	encoded, err := buildCodexWebsocketPayload(map[string]any{
		"model": "gpt-5.5",
		"input": []any{
			map[string]any{
				"type":              "reasoning",
				"id":                "rs_1",
				"summary":           []any{},
				"content":           nil,
				"encrypted_content": "encrypted",
			},
		},
	})
	if err != nil {
		t.Fatalf("buildCodexWebsocketPayload error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode websocket payload: %v", err)
	}
	input := asSlice(decoded["input"])
	if len(input) != 1 {
		t.Fatalf("input = %#v, want one item in payload %s", decoded["input"], string(encoded))
	}
	reasoning, ok := input[0].(map[string]any)
	if !ok {
		t.Fatalf("input[0] = %#v, want object", input[0])
	}
	summary, ok := reasoning["summary"].([]any)
	if !ok {
		t.Fatalf("reasoning.summary = %#v, want array in websocket payload %s", reasoning["summary"], string(encoded))
	}
	if len(summary) != 0 {
		t.Fatalf("reasoning.summary = %#v, want empty array", summary)
	}
	if content, ok := reasoning["content"]; !ok || content != nil {
		t.Fatalf("reasoning.content = %#v, want explicit null in websocket payload %s", content, string(encoded))
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
		_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"300\"}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"0\"}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\" , claude son\"}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"n\"}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"et\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"3000 , claude sonnet\"}]}]}}\n\n"))
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
	if strings.Join(deltas, "") != "3000 , claude sonnet" {
		t.Fatalf("stream deltas = %q, want exact repeated-character text", strings.Join(deltas, ""))
	}
}

func TestSendRetriesNormalWebsocketCloseAfterStreamStarted(t *testing.T) {
	client := NewClient(nil)
	calls := 0
	chunks := []string{"300", "0", " , claude son", "n", "et"}
	client.sendWSFn = func(_ context.Context, _ pebblestore.CodexAuthRecord, _ []byte, onEvent func(StreamEvent)) (map[string]any, int, error) {
		calls++
		for _, chunk := range chunks {
			onEvent(StreamEvent{Type: StreamEventOutputTextDelta, Delta: chunk})
		}
		if calls <= 3 {
			return nil, 0, newStartedWebsocketStreamError(&websocket.CloseError{Code: websocket.CloseNormalClosure, Text: "normal"})
		}
		return map[string]any{
			"id":          "resp_retry_ok",
			"model":       "gpt-5.5",
			"output_text": "3000 , claude sonnet",
		}, http.StatusOK, nil
	}

	var deltas []string
	decoded, status, err := client.send(context.Background(), pebblestore.CodexAuthRecord{
		Type:        pebblestore.CodexAuthTypeOAuth,
		AccessToken: "access-token",
	}, []byte(`{"model":"gpt-5.5","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`), func(event StreamEvent) {
		if event.Type == StreamEventOutputTextDelta {
			deltas = append(deltas, event.Delta)
		}
	})
	if err != nil {
		t.Fatalf("send error: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if calls != 4 {
		t.Fatalf("websocket attempts = %d, want 4", calls)
	}
	if got := strings.Join(deltas, ""); got != "3000 , claude sonnet" {
		t.Fatalf("streamed deltas = %q, want one exact text without retry duplicates", got)
	}
	if decoded["id"] != "resp_retry_ok" {
		t.Fatalf("decoded id = %#v, want resp_retry_ok", decoded["id"])
	}
}

func TestSendFailsNormalWebsocketCloseAfterStartedRetriesExhausted(t *testing.T) {
	client := NewClient(nil)
	calls := 0
	client.sendWSFn = func(_ context.Context, _ pebblestore.CodexAuthRecord, _ []byte, onEvent func(StreamEvent)) (map[string]any, int, error) {
		calls++
		onEvent(StreamEvent{Type: StreamEventOutputTextDelta, Delta: "partial"})
		return nil, 0, newStartedWebsocketStreamError(&websocket.CloseError{Code: websocket.CloseNormalClosure, Text: "normal"})
	}

	var deltas []string
	_, _, err := client.send(context.Background(), pebblestore.CodexAuthRecord{
		Type:        pebblestore.CodexAuthTypeOAuth,
		AccessToken: "access-token",
	}, []byte(`{"model":"gpt-5.5","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`), func(event StreamEvent) {
		if event.Type == StreamEventOutputTextDelta {
			deltas = append(deltas, event.Delta)
		}
	})
	if err == nil {
		t.Fatal("send error = nil, want started stream failure after retries")
	}
	if calls != 4 {
		t.Fatalf("websocket attempts = %d, want 4", calls)
	}
	if got := strings.Join(deltas, ""); got != "partial" {
		t.Fatalf("streamed deltas = %q, want one persisted partial without retry duplicates", got)
	}
}

func TestShouldRetryStartedWebsocketStreamNormalClose(t *testing.T) {
	err := newStartedWebsocketStreamError(&websocket.CloseError{Code: websocket.CloseNormalClosure, Text: "normal"})
	if !shouldRetryStartedWebsocketStream(err) {
		t.Fatal("normal close after payload started should be retried")
	}
}
