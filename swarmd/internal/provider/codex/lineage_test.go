package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestCodexAndOpenAIRunnerRequestConversionPreservesExplicitEpochLifecycle(t *testing.T) {
	request := provideriface.Request{
		SessionID:                 "durable-session",
		ProviderLineageID:         "epoch-lineage",
		ExecutionEpochID:          "epoch-2",
		ProviderCacheKey:          "cache-epoch-2",
		SessionAffinityKey:        "chain-epoch-2",
		TransportAffinityKey:      "transport-root",
		StartNewChain:             true,
		AllowContinuation:         false,
		ReuseTransport:            true,
		ResetTransport:            false,
		ForceFreshProviderContext: true,
		Model:                     "gpt-5.4",
	}
	converted := ToRequest(request)
	if converted.ProviderLineageID != request.ProviderLineageID || converted.ProviderCacheKey != request.ProviderCacheKey || converted.SessionAffinityKey != request.SessionAffinityKey || converted.TransportAffinityKey != request.TransportAffinityKey {
		t.Fatalf("runner conversion lost epoch keys: %+v", converted)
	}
	if !converted.StartNewChain || converted.AllowContinuation || !converted.ReuseTransport || converted.ResetTransport || !converted.ForceFreshProviderContext {
		t.Fatalf("runner conversion lost lifecycle policy: %+v", converted)
	}
}

func TestBuildRequestPayloadUsesProviderCacheKeyNotDurableSessionID(t *testing.T) {
	payload, err := buildRequestPayload(Request{
		SessionID:        "durable-session-id",
		ProviderCacheKey: "cache-lineage-key",
		Model:            "gpt-5.3-codex",
		Input: []map[string]any{{
			"role":    "user",
			"content": []map[string]any{{"type": "input_text", "text": "hello"}},
		}},
	})
	if err != nil {
		t.Fatalf("buildRequestPayload error: %v", err)
	}
	decoded := decodeTestPayload(t, payload)
	if got := asString(decoded["prompt_cache_key"]); got != "cache-lineage-key" {
		t.Fatalf("prompt_cache_key = %q, want cache-lineage-key", got)
	}
	if got := asString(decoded["prompt_cache_key"]); got == "durable-session-id" {
		t.Fatalf("prompt_cache_key used durable Swarm session id")
	}
}

func TestBuildRequestPayloadFallsBackToLineageWhenCacheKeyMissing(t *testing.T) {
	payload, err := buildRequestPayload(Request{
		SessionID:         "durable-session-id",
		ProviderLineageID: "provider-lineage-key",
		Model:             "gpt-5.3-codex",
		Input: []map[string]any{{
			"role":    "user",
			"content": []map[string]any{{"type": "input_text", "text": "hello"}},
		}},
	})
	if err != nil {
		t.Fatalf("buildRequestPayload error: %v", err)
	}
	decoded := decodeTestPayload(t, payload)
	if got := asString(decoded["prompt_cache_key"]); got != "provider-lineage-key" {
		t.Fatalf("prompt_cache_key = %q, want provider-lineage-key", got)
	}
}

func TestPrepareIncrementalWebsocketRequestDoesNotReuseAcrossLineagePayloads(t *testing.T) {
	for _, tc := range []struct {
		name          string
		previousKey   string
		currentKey    string
		previousModel string
		currentModel  string
	}{
		{name: "lineage", previousKey: "cache-old-lineage", currentKey: "cache-new-lineage", previousModel: "gpt-5.3-codex", currentModel: "gpt-5.3-codex"},
		{name: "model", previousKey: "cache-lineage", currentKey: "cache-lineage", previousModel: "gpt-5.3-codex", currentModel: "gpt-5.4-codex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := map[string]any{
				"model":            tc.previousModel,
				"prompt_cache_key": tc.previousKey,
				"input":            []any{map[string]any{"role": "user", "content": "old"}},
			}
			current := map[string]any{
				"model":            tc.currentModel,
				"prompt_cache_key": tc.currentKey,
				"input": []any{
					map[string]any{"role": "user", "content": "old"},
					map[string]any{"role": "user", "content": "new"},
				},
			}
			got := prepareIncrementalWebsocketRequest(current, previous, "resp_old", nil)
			if _, ok := got["previous_response_id"]; ok {
				t.Fatalf("previous_response_id was set across %s change: %#v", tc.name, got)
			}
			if len(asSlice(got["input"])) != 2 {
				t.Fatalf("input was made incremental across %s change: %#v", tc.name, got["input"])
			}
		})
	}
}

