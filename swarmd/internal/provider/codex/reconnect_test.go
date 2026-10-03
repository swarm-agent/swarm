package codex

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: send must recover typed 1012 closures before/after output with a
// bounded exponential delay, retaining text exactly once and publishing only the
// successful attempt's tools. Injecting at send's transport/wait boundaries is
// the narrowest deterministic layer proving replay and exhaustion postconditions.
func TestCodexServiceRestartRecovery(t *testing.T) {
	for _, partial := range []bool{false, true} {
		for _, exhausted := range []bool{false, true} {
			t.Run(strings.Join([]string{boolName(partial), boolName(exhausted)}, "/"), func(t *testing.T) {
				client := NewClient(nil)
				calls, notices, tools := 0, 0, 0
				var text string
				var waits []time.Duration
				client.reconnectWaitFn = func(_ context.Context, delay time.Duration) error {
					waits = append(waits, delay)
					return nil
				}
				client.sendWSFn = func(_ context.Context, _ pebblestore.CodexAuthRecord, _ []byte, emit func(StreamEvent)) (map[string]any, int, error) {
					calls++
					if partial {
						emit(StreamEvent{Type: StreamEventOutputTextDelta, Delta: "hello"})
						emit(StreamEvent{Type: StreamEventToolCallCompleted, ToolCallID: "call", ToolName: "read", Arguments: `{}`})
					}
					if exhausted || calls < 4 {
						err := error(&websocket.CloseError{Code: websocket.CloseServiceRestart})
						if partial {
							err = newStartedWebsocketStreamError(err)
						}
						return nil, 0, err
					}
					emit(StreamEvent{Type: StreamEventOutputTextDelta, Delta: "!"})
					return map[string]any{"id": "response"}, http.StatusOK, nil
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_, _, err := client.send(ctx, pebblestore.CodexAuthRecord{Type: pebblestore.CodexAuthTypeOAuth}, []byte(`{}`), func(event StreamEvent) {
					switch event.Type {
					case StreamEventOutputTextDelta:
						text += event.Delta
					case StreamEventAssistantCommentary:
						if !strings.Contains(event.Delta, "Reconnecting to Codex") {
							t.Errorf("unexpected notice: %q", event.Delta)
						}
						notices++
					case StreamEventToolCallCompleted:
						tools++
					}
				})
				if (err != nil) != exhausted || calls != 4 || notices != 3 {
					t.Fatalf("err=%v calls=%d notices=%d", err, calls, notices)
				}
				if !reflect.DeepEqual(waits, []time.Duration{300 * time.Millisecond, 600 * time.Millisecond, 1200 * time.Millisecond}) {
					t.Fatalf("waits=%v", waits)
				}
				wantText, wantTools := "", 0
				if partial {
					wantText = "hello"
					if !exhausted {
						wantTools = 1
					}
				}
				if !exhausted {
					wantText += "!"
				}
				if text != wantText || tools != wantTools {
					t.Fatalf("text=%q tools=%d; want %q/%d", text, tools, wantText, wantTools)
				}
			})
		}
	}
}

func boolName(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

// Requirement: cancellation during send's reconnect wait must win before another
// request or tool event. A synchronous cancellation hook avoids timer races while
// exercising the production cancellation-aware wait and final error boundary.
func TestCodexServiceRestartCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client := NewClient(nil)
	calls := 0
	client.sendWSFn = func(context.Context, pebblestore.CodexAuthRecord, []byte, func(StreamEvent)) (map[string]any, int, error) {
		calls++
		return nil, 0, &websocket.CloseError{Code: websocket.CloseServiceRestart}
	}
	client.reconnectWaitFn = func(ctx context.Context, delay time.Duration) error {
		cancel()
		return sleepWithContext(ctx, delay)
	}
	_, _, err := client.send(ctx, pebblestore.CodexAuthRecord{Type: pebblestore.CodexAuthTypeOAuth}, []byte(`{}`), nil)
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

// Requirement: replay is not stream resumption. Divergent regenerated text must
// fail closed rather than combine incompatible output or release tool calls.
// The send boundary proves preserved output and absence of tool side effects.
func TestCodexServiceRestartDivergence(t *testing.T) {
	client := NewClient(nil)
	client.reconnectWaitFn = func(context.Context, time.Duration) error { return nil }
	calls, tools := 0, 0
	text := ""
	client.sendWSFn = func(_ context.Context, _ pebblestore.CodexAuthRecord, _ []byte, emit func(StreamEvent)) (map[string]any, int, error) {
		calls++
		if calls == 1 {
			emit(StreamEvent{Type: StreamEventOutputTextDelta, Delta: "original"})
			return nil, 0, newStartedWebsocketStreamError(&websocket.CloseError{Code: websocket.CloseServiceRestart})
		}
		emit(StreamEvent{Type: StreamEventOutputTextDelta, Delta: "different"})
		emit(StreamEvent{Type: StreamEventToolCallCompleted, ToolCallID: "unsafe"})
		return map[string]any{"id": "response"}, http.StatusOK, nil
	}
	_, _, err := client.send(context.Background(), pebblestore.CodexAuthRecord{Type: pebblestore.CodexAuthTypeOAuth}, []byte(`{}`), func(event StreamEvent) {
		if event.Type == StreamEventOutputTextDelta {
			text += event.Delta
		}
		if event.Type == StreamEventToolCallCompleted {
			tools++
		}
	})
	if err == nil || !strings.Contains(err.Error(), "diverged") || calls != 2 || text != "original" || tools != 0 {
		t.Fatalf("err=%v calls=%d text=%q tools=%d", err, calls, text, tools)
	}
}

// Requirement: sendRequest must actually replace a failed websocket, not just
// re-invoke an injected callback. A loopback peer sends a real 1012 after a
// partial delta; this transport-layer test proves a second handshake, replay
// deduplication and terminal completion without external credentials/providers.
func TestCodexServiceRestartFreshSocket(t *testing.T) {
	var connections atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		attempt := connections.Add(1)
		_ = conn.WriteJSON(map[string]any{"type": "response.output_text.delta", "delta": "hello"})
		if attempt == 1 {
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseServiceRestart, "restart"), time.Now().Add(time.Second))
			return
		}
		_ = conn.WriteJSON(map[string]any{"type": "response.output_text.delta", "delta": "!"})
		_ = conn.WriteJSON(map[string]any{"type": "response.completed", "response": map[string]any{"id": "response", "status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "hello!"}}}}}})
	}))
	defer server.Close()
	client := NewClient(nil)
	client.responsesWSURL = "ws" + strings.TrimPrefix(server.URL, "http")
	client.reconnectWaitFn = func(context.Context, time.Duration) error { return nil }
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	text := ""
	_, status, err := client.sendRequest(ctx, pebblestore.CodexAuthRecord{Type: pebblestore.CodexAuthTypeOAuth}, Request{Model: "test-model", Input: []map[string]any{{"role": "user", "content": "hello"}}}, func(event StreamEvent) {
		if event.Type == StreamEventOutputTextDelta {
			text += event.Delta
		}
	})
	if err != nil || status != http.StatusOK || connections.Load() != 2 || text != "hello!" {
		t.Fatalf("err=%v status=%d connections=%d text=%q", err, status, connections.Load(), text)
	}
}

// Requirement: recovery must not reinterpret policy/auth/protocol closes as a
// service restart. The classifier is the narrowest boundary proving that typed
// permanent closes remain terminal, including after output began.
func TestCodexServiceRestartRejectsPermanentClose(t *testing.T) {
	for _, code := range []int{websocket.ClosePolicyViolation, websocket.CloseProtocolError, websocket.CloseMessageTooBig, websocket.CloseInternalServerErr} {
		err := newStartedWebsocketStreamError(&websocket.CloseError{Code: code})
		if isWebsocketServiceRestart(err) || shouldRetryStartedWebsocketStream(err) {
			t.Fatalf("permanent close %d permitted replay", code)
		}
	}
}
