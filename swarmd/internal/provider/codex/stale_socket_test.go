package codex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: sendRequest must replace idle reused sockets closed before any stream
// payload, rebuilding complete input without counting a provider failure. A real
// loopback peer exercises sendWebsocketMap's read boundary and continuation cache,
// the narrowest layer proving a new handshake and lossless input reconstruction.
// Policy closes must remain terminal with no replay or released tool calls.
func TestCodexStaleSocketRecovery(t *testing.T) {
	for _, code := range []int{0, 1011, 1000, 1001, 1006, 1008} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var connections atomic.Int32
			closed := make(chan struct{})
			requests := make(chan map[string]any, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
				_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
				n := connections.Add(1)
				var request map[string]any
				if conn.ReadJSON(&request) != nil {
					return
				}
				requests <- request
				_ = conn.WriteJSON(map[string]any{"type": "response.completed", "response": map[string]any{"id": "prior", "status": "completed", "output": []any{}}})
				if n == 1 {
					if code == 0 {
						close(closed)
						if conn.ReadJSON(&request) == nil {
							requests <- request
						}
						return
					}
					if code != 1006 {
						_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, "keepalive ping timeout"), time.Now().Add(time.Second))
					}
					_ = conn.Close()
					close(closed)
				}
			}))
			defer server.Close()
			client := NewClient(nil)
			client.responsesWSURL = "ws" + strings.TrimPrefix(server.URL, "http")
			waits := 0
			client.reconnectWaitFn = func(context.Context, time.Duration) error { waits++; return nil }
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			ctx = contextWithCodexTransportContext(ctx, codexTransportContext{SessionAffinityKey: "idle-test", AllowContinuation: true})
			record := store.CodexAuthRecord{Type: store.CodexAuthTypeOAuth}
			req := Request{Model: "test-model", AllowContinuation: true, Input: []map[string]any{{"role": "user", "content": "first"}}}
			if _, _, err := client.sendRequest(ctx, record, req, nil); err != nil {
				t.Fatal(err)
			}
			select {
			case <-closed:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			<-requests
			if code == 0 {
				client.cachedWebsocketSession("idle-test").lastUsed = time.Now().Add(-time.Minute)
			}
			req.Input = append(req.Input, map[string]any{"role": "user", "content": "second"})
			tools := 0
			_, status, err := client.sendRequest(ctx, record, req, func(event StreamEvent) {
				if event.Type == StreamEventToolCallCompleted {
					tools++
				}
			})
			if code == 1008 {
				var closeErr *websocket.CloseError
				if !errors.As(err, &closeErr) || closeErr.Code != code || connections.Load() != 1 || tools != 0 {
					t.Fatalf("err=%v connections=%d tools=%d", err, connections.Load(), tools)
				}
				return
			}
			if err != nil || status != 200 || connections.Load() != 2 || waits != 0 {
				t.Fatalf("err=%v status=%d connections=%d waits=%d", err, status, connections.Load(), waits)
			}
			request := <-requests
			if asString(request["previous_response_id"]) != "" || len(asSlice(request["input"])) != 2 {
				t.Fatalf("reconnect lost full input: %#v", request)
			}
			closeCachedWebsocketSessionLocked(client.cachedWebsocketSession("idle-test"))
		})
	}
}

// Purpose: sendRequestOnce/send and retryAwareStreamEmitter must retain the
// existing started-1011 replay path, deduplicate text and withhold tools until
// successful completion. Hooks are the narrowest deterministic replay boundary.
func TestCodexStaleSocketStartedReplay(t *testing.T) {
	client := NewClient(nil)
	calls, tools, waits := 0, 0, 0
	text := ""
	client.reconnectWaitFn = func(context.Context, time.Duration) error { waits++; return nil }
	client.sendWSFn = func(_ context.Context, _ store.CodexAuthRecord, _ []byte, emit func(StreamEvent)) (map[string]any, int, error) {
		calls++
		emit(StreamEvent{Type: StreamEventOutputTextDelta, Delta: "hello"})
		emit(StreamEvent{Type: StreamEventToolCallCompleted, ToolCallID: "call", ToolName: "read", Arguments: `{}`})
		if calls == 1 {
			return nil, 0, newStartedWebsocketStreamError(&websocket.CloseError{Code: 1011, Text: "keepalive ping timeout"})
		}
		return map[string]any{"id": "done"}, 200, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, err := client.sendRequest(ctx, store.CodexAuthRecord{Type: store.CodexAuthTypeOAuth}, Request{Model: "test-model", Input: []map[string]any{{"role": "user", "content": "hello"}}}, func(event StreamEvent) {
		if event.Type == StreamEventOutputTextDelta {
			text += event.Delta
		}
		if event.Type == StreamEventToolCallCompleted {
			tools++
		}
	})
	if err != nil || calls != 2 || waits != 1 || text != "hello" || tools != 1 {
		t.Fatalf("err=%v calls=%d waits=%d text=%q tools=%d", err, calls, waits, text, tools)
	}
}

// Purpose: prepareCachedWebsocketSessionLocked must expire an idle socket before
// sending delta input on a new connection. A real socket and backdated timestamp
// avoid sleeps; this cache boundary proves socket replacement clears lineage and
// requires full-input rebuilding, while a recent socket remains reusable.
func TestCodexStaleSocketIdleExpiry(t *testing.T) {
	for _, idle := range []time.Duration{time.Second, time.Minute} {
		t.Run(idle.String(), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				_ = conn.SetReadDeadline(time.Now().Add(time.Second))
				_, _, _ = conn.ReadMessage()
			}))
			defer server.Close()
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			session := &cachedWebsocketSession{conn: conn, lastUsed: time.Now().Add(-idle), lastResponseID: "prior", lastInputLen: 1}
			active, reused, err := prepareCachedWebsocketSessionLocked(session, codexTransportContext{}, false, true, map[string]any{"previous_response_id": "prior"})
			if idle == time.Second {
				if err != nil || !reused || active != conn {
					t.Fatalf("healthy socket replaced: %v", err)
				}
			} else if !errors.Is(err, errWebsocketRetryFresh) || reused || active != nil || session.conn != nil || session.lastResponseID != "" || session.lastInputLen != 0 {
				t.Fatalf("idle socket retained: err=%v reused=%v active=%v lineage=%q", err, reused, active != nil, session.lastResponseID)
			}
		})
	}
}

// Purpose: isStaleWebsocketClose must admit EOF but reject started streams and
// policy errors; send's immediate stale recovery must be bounded to once. Hooks
// and unit classification are the narrowest boundaries proving no retry loop or
// bypass of replay safeguards when the replacement also fails.
func TestCodexStaleSocketRetryBound(t *testing.T) {
	if !isStaleWebsocketClose(io.EOF) || isStaleWebsocketClose(newStartedWebsocketStreamError(io.EOF)) || isStaleWebsocketClose(&websocket.CloseError{Code: 1008}) {
		t.Fatal("unsafe stale classification")
	}
	client := NewClient(nil)
	calls := 0
	client.sendWSFn = func(context.Context, store.CodexAuthRecord, []byte, func(StreamEvent)) (map[string]any, int, error) {
		calls++
		return nil, 0, errWebsocketStale
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, err := client.send(ctx, store.CodexAuthRecord{Type: store.CodexAuthTypeOAuth}, []byte(`{}`), nil)
	if !errors.Is(err, errWebsocketStale) || calls != 2 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