func TestPrepareIncrementalWebsocketRequestReusesWithinSameLineage(t *testing.T) {
	previous := map[string]any{
		"model":            "gpt-5.3-codex",
		"prompt_cache_key": "cache-lineage",
		"input":            []any{map[string]any{"role": "user", "content": "old"}},
	}
	current := map[string]any{
		"model":            "gpt-5.3-codex",
		"prompt_cache_key": "cache-lineage",
		"input": []any{
			map[string]any{"role": "user", "content": "old"},
			map[string]any{"role": "user", "content": "new"},
		},
	}
	got := prepareIncrementalWebsocketRequest(current, previous, "resp_old", nil)
	if gotID := asString(got["previous_response_id"]); gotID != "resp_old" {
		t.Fatalf("previous_response_id = %q, want resp_old", gotID)
	}
	input := asSlice(got["input"])
	if len(input) != 1 {
		t.Fatalf("incremental input length = %d, want 1: %#v", len(input), input)
	}
}

func TestCodexWebsocketSendPayloadMirrorsUpstreamToolOutputDelta(t *testing.T) {
	first := map[string]any{
		"model":            "gpt-5.3-codex",
		"prompt_cache_key": "cache-lineage",
		"input": []any{
			map[string]any{"role": "user", "content": "run the echo command"},
		},
	}
	firstPayload := codexWebsocketSendPayload(first, nil, false)
	if _, ok := firstPayload["previous_response_id"]; ok {
		t.Fatalf("first websocket request set previous_response_id: %#v", firstPayload)
	}
	if input := asSlice(firstPayload["input"]); len(input) != 1 {
		t.Fatalf("first websocket request input length = %d, want full single user item: %#v", len(input), input)
	}

	callID := "shell-command-call"
	functionCall := map[string]any{
		"type":    "function_call",
		"call_id": callID,
		"name":    "shell",
		"arguments": map[string]any{
			"command": "echo websocket",
		},
		"internal_chat_message_metadata_passthrough": map[string]any{"turn_id": "turn-123"},
	}
	functionCallOutput := map[string]any{
		"type":    "function_call_output",
		"call_id": callID,
		"output":  "websocket\n",
	}
	current := map[string]any{
		"model":            "gpt-5.3-codex",
		"prompt_cache_key": "cache-lineage",
		"input": []any{
			map[string]any{"role": "user", "content": "run the echo command"},
			functionCall,
			functionCallOutput,
		},
	}
	session := &cachedWebsocketSession{
		lastPayload:    first,
		lastResponseID: "resp-1",
		lastOutput:     []any{functionCall},
	}

	secondPayload := codexWebsocketSendPayload(current, session, false)
	if gotID := asString(secondPayload["previous_response_id"]); gotID != "resp-1" {
		t.Fatalf("second websocket previous_response_id = %q, want resp-1", gotID)
	}
	input := asSlice(secondPayload["input"])
	if len(input) != 1 {
		t.Fatalf("second websocket incremental input length = %d, want only function_call_output delta: %#v", len(input), input)
	}
	output, ok := input[0].(map[string]any)
	if !ok || asString(output["type"]) != "function_call_output" || asString(output["call_id"]) != callID {
		t.Fatalf("second websocket delta = %#v, want function_call_output for %s", input[0], callID)
	}
}

func TestPrepareIncrementalWebsocketRequestMatchesUpstreamProperties(t *testing.T) {
	previous := map[string]any{
		"model":            "gpt-5.3-codex",
		"prompt_cache_key": "cache-lineage",
		"stream_options":   map[string]any{"reasoning_summary_delivery": "delta"},
		"client_metadata":  map[string]any{"trace": "old"},
		"input":            []any{map[string]any{"role": "user", "content": "old"}},
	}
	current := map[string]any{
		"model":            "gpt-5.3-codex",
		"prompt_cache_key": "cache-lineage",
		"stream_options":   map[string]any{"reasoning_summary_delivery": "snapshot"},
		"client_metadata":  map[string]any{"trace": "new"},
		"input": []any{
			map[string]any{"role": "user", "content": "old"},
			map[string]any{"role": "user", "content": "new"},
		},
	}

	got := prepareIncrementalWebsocketRequest(current, previous, "resp_old", nil)
	if gotID := asString(got["previous_response_id"]); gotID != "resp_old" {
		t.Fatalf("ignored upstream fields prevented reuse; previous_response_id = %q", gotID)
	}

	changedText := map[string]any{
		"model":            "gpt-5.3-codex",
		"prompt_cache_key": "cache-lineage",
		"text":             map[string]any{"verbosity": "high"},
		"stream_options":   map[string]any{"reasoning_summary_delivery": "snapshot"},
		"client_metadata":  map[string]any{"trace": "new"},
		"input": []any{
			map[string]any{"role": "user", "content": "old"},
			map[string]any{"role": "user", "content": "new"},
		},
	}
	got = prepareIncrementalWebsocketRequest(changedText, previous, "resp_old", nil)
	if _, ok := got["previous_response_id"]; ok {
		t.Fatalf("text change reused previous response: %#v", got)
	}
}

