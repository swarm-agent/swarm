package codex

import "testing"

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
