package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestStreamEventsWithReadyWaitsForSubscribedAck(t *testing.T) {
	readyStarted := make(chan struct{})
	allowSubscribed := make(chan struct{})
	var ready atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws" {
			t.Fatalf("websocket path = %q, want /ws", r.URL.Path)
		}
		conn, rw, err := hijackLifecycleTestWebsocket(w, r)
		if err != nil {
			t.Fatalf("hijack websocket: %v", err)
		}
		defer conn.Close()
		_, payload, err := readClientLifecycleTestFrame(rw)
		if err != nil {
			t.Fatalf("read subscribe frame: %v", err)
		}
		var subscribe map[string]any
		if err := json.Unmarshal(payload, &subscribe); err != nil {
			t.Fatalf("decode subscribe frame: %v", err)
		}
		if subscribe["type"] != "subscribe" || subscribe["channel"] != "session:session-v3" {
			t.Fatalf("subscribe frame = %#v", subscribe)
		}
		close(readyStarted)
		select {
		case <-allowSubscribed:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting to write subscribed ack")
		}
		writeServerLifecycleTestFrame(t, conn, map[string]any{"type": "subscribed", "ok": true, "channel": "session:session-v3"})
		for !ready.Load() {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- api.StreamEventsWithReady(ctx, 0, []string{"session:session-v3"}, func() {
			ready.Store(true)
			cancel()
		}, nil)
	}()

	select {
	case <-readyStarted:
	case err := <-done:
		t.Fatalf("StreamEventsWithReady returned before subscribe was read: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for subscribe frame")
	}
	select {
	case err := <-done:
		t.Fatalf("StreamEventsWithReady returned before subscribed ack: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(allowSubscribed)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("StreamEventsWithReady() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for stream completion")
	}
	if !ready.Load() {
		t.Fatalf("onReady was not called after subscribed ack")
	}
}