func TestPrepareIncrementalWebsocketRequestIgnoresInternalItemMetadata(t *testing.T) {
	previous := map[string]any{
		"model":            "gpt-5.3-codex",
		"prompt_cache_key": "cache-lineage",
		"input": []any{map[string]any{
			"role":    "user",
			"content": "old",
			"internal_chat_message_metadata_passthrough": map[string]any{"turn_id": "old-turn"},
		}},
	}
	current := map[string]any{
		"model":            "gpt-5.3-codex",
		"prompt_cache_key": "cache-lineage",
		"input": []any{
			map[string]any{
				"role":    "user",
				"content": "old",
				"internal_chat_message_metadata_passthrough": map[string]any{"turn_id": "new-turn"},
			},
			map[string]any{"role": "user", "content": "new"},
		},
	}

	got := prepareIncrementalWebsocketRequest(current, previous, "resp_old", nil)
	if gotID := asString(got["previous_response_id"]); gotID != "resp_old" {
		t.Fatalf("internal metadata prevented reuse; previous_response_id = %q", gotID)
	}
	input := asSlice(got["input"])
	if len(input) != 1 {
		t.Fatalf("incremental input length = %d, want 1: %#v", len(input), input)
	}
}

func TestPrepareIncrementalWebsocketRequestUsesCompletedOutputBaseline(t *testing.T) {
	previous := map[string]any{
		"model":            "gpt-5.3-codex",
		"prompt_cache_key": "cache-lineage",
		"input":            []any{map[string]any{"role": "user", "content": "old"}},
	}
	lastOutput := []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "done"}}}}
	current := map[string]any{
		"model":            "gpt-5.3-codex",
		"prompt_cache_key": "cache-lineage",
		"input": []any{
			map[string]any{"role": "user", "content": "old"},
			map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "done"}}},
			map[string]any{"role": "user", "content": "new"},
		},
	}

	got := prepareIncrementalWebsocketRequest(current, previous, "resp_old", lastOutput)
	if gotID := asString(got["previous_response_id"]); gotID != "resp_old" {
		t.Fatalf("previous_response_id = %q, want resp_old", gotID)
	}
	input := asSlice(got["input"])
	if len(input) != 1 {
		t.Fatalf("incremental input length = %d, want 1: %#v", len(input), input)
	}
}

func TestRecordDoneOutputItemsTracksOnlyOutputItemDone(t *testing.T) {
	state := &streamDecodeState{}
	added := map[string]any{"type": "response.output_item.added", "output_index": float64(0), "item": map[string]any{"id": "partial", "type": "message", "status": "in_progress"}}
	done := map[string]any{"type": "response.output_item.done", "output_index": float64(0), "item": map[string]any{"id": "done", "type": "message", "status": "completed"}}

	processResponseStreamEvent("response.output_item.added", mustMarshalTestJSON(t, added), state, nil)
	if len(state.outputItemsDone) != 0 {
		t.Fatalf("added event was recorded as completed output: %#v", state.outputItemsDone)
	}
	processResponseStreamEvent("response.output_item.done", mustMarshalTestJSON(t, done), state, nil)
	if len(state.outputItemsDone) != 1 || asString(state.outputItemsDone[0]["id"]) != "done" {
		t.Fatalf("done output items = %#v, want done item only", state.outputItemsDone)
	}
}

func TestBuildRequestPayloadWithOptionsMarksFreshContext(t *testing.T) {
	_, forceFresh, err := buildRequestPayloadWithOptions(Request{
		ProviderCacheKey:          "cache-lineage-key",
		Model:                     "gpt-5.3-codex",
		NativeContinuationAllowed: false,
		Input: []map[string]any{{
			"role":    "user",
			"content": []map[string]any{{"type": "input_text", "text": "hello"}},
		}},
	})
	if err != nil {
		t.Fatalf("buildRequestPayloadWithOptions error: %v", err)
	}
	if !forceFresh {
		t.Fatalf("forceFresh = false, want true when native continuation is disabled")
	}
}

func TestBuildRequestPayloadWithOptionsAllowsNativeContinuation(t *testing.T) {
	_, forceFresh, err := buildRequestPayloadWithOptions(Request{
		ProviderCacheKey:          "cache-lineage-key",
		Model:                     "gpt-5.3-codex",
		NativeContinuationAllowed: true,
		Input: []map[string]any{{
			"role":    "user",
			"content": []map[string]any{{"type": "input_text", "text": "hello"}},
		}},
	})
	if err != nil {
		t.Fatalf("buildRequestPayloadWithOptions error: %v", err)
	}
	if forceFresh {
		t.Fatalf("forceFresh = true, want false when native continuation is allowed")
	}
}

func decodeTestPayload(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	decoded, err := decodeCodexPayload(payload)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return decoded
}

