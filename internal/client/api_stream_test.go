package client

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunSessionStreamConnectsToV2Path(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.URL.Path != "/v2/sessions/session-1/run/stream" {
			t.Fatalf("websocket path = %q, want /v2/sessions/session-1/run/stream", r.URL.Path)
		}
		conn, rw, err := hijackLifecycleTestWebsocket(w, r)
		if err != nil {
			t.Fatalf("hijack websocket: %v", err)
		}
		defer conn.Close()
		if _, _, err := readClientLifecycleTestFrame(rw); err != nil {
			t.Fatalf("read start frame: %v", err)
		}
		writeServerLifecycleTestFrame(t, conn, map[string]any{"type": "turn.completed", "session_id": "session-1", "result": map[string]any{"session_id": "session-1", "assistant_message": map[string]any{"id": "msg-1", "role": "assistant", "content": "ok"}}})
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	result, err := api.RunSessionStream(context.Background(), "session-1", "test prompt", "", "", nil)
	if err != nil {
		t.Fatalf("RunSessionStream() error = %v", err)
	}
	if got := strings.TrimSpace(result.SessionID); got != "session-1" {
		t.Fatalf("result session id = %q, want session-1", got)
	}
	if gotPath != "/v2/sessions/session-1/run/stream" {
		t.Fatalf("got path = %q", gotPath)
	}
}

func TestRunSessionStreamReconnectResumeUsesV2Path(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path != "/v2/sessions/session-1/run/stream" {
			t.Fatalf("websocket path = %q, want /v2/sessions/session-1/run/stream", r.URL.Path)
		}
		conn, rw, err := hijackLifecycleTestWebsocket(w, r)
		if err != nil {
			t.Fatalf("hijack websocket: %v", err)
		}
		defer conn.Close()

		_, payload, err := readClientLifecycleTestFrame(rw)
		if err != nil {
			t.Fatalf("read client frame: %v", err)
		}
		var msg map[string]any
		if err := json.Unmarshal(payload, &msg); err != nil {
			t.Fatalf("decode client frame: %v", err)
		}
		switch msg["type"] {
		case "run.start":
			writeServerLifecycleTestFrame(t, conn, map[string]any{"type": "assistant.delta", "session_id": "session-1", "run_id": "run-1", "seq": 3, "delta": "hello"})
			return
		case "run.resume":
			if msg["run_id"] != "run-1" {
				t.Fatalf("resume run_id = %#v, want run-1", msg["run_id"])
			}
			writeServerLifecycleTestFrame(t, conn, map[string]any{"type": "turn.completed", "session_id": "session-1", "run_id": "run-1", "seq": 4, "result": map[string]any{"session_id": "session-1", "assistant_message": map[string]any{"id": "msg-1", "role": "assistant", "content": "ok"}}})
		default:
			t.Fatalf("unexpected client message type: %#v", msg["type"])
		}
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	if _, err := api.RunSessionStream(context.Background(), "session-1", "test prompt", "", "", nil); err != nil {
		t.Fatalf("RunSessionStream() error = %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("websocket dials = %d, want 2 (%#v)", len(paths), paths)
	}
	for _, path := range paths {
		if path != "/v2/sessions/session-1/run/stream" {
			t.Fatalf("reconnect path = %q, want v2 stream path", path)
		}
	}
}

func TestRunSessionStreamPersistsClientDecodeErrorMessageToV2Path(t *testing.T) {
	var persistedPath string
	api := New("http://swarm.test")
	api.SetToken("test-token")
	api.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		persistedPath = req.URL.Path
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Request: req}, nil
	})}

	api.persistRunStreamClientError("session-1", "decode", net.ErrClosed)
	if persistedPath != "/v2/sessions/session-1/messages" {
		t.Fatalf("persisted path = %q, want /v2/sessions/session-1/messages", persistedPath)
	}
}
