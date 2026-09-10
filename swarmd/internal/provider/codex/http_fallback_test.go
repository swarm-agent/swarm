package codex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: sendRequest must prefer WebSockets, wait/retry rejected 503
// handshakes, then POST the full request to the same OAuth endpoint. This local
// HTTP boundary test prevents sticky fallback, wrong auth routing and replay of
// socket-local continuation state without requiring a live provider.
func TestCodexHTTPSFallbackAfterHandshakeRetries(t *testing.T) {
	var ws, posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			ws.Add(1)
			http.Error(w, "upstream connect error", http.StatusServiceUnavailable)
			return
		}
		posts.Add(1)
		if ws.Load() != posts.Load()*transportRetryAttempts {
			t.Error("HTTP fallback preceded exhausted WebSocket retries")
		}
		if r.Header.Get(chatGPTAccountIDHeader) != "test-account" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("OAuth identity missing from fallback")
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if len(asSlice(payload["input"])) != 1 || payload["previous_response_id"] != nil || payload["type"] != nil {
			t.Errorf("fallback must carry full HTTP payload: %#v", payload)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"status\":\"completed\",\"output\":[]}}\n\n"))
	}))
	defer server.Close()
	client := &Client{httpClient: server.Client(), responsesWSURL: server.URL}
	record := pebblestore.CodexAuthRecord{Type: pebblestore.CodexAuthTypeOAuth, AccessToken: "test-token", AccountID: "test-account"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		decoded, status, err := client.sendRequest(ctx, record, Request{Model: "test-model", Input: []map[string]any{{"role": "user", "content": "hello"}}}, nil)
		if err != nil || status != http.StatusOK || decoded[codexTransportMetadataKey] != codexTransportResponsesHTTP || decoded[codexConnectedViaWSMetadataKey] != false {
			t.Fatalf("fallback result = %#v, %d, %v", decoded, status, err)
		}
	}
	if posts.Load() != 2 || ws.Load() != 2*transportRetryAttempts {
		t.Fatalf("sticky or missing fallback: ws=%d posts=%d", ws.Load(), posts.Load())
	}
}

// Requirement: only transient rejected handshakes may fall back. The classifier
// is the narrow policy boundary excluding auth/rate-limit/client failures and
// established-socket 503 frames; neither may start an extra HTTP generation.
func TestCodexHTTPSFallbackEligibility(t *testing.T) {
	for _, status := range []int{200, 400, 401, 403, 404, 429, 500, 503, 504, 426} {
		for _, handshake := range []bool{false, true} {
			want := handshake && (status >= 500 || status == 426)
			if got := codexHandshakeShouldFallbackHTTP(status, map[string]any{"_swarm_websocket_handshake_failed": handshake}); got != want {
				t.Errorf("status=%d handshake=%v got=%v want=%v", status, handshake, got, want)
			}
		}
	}
}

// Requirement: cancellation and any previously emitted response must prevent
// fallback even if the final attempt reports an eligible rejected handshake.
// Injecting sendWS at the retry boundary proves there are no HTTP side effects.
func TestCodexHTTPSFallbackRejectsReplayAndCancellation(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		var posts atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { posts.Add(1) }))
		ctx, cancel := context.WithCancel(context.Background())
		client := &Client{httpClient: server.Client(), responsesWSURL: server.URL}
		client.sendWSFn = func(ctx context.Context, record pebblestore.CodexAuthRecord, payload []byte, emit func(StreamEvent)) (map[string]any, int, error) {
			if cancelRequest {
				cancel()
			} else {
				emit(StreamEvent{Type: StreamEventOutputTextDelta, Delta: "partial"})
			}
			return map[string]any{"_swarm_websocket_handshake_failed": true}, 503, nil
		}
		_, status, err := client.send(ctx, pebblestore.CodexAuthRecord{Type: pebblestore.CodexAuthTypeOAuth}, []byte(`{}`), nil)
		cancel()
		server.Close()
		if posts.Load() != 0 || (cancelRequest && err != context.Canceled) || (!cancelRequest && status != 503) {
			t.Fatalf("unsafe fallback: posts=%d status=%d err=%v", posts.Load(), status, err)
		}
	}
}

// Requirement: successful WebSockets must never POST, and a failed HTTP recovery
// must remain a failure rather than being annotated as WebSocket success. The
// injected retry boundary plus local HTTP server observes both postconditions.
func TestCodexHTTPSFallbackSuccessPreferenceAndHTTPFailure(t *testing.T) {
	for _, wsStatus := range []int{200, 503} {
		var posts atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			posts.Add(1)
			http.Error(w, "still unavailable", 503)
		}))
		client := &Client{httpClient: server.Client(), responsesWSURL: server.URL}
		client.sendWSFn = func(context.Context, pebblestore.CodexAuthRecord, []byte, func(StreamEvent)) (map[string]any, int, error) {
			return map[string]any{"_swarm_websocket_handshake_failed": wsStatus != 200}, wsStatus, nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		decoded, status, err := client.send(ctx, pebblestore.CodexAuthRecord{Type: pebblestore.CodexAuthTypeOAuth}, []byte(`{}`), nil)
		cancel()
		server.Close()
		if err != nil || status != wsStatus {
			t.Fatalf("status=%d err=%v", status, err)
		}
		if wsStatus == 200 && (posts.Load() != 0 || decoded[codexTransportMetadataKey] != codexTransportWebsocket) {
			t.Fatal("healthy WebSocket was not preferred")
		}
		if wsStatus == 503 && (posts.Load() != 1 || decoded[codexTransportMetadataKey] != codexTransportResponsesHTTP || decoded["raw_body"] == "") {
			t.Fatalf("HTTP failure lost: %#v posts=%d", decoded, posts.Load())
		}
	}
}