func TestCodexWebsocketRequestPayloadUsesIncrementalDeltaWithoutFullPayloadBuild(t *testing.T) {
	client := NewClient(nil)
	ctx := contextWithCodexTransportContext(context.Background(), codexTransportContext{
		PromptCacheKey:            "cache-lineage-key",
		SessionAffinityKey:        "affinity-window-key",
		NativeContinuationAllowed: true,
	})
	session := client.cachedWebsocketSession("affinity-window-key")
	session.lastRequestProperties = map[string]any{
		"model":            "gpt-5.3-codex",
		"stream":           true,
		"store":            false,
		"prompt_cache_key": "cache-lineage-key",
		"text":             map[string]any{"verbosity": defaultCodexTextVerbosity},
	}
	session.lastInputLen = 1
	session.lastResponseID = "resp-1"
	session.lastOutput = []any{map[string]any{"type": "function_call", "call_id": "shell-command-call", "name": "shell", "arguments": map[string]any{"command": "echo websocket"}}}

	payload, properties, inputLen, err := client.codexWebsocketRequestPayload(ctx, Request{
		ProviderCacheKey:          "cache-lineage-key",
		SessionAffinityKey:        "affinity-window-key",
		Model:                     "gpt-5.3-codex",
		NativeContinuationAllowed: true,
		Input: []map[string]any{
			{"role": "user", "content": "run the echo command"},
			{"type": "function_call", "call_id": "shell-command-call", "name": "shell", "arguments": map[string]any{"command": "echo websocket"}},
			{"type": "function_call_output", "call_id": "shell-command-call", "output": "websocket\n"},
		},
	})
	if err != nil {
		t.Fatalf("codexWebsocketRequestPayload error: %v", err)
	}
	if gotID := asString(payload["previous_response_id"]); gotID != "resp-1" {
		t.Fatalf("previous_response_id = %q, want resp-1: %#v", gotID, payload)
	}
	input := asSlice(payload["input"])
	if len(input) != 1 {
		t.Fatalf("incremental input length = %d, want only function_call_output delta: %#v", len(input), input)
	}
	output, ok := input[0].(map[string]any)
	if !ok || asString(output["type"]) != "function_call_output" {
		t.Fatalf("incremental input = %#v, want function_call_output", input[0])
	}
	if inputLen != 3 {
		t.Fatalf("inputLen = %d, want full request input length 3", inputLen)
	}
	if !codexWebsocketRequestPropertiesMatch(properties, session.lastRequestProperties) {
		t.Fatalf("request properties changed unexpectedly: %#v", properties)
	}
}

func TestCodexFreshWebsocketPayloadDoesNotReusePreviousResponseEvenWithSameProviderCacheKey(t *testing.T) {
	current := map[string]any{
		"model":            "gpt-5.3-codex",
		"prompt_cache_key": "cache-lineage-key",
		"input": []any{
			map[string]any{"role": "user", "content": "bounded checkpoint summary"},
			map[string]any{"role": "user", "content": "new follow-up"},
		},
	}
	session := &cachedWebsocketSession{
		lastPayload: map[string]any{
			"model":            "gpt-5.3-codex",
			"prompt_cache_key": "cache-lineage-key",
			"input":            []any{map[string]any{"role": "user", "content": "bounded checkpoint summary"}},
		},
		lastResponseID: "resp_old_provider_context",
	}

	reused := codexWebsocketSendPayload(current, session, false)
	if gotID := asString(reused["previous_response_id"]); gotID != "resp_old_provider_context" {
		t.Fatalf("non-fresh previous_response_id = %q, want old response id", gotID)
	}

	fresh := codexWebsocketSendPayload(current, session, true)
	if _, ok := fresh["previous_response_id"]; ok {
		t.Fatalf("fresh websocket payload reused previous_response_id despite same prompt cache key: %#v", fresh)
	}
	input := asSlice(fresh["input"])
	if len(input) != 2 {
		t.Fatalf("fresh websocket payload was made incremental: %#v", fresh["input"])
	}
}

func TestCodexTransportAffinityIsIndependentFromEpochChain(t *testing.T) {
	client := NewClient(nil)
	for _, chainKey := range []string{"epoch-chain-a", "epoch-chain-b"} {
		ctx := contextWithCodexTransportContext(context.Background(), codexTransportContext{
			PromptCacheKey:     "stable-runtime-cache",
			SessionAffinityKey: "transport-root-key",
			StartNewChain:      true,
			ReuseTransport:     true,
		})
		payload, _, _, err := client.codexWebsocketRequestPayload(ctx, Request{
			ProviderCacheKey:     "stable-runtime-cache",
			SessionAffinityKey:   chainKey,
			TransportAffinityKey: "transport-root-key",
			Model:                "gpt-5.3-codex",
			StartNewChain:        true,
			ReuseTransport:       true,
			Input:                []map[string]any{{"role": "user", "content": chainKey}},
		})
		if err != nil {
			t.Fatalf("build %s payload: %v", chainKey, err)
		}
		if _, ok := payload["previous_response_id"]; ok {
			t.Fatalf("new epoch chain %s reused predecessor response: %#v", chainKey, payload)
		}
	}
	if client.cachedWebsocketSession("transport-root-key") == nil {
		t.Fatal("transport affinity did not select reusable root transport")
	}
}

