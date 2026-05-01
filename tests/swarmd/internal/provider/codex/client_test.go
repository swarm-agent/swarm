package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type close1006Error struct {
	text string
}

func (e *close1006Error) Error() string {
	if e == nil {
		return "websocket: close 1006 (abnormal closure)"
	}
	if strings.TrimSpace(e.text) == "" {
		return "websocket: close 1006 (abnormal closure)"
	}
	return fmt.Sprintf("websocket: close 1006 (abnormal closure): %s", e.text)
}

func TestReasoningPayloadMapsNormalizedThinkingLevels(t *testing.T) {
	cases := []struct {
		name     string
		thinking string
		want     string
		hasValue bool
	}{
		{name: "off", thinking: "off", hasValue: false},
		{name: "empty", thinking: "", hasValue: false},
		{name: "low", thinking: "low", want: "low", hasValue: true},
		{name: "medium", thinking: "medium", want: "medium", hasValue: true},
		{name: "high", thinking: "high", want: "high", hasValue: true},
		{name: "xhigh", thinking: "xhigh", want: "xhigh", hasValue: true},
		{name: "invalid falls back to medium", thinking: "max", want: "medium", hasValue: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := reasoningPayload(tc.thinking)
			if !tc.hasValue {
				if payload != nil {
					t.Fatalf("payload = %#v, want nil", payload)
				}
				return
			}
			effort, _ := payload["effort"].(string)
			if effort != tc.want {
				t.Fatalf("effort = %q, want %q", effort, tc.want)
			}
			summary, _ := payload["summary"].(string)
			if summary != "auto" {
				t.Fatalf("summary = %q, want auto", summary)
			}
		})
	}
}

func TestNewClientUsesNoGlobalHTTPTimeout(t *testing.T) {
	client := NewClient(nil)
	if client == nil {
		t.Fatalf("NewClient returned nil")
	}
	if client.httpClient == nil {
		t.Fatalf("http client must be initialized")
	}
	if client.httpClient.Timeout != 0 {
		t.Fatalf("httpClient.Timeout = %s, want 0", client.httpClient.Timeout)
	}
}

