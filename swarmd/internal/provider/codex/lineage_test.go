package codex

import (
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

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