func TestCodexFirstEpochResponseBecomesContinuationBaseline(t *testing.T) {
	client := NewClient(nil)
	ctx := contextWithCodexTransportContext(context.Background(), codexTransportContext{
		PromptCacheKey:     "stable-runtime-cache",
		SessionAffinityKey: "transport-root-key",
		AllowContinuation:  true,
	})
	session := client.cachedWebsocketSession("transport-root-key")
	session.lastRequestProperties = map[string]any{
		"model": "gpt-5.3-codex", "stream": true, "store": false,
		"prompt_cache_key": "stable-runtime-cache",
		"text":             map[string]any{"verbosity": defaultCodexTextVerbosity},
	}
	session.lastInputLen = 1
	session.lastResponseID = "resp-first-in-epoch"

	payload, _, _, err := client.codexWebsocketRequestPayload(ctx, Request{
		ProviderCacheKey:     "stable-runtime-cache",
		TransportAffinityKey: "transport-root-key",
		Model:                "gpt-5.3-codex",
		AllowContinuation:    true,
		Input: []map[string]any{
			{"role": "user", "content": "first epoch turn"},
			{"role": "user", "content": "later epoch turn"},
		},
	})
	if err != nil {
		t.Fatalf("continuation payload: %v", err)
	}
	if got := asString(payload["previous_response_id"]); got != "resp-first-in-epoch" {
		t.Fatalf("previous_response_id = %q, want first response baseline", got)
	}
}

func TestPrepareCachedWebsocketSessionNewChainReusesHealthyTransportWithoutLineage(t *testing.T) {
	conn := &websocket.Conn{}
	session := &cachedWebsocketSession{
		conn:                  conn,
		lastPayload:           map[string]any{"model": "gpt-5.3-codex"},
		lastRequestProperties: map[string]any{"model": "gpt-5.3-codex"},
		lastInputLen:          2,
		lastResponseID:        "resp-old-epoch",
		lastOutput:            []any{"old"},
	}

	gotConn, reused, err := prepareCachedWebsocketSessionLocked(session, codexTransportContext{ReuseTransport: true}, true, true, map[string]any{"input": []any{"new epoch"}})
	if err != nil {
		t.Fatalf("prepare new chain: %v", err)
	}
	if gotConn != conn || !reused {
		t.Fatalf("new chain did not reuse healthy websocket: conn=%p want=%p reused=%t", gotConn, conn, reused)
	}
	if session.conn != conn {
		t.Fatal("new chain reset healthy websocket transport")
	}
	if session.lastResponseID != "" || session.lastPayload != nil || session.lastRequestProperties != nil || session.lastInputLen != 0 || session.lastOutput != nil {
		t.Fatalf("new chain retained provider continuation state: %#v", session)
	}
}

func TestCodexTransportResetAfterPermissionWaitUsesFullInputWithoutPreviousResponse(t *testing.T) {
	client := NewClient(nil)
	ctx := contextWithCodexTransportContext(context.Background(), codexTransportContext{
		PromptCacheKey:     "stable-runtime-cache",
		SessionAffinityKey: "transport-root-key",
		AllowContinuation:  true,
		ReuseTransport:     true,
		ResetTransport:     true,
	})
	session := client.cachedWebsocketSession("transport-root-key")
	session.lastRequestProperties = map[string]any{
		"model": "gpt-5.3-codex", "stream": true, "store": false,
		"prompt_cache_key": "stable-runtime-cache",
		"text":             map[string]any{"verbosity": defaultCodexTextVerbosity},
	}
	session.lastInputLen = 1
	session.lastResponseID = "resp-before-permission-wait"
	session.lastOutput = []any{map[string]any{"type": "function_call", "call_id": "call-1", "name": "bash", "arguments": `{}`}}

	input := []map[string]any{
		{"role": "user", "content": "run a command"},
		{"type": "function_call", "call_id": "call-1", "name": "bash", "arguments": `{}`},
		{"type": "function_call_output", "call_id": "call-1", "output": "ok"},
	}
	payload, _, _, err := client.codexWebsocketRequestPayload(ctx, Request{
		ProviderCacheKey:     "stable-runtime-cache",
		TransportAffinityKey: "transport-root-key",
		Model:                "gpt-5.3-codex",
		AllowContinuation:    true,
		ReuseTransport:       true,
		ResetTransport:       true,
		Input:                input,
	})
	if err != nil {
		t.Fatalf("reset continuation payload: %v", err)
	}
	if _, ok := payload["previous_response_id"]; ok {
		t.Fatalf("reset continuation retained previous_response_id: %#v", payload)
	}
	if got := len(asSlice(payload["input"])); got != len(input) {
		t.Fatalf("reset continuation input length = %d, want full %d-item input", got, len(input))
	}
}