func TestBuildRequestPayloadIncludesCodexParityFields(t *testing.T) {
	payload, err := buildRequestPayload(Request{
		Model:     "gpt-5.3-codex",
		SessionID: "sess_1",
		Thinking:  "low",
		Input: []map[string]any{
			{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "input_text",
						"text": "hello",
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("buildRequestPayload error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if got := strings.TrimSpace(asString(decoded["prompt_cache_key"])); got != "sess_1" {
		t.Fatalf("prompt_cache_key = %q, want sess_1", got)
	}
	text, ok := decoded["text"].(map[string]any)
	if !ok {
		t.Fatalf("text field missing or invalid: %#v", decoded["text"])
	}
	if got := strings.TrimSpace(asString(text["verbosity"])); got != defaultCodexTextVerbosity {
		t.Fatalf("text.verbosity = %q, want %q", got, defaultCodexTextVerbosity)
	}
	include := asSlice(decoded["include"])
	if len(include) != 1 || strings.TrimSpace(asString(include[0])) != includeReasoningEncryptedContentPath {
		t.Fatalf("include = %#v, want [%q]", include, includeReasoningEncryptedContentPath)
	}
}

func TestBuildRequestPayloadOmitsReasoningIncludeWhenThinkingOff(t *testing.T) {
	payload, err := buildRequestPayload(Request{
		Model:    "gpt-5.3-codex",
		Thinking: "off",
		Input: []map[string]any{
			{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "input_text",
						"text": "hello",
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("buildRequestPayload error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if _, ok := decoded["include"]; ok {
		t.Fatalf("include = %#v, want absent when reasoning is disabled", decoded["include"])
	}
	if _, ok := decoded["reasoning"]; ok {
		t.Fatalf("reasoning = %#v, want absent when thinking is off", decoded["reasoning"])
	}
	if text, ok := decoded["text"].(map[string]any); !ok || strings.TrimSpace(asString(text["verbosity"])) == "" {
		t.Fatalf("text field missing when thinking is off: %#v", decoded["text"])
	}
}

func TestBuildRequestPayloadNormalizesNilToolParameters(t *testing.T) {
	payload, err := buildRequestPayload(Request{
		Model: "gpt-5.3-codex",
		Input: []map[string]any{
			{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "input_text",
						"text": "hello",
					},
				},
			},
		},
		Tools: []ToolDefinition{
			{
				Type: "function",
				Name: "manage-agent",
			},
		},
	})
	if err != nil {
		t.Fatalf("buildRequestPayload error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	tools := asSlice(decoded["tools"])
	if len(tools) != 1 {
		t.Fatalf("tools = %#v, want one tool", decoded["tools"])
	}
	toolDef, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("tool = %#v, want object", tools[0])
	}
	parameters, ok := toolDef["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("parameters = %#v, want object", toolDef["parameters"])
	}
	if got := strings.TrimSpace(asString(parameters["type"])); got != "object" {
		t.Fatalf("parameters.type = %q, want object", got)
	}
	if properties, ok := parameters["properties"].(map[string]any); !ok || len(properties) != 0 {
		t.Fatalf("parameters.properties = %#v, want empty object", parameters["properties"])
	}
}

func TestBuildCodexWebsocketPayloadNormalizesNilToolParameters(t *testing.T) {
	payload, err := buildCodexWebsocketPayload(map[string]any{
		"model": "gpt-5.3-codex",
		"input": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
		"tools": []any{
			map[string]any{
				"type":       "function",
				"name":       "manage-agent",
				"parameters": nil,
			},
		},
	})
	if err != nil {
		t.Fatalf("buildCodexWebsocketPayload error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode websocket payload: %v", err)
	}
	tools := asSlice(decoded["tools"])
	if len(tools) != 1 {
		t.Fatalf("tools = %#v, want one tool", decoded["tools"])
	}
	toolDef, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("tool = %#v, want object", tools[0])
	}
	parameters, ok := toolDef["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("parameters = %#v, want object", toolDef["parameters"])
	}
	if got := strings.TrimSpace(asString(parameters["type"])); got != "object" {
		t.Fatalf("parameters.type = %q, want object", got)
	}
}

func TestSendUsesWebsocketResultWithoutFallback(t *testing.T) {
	client := NewClient(nil)
	client.sendWSFn = func(context.Context, pebblestore.CodexAuthRecord, []byte, func(StreamEvent)) (map[string]any, int, error) {
		return map[string]any{"output_text": "ws"}, http.StatusOK, nil
	}

	decoded, status, err := client.send(context.Background(), pebblestore.CodexAuthRecord{}, []byte(`{}`), nil)
	if err != nil {
		t.Fatalf("send error: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if strings.TrimSpace(asString(decoded["output_text"])) != "ws" {
		t.Fatalf("output_text = %#v, want ws", decoded["output_text"])
	}
	if got := strings.TrimSpace(asString(decoded[codexTransportMetadataKey])); got != codexTransportWebsocket {
		t.Fatalf("transport metadata = %q, want %q", got, codexTransportWebsocket)
	}
}

func TestSendReturnsWebsocketTransportError(t *testing.T) {
	client := NewClient(nil)
	client.sendWSFn = func(context.Context, pebblestore.CodexAuthRecord, []byte, func(StreamEvent)) (map[string]any, int, error) {
		return nil, 0, errors.New("ws dial failed")
	}

	_, _, err := client.send(context.Background(), pebblestore.CodexAuthRecord{}, []byte(`{}`), nil)
	if err == nil {
		t.Fatalf("send error = nil, want websocket transport error")
	}
	if !strings.Contains(err.Error(), "ws dial failed") {
		t.Fatalf("error = %q, want websocket transport context", err.Error())
	}
}

func TestSendRetriesWebsocketTransportError(t *testing.T) {
	client := NewClient(nil)
	wsCalls := 0
	client.sendWSFn = func(context.Context, pebblestore.CodexAuthRecord, []byte, func(StreamEvent)) (map[string]any, int, error) {
		wsCalls++
		if wsCalls == 1 {
			return nil, 0, errors.New("ws dial failed")
		}
		return map[string]any{"output_text": "ws"}, http.StatusOK, nil
	}

	decoded, status, err := client.send(context.Background(), pebblestore.CodexAuthRecord{}, []byte(`{}`), nil)
	if err != nil {
		t.Fatalf("send error: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got := strings.TrimSpace(asString(decoded["output_text"])); got != "ws" {
		t.Fatalf("output_text = %q, want ws", got)
	}
	if wsCalls != 2 {
		t.Fatalf("websocket calls = %d, want 2", wsCalls)
	}
}

func TestSendDoesNotRetryCanceledWebsocketTransportError(t *testing.T) {
	client := NewClient(nil)
	wsCalls := 0
	client.sendWSFn = func(context.Context, pebblestore.CodexAuthRecord, []byte, func(StreamEvent)) (map[string]any, int, error) {
		wsCalls++
		return nil, 0, context.Canceled
	}

	_, _, err := client.send(context.Background(), pebblestore.CodexAuthRecord{}, []byte(`{}`), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if wsCalls != 1 {
		t.Fatalf("websocket calls = %d, want 1", wsCalls)
	}
}

func TestSendReturnsContextCanceledWithoutRetryAfterStartedStream(t *testing.T) {
	client := NewClient(nil)
	wsCalls := 0
	client.sendWSFn = func(_ context.Context, _ pebblestore.CodexAuthRecord, _ []byte, onEvent func(StreamEvent)) (map[string]any, int, error) {
		wsCalls++
		onEvent(StreamEvent{Type: StreamEventOutputTextDelta, Delta: "partial"})
		return nil, 0, context.Canceled
	}

	_, _, err := client.send(context.Background(), pebblestore.CodexAuthRecord{}, []byte(`{}`), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if wsCalls != 1 {
		t.Fatalf("websocket calls = %d, want 1", wsCalls)
	}
}

func TestSendDoesNotRetryUnauthorizedStatus(t *testing.T) {
	client := NewClient(nil)
	wsCalls := 0
	client.sendWSFn = func(context.Context, pebblestore.CodexAuthRecord, []byte, func(StreamEvent)) (map[string]any, int, error) {
		wsCalls++
		return map[string]any{"raw_body": "unauthorized"}, http.StatusUnauthorized, nil
	}

	decoded, status, err := client.send(context.Background(), pebblestore.CodexAuthRecord{}, []byte(`{}`), nil)
	if err != nil {
		t.Fatalf("send error: %v", err)
	}
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", status, http.StatusUnauthorized)
	}
	if strings.TrimSpace(asString(decoded["raw_body"])) != "unauthorized" {
		t.Fatalf("raw_body = %#v, want unauthorized", decoded["raw_body"])
	}
	if wsCalls != 1 {
		t.Fatalf("websocket calls = %d, want 1", wsCalls)
	}
}

func TestSendRetriesForbiddenEmptyBody(t *testing.T) {
	client := NewClient(nil)
	wsCalls := 0
	client.sendWSFn = func(context.Context, pebblestore.CodexAuthRecord, []byte, func(StreamEvent)) (map[string]any, int, error) {
		wsCalls++
		if wsCalls == 1 {
			return map[string]any{"raw_body": ""}, http.StatusForbidden, nil
		}
		return map[string]any{"output_text": "ws"}, http.StatusOK, nil
	}

	decoded, status, err := client.send(context.Background(), pebblestore.CodexAuthRecord{}, []byte(`{}`), nil)
	if err != nil {
		t.Fatalf("send error: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got := strings.TrimSpace(asString(decoded["output_text"])); got != "ws" {
		t.Fatalf("output_text = %q, want ws", got)
	}
	if wsCalls != 2 {
		t.Fatalf("websocket calls = %d, want 2", wsCalls)
	}
}

func TestSendDoesNotRetryForbiddenWithBody(t *testing.T) {
	client := NewClient(nil)
	wsCalls := 0
	client.sendWSFn = func(context.Context, pebblestore.CodexAuthRecord, []byte, func(StreamEvent)) (map[string]any, int, error) {
		wsCalls++
		return map[string]any{"raw_body": "forbidden"}, http.StatusForbidden, nil
	}

	decoded, status, err := client.send(context.Background(), pebblestore.CodexAuthRecord{}, []byte(`{}`), nil)
	if err != nil {
		t.Fatalf("send error: %v", err)
	}
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", status, http.StatusForbidden)
	}
	if got := strings.TrimSpace(asString(decoded["raw_body"])); got != "forbidden" {
		t.Fatalf("raw_body = %q, want forbidden", got)
	}
	if wsCalls != 1 {
		t.Fatalf("websocket calls = %d, want 1", wsCalls)
	}
}

func TestSendDoesNotRetryTooManyRequests(t *testing.T) {
	client := NewClient(nil)
	wsCalls := 0
	client.sendWSFn = func(context.Context, pebblestore.CodexAuthRecord, []byte, func(StreamEvent)) (map[string]any, int, error) {
		wsCalls++
		return map[string]any{"raw_body": "rate limited"}, http.StatusTooManyRequests, nil
	}

	decoded, status, err := client.send(context.Background(), pebblestore.CodexAuthRecord{}, []byte(`{}`), nil)
	if err != nil {
		t.Fatalf("send error: %v", err)
	}
	if status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", status, http.StatusTooManyRequests)
	}
	if got := strings.TrimSpace(asString(decoded["raw_body"])); got != "rate limited" {
		t.Fatalf("raw_body = %q, want rate limited", got)
	}
	if wsCalls != 1 {
		t.Fatalf("websocket calls = %d, want 1", wsCalls)
	}
}

func TestSendRetriesServerErrorOverWebsocket(t *testing.T) {
	client := NewClient(nil)
	wsCalls := 0
	client.sendWSFn = func(context.Context, pebblestore.CodexAuthRecord, []byte, func(StreamEvent)) (map[string]any, int, error) {
		wsCalls++
		if wsCalls == 1 {
			return map[string]any{"raw_body": "ws unavailable"}, http.StatusServiceUnavailable, nil
		}
		return map[string]any{"output_text": "ws"}, http.StatusOK, nil
	}

	decoded, status, err := client.send(context.Background(), pebblestore.CodexAuthRecord{}, []byte(`{}`), nil)
	if err != nil {
		t.Fatalf("send error: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got := strings.TrimSpace(asString(decoded["output_text"])); got != "ws" {
		t.Fatalf("output_text = %q, want ws", got)
	}
	if wsCalls != 2 {
		t.Fatalf("websocket calls = %d, want 2", wsCalls)
	}
	if got, ok := asInt64(decoded["retry_attempts"]); !ok || got != 2 {
		t.Fatalf("retry_attempts = %#v, want 2", decoded["retry_attempts"])
	}
}

func TestSendDoesNotAnnotateRetryAttemptsOn429(t *testing.T) {
	client := NewClient(nil)
	wsCalls := 0
	client.sendWSFn = func(context.Context, pebblestore.CodexAuthRecord, []byte, func(StreamEvent)) (map[string]any, int, error) {
		wsCalls++
		return map[string]any{"raw_body": "rate limited"}, http.StatusTooManyRequests, nil
	}

	decoded, status, err := client.send(context.Background(), pebblestore.CodexAuthRecord{}, []byte(`{}`), nil)
	if err != nil {
		t.Fatalf("send error: %v", err)
	}
	if status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", status, http.StatusTooManyRequests)
	}
	if _, ok := decoded["retry_attempts"]; ok {
		t.Fatalf("retry_attempts = %#v, want absent when 429 is not retried", decoded["retry_attempts"])
	}
	if wsCalls != 1 {
		t.Fatalf("websocket calls = %d, want 1", wsCalls)
	}
}

func TestSendRetriesRetryableWebsocketStreamInterruptions(t *testing.T) {
	client := NewClient(nil)
	wsCalls := 0
	var streamedText []string
	var streamedReasoning []string
	client.sendWSFn = func(_ context.Context, _ pebblestore.CodexAuthRecord, _ []byte, onEvent func(StreamEvent)) (map[string]any, int, error) {
		wsCalls++
		onEvent(StreamEvent{Type: StreamEventOutputTextDelta, Delta: "hello"})
		onEvent(StreamEvent{Type: StreamEventReasoningSummaryDelta, Delta: "Inspecting"})
		if wsCalls < 3 {
			return nil, 0, newStartedWebsocketStreamError(&close1006Error{text: "unexpected EOF"})
		}
		onEvent(StreamEvent{Type: StreamEventOutputTextDelta, Delta: "hello world"})
		onEvent(StreamEvent{Type: StreamEventReasoningSummaryDelta, Delta: "Inspecting current state"})
		return map[string]any{"output_text": "hello world", "reasoning_summary_text": "Inspecting current state"}, http.StatusOK, nil
	}

	decoded, status, err := client.send(context.Background(), pebblestore.CodexAuthRecord{}, []byte(`{}`), func(event StreamEvent) {
		switch event.Type {
		case StreamEventOutputTextDelta:
			streamedText = append(streamedText, event.Delta)
		case StreamEventReasoningSummaryDelta:
			streamedReasoning = append(streamedReasoning, event.Delta)
		}
	})
	if err != nil {
		t.Fatalf("send error: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got := strings.TrimSpace(asString(decoded["output_text"])); got != "hello world" {
		t.Fatalf("output_text = %q, want hello world", got)
	}
	if wsCalls != 3 {
		t.Fatalf("websocket calls = %d, want 3", wsCalls)
	}
	if got := strings.Join(streamedText, ""); got != "hello world" {
		t.Fatalf("streamed output = %q, want hello world without duplication", got)
	}
	if len(streamedReasoning) != 2 {
		t.Fatalf("streamed reasoning events = %d, want 2", len(streamedReasoning))
	}
	if streamedReasoning[0] != "Inspecting" {
		t.Fatalf("first reasoning snapshot = %q, want Inspecting", streamedReasoning[0])
	}
	if streamedReasoning[1] != "Inspecting current state" {
		t.Fatalf("second reasoning snapshot = %q, want Inspecting current state", streamedReasoning[1])
	}
}

func TestSendFailsAfterStartedWebsocketStreamRetryLimit(t *testing.T) {
	client := NewClient(nil)
	wsCalls := 0
	client.sendWSFn = func(context.Context, pebblestore.CodexAuthRecord, []byte, func(StreamEvent)) (map[string]any, int, error) {
		wsCalls++
		return nil, 0, newStartedWebsocketStreamError(io.ErrUnexpectedEOF)
	}

	_, _, err := client.send(context.Background(), pebblestore.CodexAuthRecord{}, []byte(`{}`), nil)
	if err == nil {
		t.Fatalf("send error = nil, want websocket stream-started error")
	}
	if !errors.Is(err, errWebsocketStreamStarted) {
		t.Fatalf("error = %v, want errWebsocketStreamStarted", err)
	}
	if wsCalls != startedWebsocketStreamRetryLimit+1 {
		t.Fatalf("websocket calls = %d, want %d", wsCalls, startedWebsocketStreamRetryLimit+1)
	}
}

func TestSendDoesNotRetryNonRetryableStartedWebsocketStreamError(t *testing.T) {
	client := NewClient(nil)
	wsCalls := 0
	client.sendWSFn = func(context.Context, pebblestore.CodexAuthRecord, []byte, func(StreamEvent)) (map[string]any, int, error) {
		wsCalls++
		return nil, 0, newStartedWebsocketStreamError(errors.New("read timeout"))
	}

	_, _, err := client.send(context.Background(), pebblestore.CodexAuthRecord{}, []byte(`{}`), nil)
	if err == nil {
		t.Fatalf("send error = nil, want websocket stream-started error")
	}
	if !errors.Is(err, errWebsocketStreamStarted) {
		t.Fatalf("error = %v, want errWebsocketStreamStarted", err)
	}
	if wsCalls != 1 {
		t.Fatalf("websocket calls = %d, want 1", wsCalls)
	}
}

func TestShouldRetryWebsocketTransportError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "fresh retry sentinel", err: errWebsocketRetryFresh, want: true},
		{name: "context canceled", err: context.Canceled, want: false},
		{name: "deadline exceeded", err: context.DeadlineExceeded, want: false},
		{name: "started stream sentinel", err: newStartedWebsocketStreamError(io.ErrUnexpectedEOF), want: false},
		{name: "dial failure", err: errors.New("ws dial failed"), want: true},
	}

	for _, tc := range cases {
		if got := shouldRetryWebsocketTransportError(tc.err); got != tc.want {
			t.Fatalf("%s: shouldRetryWebsocketTransportError(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}

func TestShouldRetryStartedWebsocketStream(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "unexpected eof", err: newStartedWebsocketStreamError(io.ErrUnexpectedEOF), want: true},
		{name: "plain eof", err: newStartedWebsocketStreamError(io.EOF), want: true},
		{name: "abnormal close", err: newStartedWebsocketStreamError(&close1006Error{text: "unexpected EOF"}), want: true},
		{name: "message close 1006", err: newStartedWebsocketStreamError(errors.New("websocket: close 1006 (abnormal closure): unexpected EOF")), want: true},
		{name: "non retryable", err: newStartedWebsocketStreamError(errors.New("read timeout")), want: false},
		{name: "wrong sentinel", err: io.ErrUnexpectedEOF, want: false},
	}

	for _, tc := range cases {
		if got := shouldRetryStartedWebsocketStream(tc.err); got != tc.want {
			t.Fatalf("%s: shouldRetryStartedWebsocketStream(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}

func TestMergeRetriedStreamText(t *testing.T) {
	current, appended := mergeRetriedStreamText("hello", "hello world")
	if current != "hello world" || appended != " world" {
		t.Fatalf("mergeRetriedStreamText growth = (%q, %q), want (hello world,  world)", current, appended)
	}
	current, appended = mergeRetriedStreamText("hello world", "hello")
	if current != "hello world" || appended != "" {
		t.Fatalf("mergeRetriedStreamText retry replay = (%q, %q), want (hello world, \"\")", current, appended)
	}
}

func TestMergeRetriedReasoningSummary(t *testing.T) {
	current, snapshot, changed := mergeRetriedReasoningSummary("Inspecting", "Inspecting current state")
	if current != "Inspecting current state" || snapshot != "Inspecting current state" || !changed {
		t.Fatalf("mergeRetriedReasoningSummary growth = (%q, %q, %v)", current, snapshot, changed)
	}
	current, snapshot, changed = mergeRetriedReasoningSummary("Inspecting current state", "Inspecting")
	if current != "Inspecting current state" || snapshot != "" || changed {
		t.Fatalf("mergeRetriedReasoningSummary retry replay = (%q, %q, %v)", current, snapshot, changed)
	}
}

func TestBuildCodexTransportHeadersIncludesCodexDefaults(t *testing.T) {
	record := pebblestore.CodexAuthRecord{
		Type:        pebblestore.CodexAuthTypeOAuth,
		AccessToken: "oauth-token",
		AccountID:   "acct_123",
	}
	payload := []byte(`{"prompt_cache_key":"sess_123"}`)

	headers := buildCodexTransportHeaders(record, payload)

	if got := headers.Get("Authorization"); got != "Bearer oauth-token" {
		t.Fatalf("Authorization = %q, want Bearer oauth-token", got)
	}
	if got := headers.Get(originatorHeader); got != defaultOriginatorHeaderValue {
		t.Fatalf("originator = %q, want %q", got, defaultOriginatorHeaderValue)
	}
	if got := headers.Get(userAgentHeader); got != defaultCodexTransportUserAgent {
		t.Fatalf("User-Agent = %q, want %q", got, defaultCodexTransportUserAgent)
	}
	if got := headers.Get("session_id"); got != "sess_123" {
		t.Fatalf("session_id = %q, want sess_123", got)
	}
	if got := headers.Get(chatGPTAccountIDHeader); got != "acct_123" {
		t.Fatalf("%s = %q, want acct_123", chatGPTAccountIDHeader, got)
	}
	if got := headers.Get(openAIBetaHeader); got != responsesWebsocketBetaHeaderV2 {
		t.Fatalf("OpenAI-Beta = %q, want %q", got, responsesWebsocketBetaHeaderV2)
	}
}

func TestBuildCodexWebsocketPayloadDropsTransportOnlyFields(t *testing.T) {
	encoded, err := buildCodexWebsocketPayload(map[string]any{
		"model":      "gpt-5.4",
		"stream":     true,
		"background": true,
		"store":      false,
		"input": []any{
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": "hello"},
				},
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
	if got := strings.TrimSpace(asString(decoded["type"])); got != "response.create" {
		t.Fatalf("type = %q, want response.create", got)
	}
	if _, ok := decoded["stream"]; ok {
		t.Fatalf("stream = %#v, want absent", decoded["stream"])
	}
	if _, ok := decoded["background"]; ok {
		t.Fatalf("background = %#v, want absent", decoded["background"])
	}
}

func TestShouldRetryFreshWebsocketRequest(t *testing.T) {
	cases := []struct {
		name    string
		decoded map[string]any
		want    bool
	}{
		{
			name: "previous response not found",
			decoded: map[string]any{
				"type": "error",
				"error": map[string]any{
					"code": "previous_response_not_found",
				},
			},
			want: true,
		},
		{
			name: "connection limit reached",
			decoded: map[string]any{
				"type": "error",
				"error": map[string]any{
					"code": "websocket_connection_limit_reached",
				},
			},
			want: true,
		},
		{
			name: "other error",
			decoded: map[string]any{
				"type": "error",
				"error": map[string]any{
					"code": "invalid_request_error",
				},
			},
			want: false,
		},
	}

	for _, tc := range cases {
		if got := shouldRetryFreshWebsocketRequest(tc.decoded); got != tc.want {
			t.Fatalf("%s: shouldRetryFreshWebsocketRequest(%v) = %v, want %v", tc.name, tc.decoded, got, tc.want)
		}
	}
}

func TestCompactBodyRedactsSensitiveFields(t *testing.T) {
	input := map[string]any{
		"error":        "oauth failure",
		"access_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.header.payload",
		"nested": map[string]any{
			"refresh_token": "refresh-secret-value",
			"message":       "Bearer very-secret-token",
		},
		"raw_body": `{"token":"abc123","ok":false}`,
	}
	out := compactBody(input)
	for _, secret := range []string{
		"refresh-secret-value",
		"very-secret-token",
		"abc123",
	} {
		if strings.Contains(out, secret) {
			t.Fatalf("compactBody leaked secret %q: %s", secret, out)
		}
	}
	if !strings.Contains(out, "[redacted]") {
		t.Fatalf("compactBody missing redaction marker: %s", out)
	}
}

func TestSanitizeDiagnosticTextRedactsBearerAndQueryTokens(t *testing.T) {
	raw := `Authorization: Bearer abc.def.ghi access_token=xyz refresh_token=r123 sk-abcdef1234567890`
	sanitized := sanitizeDiagnosticText(raw)
	for _, secret := range []string{"abc.def.ghi", "xyz", "r123", "sk-abcdef1234567890"} {
		if strings.Contains(sanitized, secret) {
			t.Fatalf("sanitizeDiagnosticText leaked %q: %s", secret, sanitized)
		}
	}
}

func TestParseEventStreamCapturesReasoningTextDelta(t *testing.T) {
	stream := strings.Join([]string{
		"event: response.reasoning_text.delta",
		`data: {"type":"response.reasoning_text.delta","delta":"Inspecting current project state."}`,
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","delta":"Done."}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_1","output_text":"Done."}}`,
		"",
	}, "\n")

	decoded, err := parseEventStream([]byte(stream))
	if err != nil {
		t.Fatalf("parseEventStream error: %v", err)
	}
	got := strings.TrimSpace(asString(decoded["reasoning_summary_text"]))
	if got != "Inspecting current project state." {
		t.Fatalf("reasoning_summary_text = %q", got)
	}
}

func TestParseEventStreamDedupesCumulativeReasoningDeltas(t *testing.T) {
	stream := strings.Join([]string{
		"event: response.reasoning_summary_text.delta",
		`data: {"type":"response.reasoning_summary_text.delta","delta":"Inspecting"}`,
		"",
		"event: response.reasoning_summary_text.delta",
		`data: {"type":"response.reasoning_summary_text.delta","delta":"Inspecting current project state."}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_2","output_text":"Done."}}`,
		"",
	}, "\n")

	decoded, err := parseEventStream([]byte(stream))
	if err != nil {
		t.Fatalf("parseEventStream error: %v", err)
	}
	got := strings.TrimSpace(asString(decoded["reasoning_summary_text"]))
	if got != "Inspecting current project state." {
		t.Fatalf("reasoning_summary_text = %q", got)
	}
}

func TestParseEventStreamReaderSkipsReasoningModeDeltas(t *testing.T) {
	stream := strings.Join([]string{
		"event: response.reasoning_summary.delta",
		`data: {"type":"response.reasoning_summary.delta","delta":"detailed"}`,
		"",
		"event: response.reasoning_summary_text.delta",
		`data: {"type":"response.reasoning_summary_text.delta","delta":"Inspecting current project state."}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_3","output_text":"Done."}}`,
		"",
	}, "\n")

	var streamed []string
	decoded, err := parseEventStreamReader(strings.NewReader(stream), func(event StreamEvent) {
		if event.Type == StreamEventReasoningSummaryDelta {
			streamed = append(streamed, strings.TrimSpace(event.Delta))
		}
	})
	if err != nil {
		t.Fatalf("parseEventStreamReader error: %v", err)
	}
	if got := strings.TrimSpace(asString(decoded["reasoning_summary_text"])); got != "Inspecting current project state." {
		t.Fatalf("reasoning_summary_text = %q", got)
	}
	if len(streamed) != 1 {
		t.Fatalf("reasoning stream events = %d, want 1", len(streamed))
	}
	if streamed[0] != "Inspecting current project state." {
		t.Fatalf("streamed reasoning delta = %q, want full summary snapshot", streamed[0])
	}
}

func TestParseEventStreamReaderEmitsFullReasoningSnapshots(t *testing.T) {
	stream := strings.Join([]string{
		"event: response.reasoning_summary_text.delta",
		`data: {"type":"response.reasoning_summary_text.delta","delta":"Inspecting"}`,
		"",
		"event: response.reasoning_summary_text.delta",
		`data: {"type":"response.reasoning_summary_text.delta","delta":"Inspecting current project state."}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_4","output_text":"Done."}}`,
		"",
	}, "\n")

	var streamed []string
	decoded, err := parseEventStreamReader(strings.NewReader(stream), func(event StreamEvent) {
		if event.Type == StreamEventReasoningSummaryDelta {
			streamed = append(streamed, strings.TrimSpace(event.Delta))
		}
	})
	if err != nil {
		t.Fatalf("parseEventStreamReader error: %v", err)
	}
	if got := strings.TrimSpace(asString(decoded["reasoning_summary_text"])); got != "Inspecting current project state." {
		t.Fatalf("reasoning_summary_text = %q", got)
	}
	if len(streamed) != 2 {
		t.Fatalf("reasoning stream events = %d, want 2", len(streamed))
	}
	if streamed[0] != "Inspecting" {
		t.Fatalf("first streamed reasoning delta = %q, want Inspecting", streamed[0])
	}
	if streamed[1] != "Inspecting current project state." {
		t.Fatalf("second streamed reasoning delta = %q, want full snapshot", streamed[1])
	}
}

func TestParseEventStreamReaderDedupesCompletedMessageItems(t *testing.T) {
	stream := strings.Join([]string{
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_stream","type":"message","role":"assistant","content":[{"type":"output_text","text":"Done."}]}}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_dupe","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done."}]}]}}`,
		"",
	}, "\n")

	var streamed []string
	decoded, err := parseEventStreamReader(strings.NewReader(stream), func(event StreamEvent) {
		if event.Type == StreamEventOutputTextDelta {
			streamed = append(streamed, event.Delta)
		}
	})
	if err != nil {
		t.Fatalf("parseEventStreamReader error: %v", err)
	}
	if got := strings.Join(streamed, ""); got != "Done." {
		t.Fatalf("streamed output = %q, want Done.", got)
	}

	output := asSlice(decoded["output"])
	if len(output) != 1 {
		t.Fatalf("decoded output items = %d, want 1 after dedupe", len(output))
	}

	parsed := parseResponse(decoded)
	if got := strings.TrimSpace(parsed.Text); got != "Done." {
		t.Fatalf("parsed text = %q, want Done.", got)
	}
	if len(parsed.Messages) != 1 {
		t.Fatalf("parsed messages = %d, want 1 after dedupe", len(parsed.Messages))
	}
}

func TestParseEventStreamReaderCapturesOutputItems(t *testing.T) {
	stream := strings.Join([]string{
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"I'll run a quick read/grep/read sequence locally."}]}}`,
		"",
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","output_index":1,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"exec_command","arguments":"{\"cmd\":\"rg --files\"}"}}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_5"}}`,
		"",
	}, "\n")

	var streamed []string
	decoded, err := parseEventStreamReader(strings.NewReader(stream), func(event StreamEvent) {
		if event.Type == StreamEventOutputTextDelta {
			streamed = append(streamed, event.Delta)
		}
	})
	if err != nil {
		t.Fatalf("parseEventStreamReader error: %v", err)
	}
	output := asSlice(decoded["output"])
	if len(output) != 2 {
		t.Fatalf("decoded output items = %d, want 2", len(output))
	}
	if len(streamed) != 1 {
		t.Fatalf("streamed output deltas = %d, want 1", len(streamed))
	}
	if got := strings.TrimSpace(streamed[0]); got != "I'll run a quick read/grep/read sequence locally." {
		t.Fatalf("streamed output delta = %q", got)
	}

	parsed := parseResponse(decoded)
	if len(parsed.FunctionCalls) != 1 {
		t.Fatalf("function calls = %d, want 1", len(parsed.FunctionCalls))
	}
	if parsed.FunctionCalls[0].Name != "exec_command" {
		t.Fatalf("function call name = %q, want exec_command", parsed.FunctionCalls[0].Name)
	}
	if got := strings.TrimSpace(parsed.Text); got != "I'll run a quick read/grep/read sequence locally." {
		t.Fatalf("parsed text = %q", got)
	}
}

func TestParseResponseExtractsMessageInputTextWhenOutputTextMissing(t *testing.T) {
	decoded := map[string]any{
		"response": map[string]any{
			"id":    "resp_compact_1",
			"model": "gpt-5-codex",
			"output": []any{
				map[string]any{
					"type": "message",
					"role": "assistant",
					"content": []any{
						map[string]any{"type": "input_text", "text": "Recovered summary from message input_text."},
					},
				},
			},
		},
	}

	parsed := parseResponse(decoded)
	if got := strings.TrimSpace(parsed.Text); got != "Recovered summary from message input_text." {
		t.Fatalf("parsed text = %q, want recovered summary from input_text", got)
	}
}

func TestParseResponseExtractsReasoningSummarySummaryText(t *testing.T) {
	decoded := map[string]any{
		"response": map[string]any{
			"id":    "resp_compact_2",
			"model": "gpt-5-codex",
			"output": []any{
				map[string]any{
					"type": "reasoning",
					"summary": []any{
						map[string]any{"type": "summary_text", "text": "Compact summary recovered from reasoning summary_text."},
					},
				},
			},
		},
	}

	parsed := parseResponse(decoded)
	if got := strings.TrimSpace(parsed.ReasoningSummary); got != "Compact summary recovered from reasoning summary_text." {
		t.Fatalf("reasoning summary = %q, want summary_text recovery", got)
	}
}

func TestParseResponseMergesOutputItemsUsingInputTextWhenOutputTextMissing(t *testing.T) {
	decoded := map[string]any{
		"response": map[string]any{
			"id":    "resp_compact_3",
			"model": "gpt-5-codex",
		},
		"output": []any{
			map[string]any{
				"type": "message",
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "input_text", "text": "Recovered from merged output items."},
				},
			},
		},
	}

	parsed := parseResponse(decoded)
	if got := strings.TrimSpace(parsed.Text); got != "Recovered from merged output items." {
		t.Fatalf("parsed text = %q, want merged output-item input_text recovery", got)
	}
}

func TestParseResponseExtractsUsageWithCacheTokens(t *testing.T) {
	decoded := map[string]any{
		"response": map[string]any{
			"id":          "resp_123",
			"model":       "gpt-5-codex",
			"output_text": "Done.",
			"usage": map[string]any{
				"input_tokens":  float64(1200),
				"output_tokens": float64(300),
				"total_tokens":  float64(1500),
				"input_tokens_details": map[string]any{
					"cached_tokens":         float64(700),
					"cache_creation_tokens": float64(90),
				},
				"output_tokens_details": map[string]any{
					"reasoning_tokens": float64(44),
				},
			},
		},
	}

	out := parseResponse(decoded)
	if out.Usage.InputTokens != 1200 {
		t.Fatalf("input tokens = %d, want 1200", out.Usage.InputTokens)
	}
	if out.Usage.OutputTokens != 300 {
		t.Fatalf("output tokens = %d, want 300", out.Usage.OutputTokens)
	}
	if out.Usage.TotalTokens != 1500 {
		t.Fatalf("total tokens = %d, want 1500", out.Usage.TotalTokens)
	}
	if out.Usage.CacheReadTokens != 700 {
		t.Fatalf("cache read tokens = %d, want 700", out.Usage.CacheReadTokens)
	}
	if out.Usage.CacheWriteTokens != 90 {
		t.Fatalf("cache write tokens = %d, want 90", out.Usage.CacheWriteTokens)
	}
	if out.Usage.ThinkingTokens != 44 {
		t.Fatalf("thinking tokens = %d, want 44", out.Usage.ThinkingTokens)
	}
	if out.Usage.Source != "codex_api_usage" {
		t.Fatalf("usage source = %q, want codex_api_usage", out.Usage.Source)
	}
	if got := out.Usage.APIUsageRawPath; got != "response.usage" {
		t.Fatalf("api usage raw path = %q, want response.usage", got)
	}
	if len(out.Usage.APIUsageRaw) == 0 {
		t.Fatal("expected api usage raw payload")
	}
	if len(out.Usage.APIUsageHistory) != 1 {
		t.Fatalf("api usage history entries = %d, want 1", len(out.Usage.APIUsageHistory))
	}
	if len(out.Usage.APIUsagePaths) != 1 || out.Usage.APIUsagePaths[0] != "response.usage" {
		t.Fatalf("api usage paths = %#v, want [response.usage]", out.Usage.APIUsagePaths)
	}
}

func TestParseResponseUsageRequiresCanonicalAPIFields(t *testing.T) {
	decoded := map[string]any{
		"response": map[string]any{
			"id":          "resp_124",
			"model":       "gpt-5-codex",
			"output_text": "Done.",
			"usage": map[string]any{
				"prompt_tokens":           "11",
				"completion_tokens":       "7",
				"cache_read_input_tokens": float64(3),
			},
		},
	}

	out := parseResponse(decoded)
	if out.Usage.InputTokens != 0 {
		t.Fatalf("input tokens = %d, want 0", out.Usage.InputTokens)
	}
	if out.Usage.OutputTokens != 0 {
		t.Fatalf("output tokens = %d, want 0", out.Usage.OutputTokens)
	}
	if out.Usage.TotalTokens != 0 {
		t.Fatalf("total tokens = %d, want 0", out.Usage.TotalTokens)
	}
	if out.Usage.CacheReadTokens != 0 {
		t.Fatalf("cache read tokens = %d, want 0", out.Usage.CacheReadTokens)
	}
}

func TestAppendCodexThinkingDebugLineNoops(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex-debug.log")
	if err := appendCodexThinkingDebugLine(path, []byte(`{"event":"test"}`)); err != nil {
		t.Fatalf("appendCodexThinkingDebugLine returned error: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected no debug log file, stat err=%v", err)
	}
}