func TestPrepareCachedWebsocketSessionInEpochContinuationRetainsLineage(t *testing.T) {
	conn := &websocket.Conn{}
	session := &cachedWebsocketSession{
		conn:           conn,
		lastResponseID: "resp-in-epoch",
		lastPayload:    map[string]any{"input": []any{"first turn"}},
	}

	gotConn, reused, err := prepareCachedWebsocketSessionLocked(session, codexTransportContext{ReuseTransport: true}, false, false, map[string]any{"input": []any{"second turn"}})
	if err != nil {
		t.Fatalf("prepare in-epoch continuation: %v", err)
	}
	if gotConn != conn || !reused {
		t.Fatalf("in-epoch continuation did not reuse websocket: conn=%p want=%p reused=%t", gotConn, conn, reused)
	}
	if session.lastResponseID != "resp-in-epoch" {
		t.Fatalf("in-epoch continuation lost response lineage: %q", session.lastResponseID)
	}
}

func TestCodexTransportUsesSessionAffinityForHeadersAndWebsocketCache(t *testing.T) {
	transport := codexTransportContext{
		PromptCacheKey:     "cache-lineage-key",
		SessionAffinityKey: "affinity-window-key",
	}
	headers := buildCodexTransportHeaders(pebblestore.CodexAuthRecord{AccessToken: "token", Type: pebblestore.CodexAuthTypeOAuth}, transport)
	if got := headers.Get("session_id"); got != "affinity-window-key" {
		t.Fatalf("session_id header = %q, want affinity-window-key", got)
	}
	if got := headers.Get("x-codex-window-id"); got != "affinity-window-key" {
		t.Fatalf("x-codex-window-id header = %q, want affinity-window-key", got)
	}

	client := NewClient(nil)
	first := client.cachedWebsocketSession(transport.SessionAffinityKey)
	second := client.cachedWebsocketSession(transport.SessionAffinityKey)
	if first == nil || first != second {
		t.Fatalf("websocket cache did not reuse same affinity session")
	}
	if cacheSession := client.cachedWebsocketSession(transport.PromptCacheKey); cacheSession == nil || cacheSession == first {
		t.Fatalf("prompt cache key was coupled to websocket affinity cache")
	}
}

func TestCodexOutboundShapeDiagnosticsAreSafeAndCountNativeInput(t *testing.T) {
	event := codexOutboundShapeEvent("websocket_request_shape", codexTransportContext{
		PromptCacheKey:            "cache-lineage-key",
		SessionAffinityKey:        "affinity-window-key",
		NativeContinuationAllowed: false,
		ForceFreshProviderContext: true,
		BoundaryReason:            "checkpoint_fresh_context",
	}, map[string]any{"input": []any{
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hello"}}},
		map[string]any{"type": "reasoning", "encrypted_content": "secret-token"},
	}}, false, false)

	if event.Extra["prompt_cache_key_present"] != true || event.Extra["session_affinity_key_present"] != true {
		t.Fatalf("diagnostic missing key presence: %+v", event.Extra)
	}
	if event.Extra["session_affinity_key_hash"] == "" || event.Extra["session_affinity_key_hash"] == "affinity-window-key" {
		t.Fatalf("diagnostic did not hash affinity key safely: %+v", event.Extra)
	}
	if event.Extra["previous_response_id_present"] != false || event.Extra["websocket_reused"] != false {
		t.Fatalf("fresh diagnostic reported reuse: %+v", event.Extra)
	}
	if event.Extra["input_items"] != 2 || event.Extra["input_text_chars"] != 40 || event.Extra["native_input_items"] != 1 || event.Extra["encrypted_input_items"] != 1 {
		t.Fatalf("diagnostic input shape = %+v", event.Extra)
	}
}

func TestResponsesSameEpochContinuationUsesMatchingLocalChainOtherwiseFullInput(t *testing.T) {
	client := NewClient(nil)
	session := client.cachedWebsocketSession("transport-key")
	session.lastRequestProperties = map[string]any{
		"model":            "gpt-5.4",
		"stream":           true,
		"store":            false,
		"prompt_cache_key": "cache-epoch-a",
		"text":             map[string]any{"verbosity": defaultCodexTextVerbosity},
	}
	session.lastInputLen = 1
	session.lastResponseID = "resp-epoch-a"
	session.lastOutput = []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "answer"}}}}
	ctx := contextWithCodexTransportContext(context.Background(), codexTransportContext{SessionAffinityKey: "transport-key", AllowContinuation: true})
	fullInput := []map[string]any{
		{"role": "user", "content": "first"},
		{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "answer"}}},
		{"role": "user", "content": "second"},
	}
	matching, _, _, err := client.codexWebsocketRequestPayload(ctx, Request{ProviderCacheKey: "cache-epoch-a", Model: "gpt-5.4", AllowContinuation: true, Input: fullInput})
	if err != nil {
		t.Fatalf("matching chain payload: %v", err)
	}
	if got := asString(matching["previous_response_id"]); got != "resp-epoch-a" || len(asSlice(matching["input"])) != 1 {
		t.Fatalf("matching chain payload = %#v, want response ID and one-item delta", matching)
	}
	mismatched, _, _, err := client.codexWebsocketRequestPayload(ctx, Request{ProviderCacheKey: "cache-epoch-b", Model: "gpt-5.4", AllowContinuation: true, Input: fullInput})
	if err != nil {
		t.Fatalf("mismatched chain payload: %v", err)
	}
	if _, ok := mismatched["previous_response_id"]; ok {
		t.Fatalf("mismatched local chain reused response ID: %#v", mismatched)
	}
	if got := reflect.ValueOf(mismatched["input"]); !got.IsValid() || got.Len() != len(fullInput) {
		t.Fatalf("mismatched local chain input = %#v, want full input", mismatched["input"])
	}
}

func TestResponsesTransportCompatibilityKeySeparatesCredentialsProviderModelAndEndpointNotEpoch(t *testing.T) {
	client := NewClient(nil)
	baseRecord := pebblestore.CodexAuthRecord{Provider: "codex", Type: pebblestore.CodexAuthTypeOAuth, AccountScopeID: "account-a", ID: "credential-a", AccountID: "chatgpt-a"}
	baseReq := Request{TransportAffinityKey: "root-session", SessionAffinityKey: "epoch-chain-a", ProviderLineageID: "lineage-a", Model: "gpt-5.3-codex"}
	base := client.responsesTransportCompatibilityKey(baseRecord, baseReq)
	if base == "" {
		t.Fatal("compatibility key is empty")
	}
	epochChanged := baseReq
	epochChanged.SessionAffinityKey = "epoch-chain-b"
	epochChanged.ProviderLineageID = "lineage-b"
	if got := client.responsesTransportCompatibilityKey(baseRecord, epochChanged); got != base {
		t.Fatalf("epoch rotated transport compatibility key: %q != %q", got, base)
	}
	cases := []struct {
		name   string
		record pebblestore.CodexAuthRecord
		req    Request
		client *Client
	}{
		{name: "account", record: pebblestore.CodexAuthRecord{Provider: "codex", Type: pebblestore.CodexAuthTypeOAuth, AccountScopeID: "account-b", ID: "credential-a", AccountID: "chatgpt-a"}, req: baseReq, client: client},
		{name: "provider", record: pebblestore.CodexAuthRecord{Provider: "openai", Type: pebblestore.CodexAuthTypeAPI, AccountScopeID: "account-a", ID: "credential-a", APIKey: "key-a"}, req: baseReq, client: client},
		{name: "model", record: baseRecord, req: Request{TransportAffinityKey: "root-session", Model: "gpt-5.4-codex"}, client: client},
		{name: "endpoint", record: baseRecord, req: baseReq, client: &Client{responsesWSURL: "wss://example.test/v1/responses"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.client.responsesTransportCompatibilityKey(tc.record, tc.req); got == base {
				t.Fatalf("incompatible %s reused transport key %q", tc.name, got)
			}
		})
	}
	apiReq := Request{TransportAffinityKey: "root-session", Model: "gpt-5.4"}
	apiA := pebblestore.CodexAuthRecord{Provider: "openai", Type: pebblestore.CodexAuthTypeAPI, AccountScopeID: "account-a", ID: "credential-a", APIKey: "key-a"}
	apiB := apiA
	apiB.APIKey = "key-b"
	if client.responsesTransportCompatibilityKey(apiA, apiReq) == client.responsesTransportCompatibilityKey(apiB, apiReq) {
		t.Fatal("rotated API key reused transport compatibility identity")
	}
}

func TestOpenAIAPIKeyResponsesWebsocketFreshEpochReusesSocketAndRotatesChain(t *testing.T) {
	var connections atomic.Int32
	var payloadsMu sync.Mutex
	var payloads []map[string]any
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer sk-test" && auth != "Bearer sk-rotated" {
			t.Errorf("Authorization = %q", auth)
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		connections.Add(1)
		defer conn.Close()
		for {
			_, encoded, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var payload map[string]any
			if err := json.Unmarshal(encoded, &payload); err != nil {
				t.Errorf("decode request: %v", err)
				return
			}
			payloadsMu.Lock()
			payloads = append(payloads, payload)
			responseNumber := len(payloads)
			payloadsMu.Unlock()
			completed := map[string]any{"type": "response.completed", "response": map[string]any{"id": fmt.Sprintf("resp-%d", responseNumber), "model": "gpt-5.4", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": fmt.Sprintf("answer-%d", responseNumber)}}}}}}
			if err := conn.WriteJSON(completed); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	client := NewClient(nil)
	client.responsesWSURL = "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	record := pebblestore.CodexAuthRecord{Provider: "openai", Type: pebblestore.CodexAuthTypeAPI, AccountScopeID: "account-a", ID: "credential-a", APIKey: "sk-test"}
	inputA := []map[string]any{{"role": "user", "content": "epoch-a-full"}}
	inputB := []map[string]any{{"role": "user", "content": "epoch-b-full"}}
	for i, tc := range []struct {
		lineage string
		cache   string
		input   []map[string]any
		apiKey  string
	}{{"lineage-a", "cache-a", inputA, "sk-test"}, {"lineage-b", "cache-b", inputB, "sk-test"}, {"lineage-c", "cache-c", []map[string]any{{"role": "user", "content": "epoch-c-full"}}, "sk-rotated"}} {
		record.APIKey = tc.apiKey
		response, err := client.CreateResponseWithAuth(context.Background(), record, Request{
			ProviderLineageID:         tc.lineage,
			ProviderCacheKey:          tc.cache,
			SessionAffinityKey:        "chain-" + tc.lineage,
			TransportAffinityKey:      "root-session",
			StartNewChain:             true,
			ReuseTransport:            true,
			ForceFreshProviderContext: true,
			Model:                     "gpt-5.4",
			Input:                     tc.input,
		})
		if err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		if response.ID == "" {
			t.Fatalf("request %d returned empty response id", i+1)
		}
	}
	if got := connections.Load(); got != 2 {
		t.Fatalf("websocket connections = %d, want reuse for compatible epochs and redial after API-key rotation", got)
	}
	payloadsMu.Lock()
	defer payloadsMu.Unlock()
	if len(payloads) != 3 {
		t.Fatalf("payload count = %d, want 3", len(payloads))
	}
	for i, payload := range payloads {
		if _, ok := payload["previous_response_id"]; ok {
			t.Fatalf("fresh epoch payload %d reused previous_response_id: %#v", i+1, payload)
		}
		if _, ok := payload["conversation"]; ok {
			t.Fatalf("fresh epoch payload %d set conversation: %#v", i+1, payload)
		}
		input := asSlice(payload["input"])
		if len(input) != 1 {
			t.Fatalf("fresh epoch payload %d input = %#v, want full single item", i+1, input)
		}
	}
	seenCacheKeys := map[string]struct{}{}
	for _, payload := range payloads {
		seenCacheKeys[asString(payload["prompt_cache_key"])] = struct{}{}
	}
	if len(seenCacheKeys) != len(payloads) {
		t.Fatalf("fresh epochs reused prompt cache identity: %#v", payloads)
	}
}

func TestCodexWebsocketIdleTimeoutReconnectsTwiceThenFails(t *testing.T) {
	var connections atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		connections.Add(1)
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		// Prove the idle deadline applies after provider activity, not only while
		// waiting for the first frame. The client must reset it for the next read.
		if err := conn.WriteJSON(map[string]any{"type": "response.created"}); err != nil {
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	client := NewClient(nil)
	client.responsesWSURL = "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	client.websocketIdleTimeout = 50 * time.Millisecond
	record := pebblestore.CodexAuthRecord{Provider: "codex", Type: pebblestore.CodexAuthTypeOAuth, AccountScopeID: "account-a", ID: "credential-a", AccountID: "chatgpt-a", AccessToken: "token"}
	_, err := client.CreateResponseWithAuth(context.Background(), record, Request{
		ProviderLineageID:    "lineage-a",
		ProviderCacheKey:     "cache-a",
		SessionAffinityKey:   "chain-a",
		TransportAffinityKey: "root-session",
		AllowContinuation:    true,
		ReuseTransport:       true,
		Model:                "gpt-5.4",
		Input:                []map[string]any{{"role": "user", "content": "first"}},
	})
	if err == nil {
		t.Fatal("idle websocket request error = nil, want timeout exhaustion")
	}
	if got := connections.Load(); got != websocketIdleTimeoutAttempts {
		t.Fatalf("websocket connections = %d, want %d total attempts", got, websocketIdleTimeoutAttempts)
	}
	if !strings.Contains(err.Error(), "codex timed out 3 times in a row") {
		t.Fatalf("idle websocket error = %q, want explicit three-timeout message", err)
	}
	if !errors.Is(err, errWebsocketIdleTimeout) {
		t.Fatalf("idle websocket error = %v, want idle timeout cause", err)
	}
}

func TestShouldRetryStartedWebsocketStreamRecognizesKeepaliveTimeout(t *testing.T) {
	keepalive := newStartedWebsocketStreamError(&websocket.CloseError{Code: websocket.CloseInternalServerErr, Text: "keepalive ping timeout"})
	if !shouldRetryStartedWebsocketStream(keepalive) {
		t.Fatal("keepalive timeout did not permit a fresh transport retry")
	}
	otherInternal := newStartedWebsocketStreamError(&websocket.CloseError{Code: websocket.CloseInternalServerErr, Text: "provider failure"})
	if shouldRetryStartedWebsocketStream(otherInternal) {
		t.Fatal("generic close 1011 unexpectedly permitted retry")
	}
}

func mustMarshalTestJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal test JSON: %v", err)
	}
	return string(encoded)
}
